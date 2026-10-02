package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"golang.org/x/crypto/ssh"
)

// Терминал ноды в панели: браузер ↔ WebSocket ↔ мастер ↔ SSH (ключ мастера) ↔ нода.
// От агента не зависит — работает, даже если агент или Xray на ноде упали.
// Вход только с включённой 2FA и по свежему коду на каждую сессию.

const (
	terminalTicketTTL = time.Minute
	terminalIdle      = 15 * time.Minute
	terminalMaxLife   = 8 * time.Hour
	terminalMax       = 5 // одновременных сессий на всю панель
)

type terminalTicket struct {
	node    string
	ip      string
	expires time.Time
}

type terminals struct {
	mu      sync.Mutex
	tickets map[string]terminalTicket
	active  int
}

// sshAddr — адрес SSH ноды (в тестах подменяется на локальный сервер)
func (s *Server) sshAddrOf(ip string) string {
	if s.sshAddr != nil {
		return s.sshAddr(ip)
	}
	return net.JoinHostPort(ip, "22")
}

var errHostKeyChanged = errors.New("ключ SSH-сервера ноды изменился с прошлого входа. Если сервер переустанавливали — переустановите ноду из панели (это сбросит запомненный ключ). Если нет — кто-то может подменять соединение")

// sshDial заходит на ноду root'ом с ключом мастера. Ключ сервера запоминается при
// первом входе и дальше сверяется (как known_hosts у OpenSSH).
func (s *Server) sshDial(node models.Node) (*ssh.Client, error) {
	signer, _, err := s.sshKey.load()
	if err != nil {
		return nil, fmt.Errorf("ключ мастера: %w", err)
	}
	known := node.SSHHostKey
	cfg := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got := base64.StdEncoding.EncodeToString(key.Marshal())
			if known == "" {
				s.db.Model(&models.Node{}).Where("ip = ?", node.IP).Update("ssh_host_key", got)
				return nil
			}
			if got != known {
				return errHostKeyChanged
			}
			return nil
		},
		Timeout: 10 * time.Second,
	}
	client, err := ssh.Dial("tcp", s.sshAddrOf(node.IP), cfg)
	if err != nil {
		if errors.Is(err, errHostKeyChanged) {
			return nil, errHostKeyChanged
		}
		var nerr net.Error
		if errors.As(err, &nerr) {
			return nil, fmt.Errorf("нет соединения с %s по SSH (порт 22): %v", node.IP, err)
		}
		return nil, fmt.Errorf("вход по ключу мастера не удался: %v. Агент ставит ключ в /root/.ssh/authorized_keys — обновите агент или подождите минуту", err)
	}
	return client, nil
}

// hasPanelKey — агент сообщил, что ключ мастера стоит
func hasPanelKey(n models.Node) bool { return n.SSHState == "keys" || n.SSHState == "keys-only" }

// setNodeSSH — вход на ноду по паролю: выключить можно, только убедившись, что
// ключ мастера работает (иначе можно остаться без доступа к серверу)
func (s *Server) setNodeSSH(c echo.Context) error {
	var node models.Node
	if err := s.db.First(&node, "ip = ?", c.Param("ip")).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	var req struct {
		KeysOnly bool `json:"keys_only"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if req.KeysOnly {
		if !hasPanelKey(node) {
			return jsonError(c, http.StatusBadRequest, "Агент ноды ещё не поставил ключ мастера — обновите агент и подождите минуту")
		}
		client, err := s.sshDial(node)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, "Проверка входа по ключу: "+err.Error())
		}
		client.Close()
	}
	s.db.Model(&node).Update("ssh_keys_only", req.KeysOnly)
	return c.NoContent(http.StatusNoContent)
}

// terminalTicket — одноразовый пропуск на открытие терминала (WebSocket не умеет
// заголовок Authorization, поэтому JWT не подходит; пропуск живёт минуту)
func (s *Server) terminalTicket(c echo.Context) error {
	var node models.Node
	if err := s.db.First(&node, "ip = ?", c.Param("ip")).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	st := loadTOTP(s.db)
	if !st.Enabled {
		return jsonError(c, http.StatusForbidden, "Терминал доступен только с включённой двухфакторной защитой (Настройки → Вход в панель)")
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if err := s.checkSecondFactor(&st, req.Code); err != nil {
		log.Printf("🔐 Терминал %s: неверный код 2FA с %s", node.IP, c.RealIP())
		// 403, а не 401: на 401 панель считает, что вход истёк, и разлогинивает
		return c.JSON(http.StatusForbidden, map[string]any{"error": err.Error(), "need_code": true})
	}
	if !hasPanelKey(node) {
		return jsonError(c, http.StatusBadRequest, "Агент ноды ещё не поставил ключ мастера — обновите агент и подождите минуту")
	}
	ticket, err := randomHex(24)
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка генерации")
	}
	t := &s.term
	t.mu.Lock()
	if t.tickets == nil {
		t.tickets = map[string]terminalTicket{}
	}
	for k, v := range t.tickets {
		if s.now().After(v.expires) {
			delete(t.tickets, k)
		}
	}
	t.tickets[ticket] = terminalTicket{node: node.IP, ip: c.RealIP(), expires: s.now().Add(terminalTicketTTL)}
	t.mu.Unlock()
	return c.JSON(http.StatusOK, map[string]string{"ticket": ticket})
}

func (s *Server) takeTicket(ticket, ip string) (string, bool) {
	t := &s.term
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.tickets[ticket]
	delete(t.tickets, ticket)
	if !ok || s.now().After(v.expires) || v.ip != ip {
		return "", false
	}
	if t.active >= terminalMax {
		return "", false
	}
	t.active++
	return v.node, true
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 32768}

// terminalWS — сама сессия: байты терминала идут бинарными сообщениями,
// служебные (размер окна, ошибки) — текстовыми JSON
func (s *Server) terminalWS(c echo.Context) error {
	nodeIP, ok := s.takeTicket(c.QueryParam("ticket"), c.RealIP())
	if !ok {
		return jsonError(c, http.StatusUnauthorized, "Пропуск в терминал недействителен — откройте терминал заново")
	}
	defer func() { s.term.mu.Lock(); s.term.active--; s.term.mu.Unlock() }()

	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return nil // ответ уже отправлен апгрейдером
	}
	defer ws.Close()
	ws.SetReadLimit(64 << 10)

	var wmu sync.Mutex
	send := func(kind int, data []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return ws.WriteMessage(kind, data)
	}
	sendJSON := func(v map[string]any) { b, _ := json.Marshal(v); _ = send(websocket.TextMessage, b) }
	fail := func(msg string) error {
		sendJSON(map[string]any{"type": "error", "message": msg})
		_ = send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		return nil
	}

	var node models.Node
	if err := s.db.First(&node, "ip = ?", nodeIP).Error; err != nil {
		return fail("Нода не найдена")
	}
	client, err := s.sshDial(node)
	if err != nil {
		return fail(err.Error())
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return fail("SSH: " + err.Error())
	}
	defer sess.Close()

	cols, _ := strconv.Atoi(c.QueryParam("cols"))
	rows, _ := strconv.Atoi(c.QueryParam("rows"))
	if cols < 20 || cols > 500 {
		cols = 80
	}
	if rows < 5 || rows > 200 {
		rows = 24
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		return fail("SSH: " + err.Error())
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	sess.Stderr = nil // при PTY stderr приходит вместе с stdout
	if err := sess.Shell(); err != nil {
		return fail("SSH: " + err.Error())
	}

	// Журнал и уведомление
	rec := models.TerminalSession{Node: node.IP, RemoteIP: c.RealIP(), StartedAt: s.now()}
	s.db.Create(&rec)
	log.Printf("🖥 Терминал: вход на %s с %s", node.IP, c.RealIP())
	if n := s.loadNotify(); n.active() {
		s.enqueue(n, notifyText(n.Lang, "terminal", nodeName(node), c.RealIP()))
	}
	sendJSON(map[string]any{"type": "ready", "idle_minutes": int(terminalIdle / time.Minute)})

	ctx, cancel := context.WithTimeout(context.Background(), terminalMaxLife)
	defer cancel()
	reason := make(chan string, 4)
	end := func(r string) {
		select {
		case reason <- r:
		default:
		}
		cancel()
	}

	// Нода → браузер
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if send(websocket.BinaryMessage, buf[:n]) != nil {
					end("обрыв связи с браузером")
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					end("выход из оболочки")
				} else {
					end("SSH: " + err.Error())
				}
				return
			}
		}
	}()

	// Браузер → нода
	var lastInput sync.Mutex
	last := time.Now()
	go func() {
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				end("терминал закрыт")
				return
			}
			switch kind {
			case websocket.BinaryMessage:
				lastInput.Lock()
				last = time.Now()
				lastInput.Unlock()
				if _, err := stdin.Write(data); err != nil {
					end("SSH: " + err.Error())
					return
				}
			case websocket.TextMessage:
				var m struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if json.Unmarshal(data, &m) == nil && m.Type == "resize" && m.Cols >= 20 && m.Cols <= 500 && m.Rows >= 5 && m.Rows <= 200 {
					_ = sess.WindowChange(m.Rows, m.Cols)
				}
			}
		}
	}()

	// Простой и проверка связи (прокси закрывают молчащие WebSocket)
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				lastInput.Lock()
				idle := time.Since(last)
				lastInput.Unlock()
				if idle >= terminalIdle {
					end(fmt.Sprintf("%d минут без ввода", int(terminalIdle/time.Minute)))
					return
				}
				wmu.Lock()
				_ = ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				wmu.Unlock()
			}
		}
	}()

	<-ctx.Done()
	why := "8 часов — максимальная длина сессии"
	select {
	case why = <-reason:
	default:
	}
	ended := s.now()
	s.db.Model(&rec).Updates(map[string]any{"ended_at": ended, "reason": why})
	log.Printf("🖥 Терминал %s закрыт: %s", node.IP, why)
	var b bytes.Buffer
	fmt.Fprintf(&b, "\r\n\x1b[2m[kvn] Сессия закрыта: %s\x1b[0m\r\n", why)
	_ = send(websocket.BinaryMessage, b.Bytes())
	sendJSON(map[string]any{"type": "closed", "reason": why})
	_ = send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	return nil
}

// terminalLog — журнал сессий (последние 50)
func (s *Server) terminalLog(c echo.Context) error {
	var rows []models.TerminalSession
	s.db.Order("started_at desc").Limit(50).Find(&rows)
	return c.JSON(http.StatusOK, rows)
}
