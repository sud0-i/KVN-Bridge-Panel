package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"golang.org/x/crypto/ssh"
)

// fakeSSHD — SSH-сервер для тестов: пускает root по одному ключу, на «оболочку»
// отвечает эхом и сообщает об изменении размера окна
type fakeSSHD struct {
	addr    string
	allowed ssh.PublicKey
	mu      sync.Mutex
	pty     string
	sizes   []string
}

func newFakeSSHD(t *testing.T, allowed ssh.PublicKey) *fakeSSHD {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewSignerFromKey(priv)
	f := &fakeSSHD{allowed: allowed}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if m.User() == "root" && f.allowed != nil && bytes.Equal(k.Marshal(), f.allowed.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, cfg)
		}
	}()
	return f
}

func (f *fakeSSHD) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for r := range creqs {
				switch r.Type {
				case "pty-req":
					n := binary.BigEndian.Uint32(r.Payload)
					cols := binary.BigEndian.Uint32(r.Payload[4+n:])
					rows := binary.BigEndian.Uint32(r.Payload[8+n:])
					f.mu.Lock()
					f.pty = fmt.Sprintf("%s %dx%d", r.Payload[4:4+n], cols, rows)
					f.mu.Unlock()
					r.Reply(true, nil)
				case "shell":
					r.Reply(true, nil)
					go func() {
						ch.Write([]byte("welcome\r\n"))
						buf := make([]byte, 1024)
						for {
							n, err := ch.Read(buf)
							if err != nil {
								return
							}
							if bytes.Contains(buf[:n], []byte("exit\r")) {
								ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
								ch.Close()
								return
							}
							ch.Write(bytes.ToUpper(buf[:n]))
						}
					}()
				case "window-change":
					f.mu.Lock()
					f.sizes = append(f.sizes, fmt.Sprintf("%dx%d", binary.BigEndian.Uint32(r.Payload), binary.BigEndian.Uint32(r.Payload[4:])))
					f.mu.Unlock()
				default:
					r.Reply(false, nil)
				}
			}
		}()
	}
}

func masterPub(t *testing.T, ev *env) ssh.PublicKey {
	signer, _, err := ev.s.sshKey.load()
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

func TestSSHKeysSync(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	token := ev.addNode("10.0.0.1", protocol.RoleExit)

	resp := ev.syncAs(token)
	if len(resp.SSHKeys) != 1 || !strings.HasSuffix(resp.SSHKeys[0], " kvn-master") || resp.SSHKeysOnly {
		t.Fatalf("ключи в синхронизации: %+v", resp.SSHKeys)
	}
	// Ключ мастера переживает перезапуск (лежит в файле)
	again := (&masterKey{path: ev.s.sshKey.path})
	if _, pub, _ := again.load(); pub != resp.SSHKeys[0] {
		t.Fatal("ключ мастера пересоздался")
	}

	// Личные ключи: проверка формата, без опций authorized_keys
	if rec := ev.do("PUT", "/api/ssh-keys", `{"admin":"not a key"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatal("мусор вместо ключа")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sg, _ := ssh.NewSignerFromKey(priv)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sg.PublicKey())))
	if rec := ev.do("PUT", "/api/ssh-keys", `{"admin":"command=\"rm -rf /\" `+strings.ReplaceAll(line, `"`, `\"`)+`"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatal("ключ с опциями принимать нельзя")
	}
	body, _ := json.Marshal(map[string]string{"admin": "# мой ноутбук\n" + line + " me@laptop\n\n"})
	if rec := ev.do("PUT", "/api/ssh-keys", string(body), adm); rec.Code != http.StatusOK {
		t.Fatalf("сохранение: %d %s", rec.Code, rec.Body)
	}
	if resp := ev.syncAs(token); len(resp.SSHKeys) != 2 || resp.SSHKeys[1] != line+" me@laptop" {
		t.Fatalf("личный ключ не ушёл на ноду: %v", resp.SSHKeys)
	}

	// Состояние от агента
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token, protocol.SSHHeader: "keys"})
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.1")
	if n.SSHState != "keys" {
		t.Fatalf("ssh_state: %q", n.SSHState)
	}
}

func TestKeysOnlyNeedsWorkingKey(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	token := ev.addNode("10.0.0.1", protocol.RoleExit)
	sshd := newFakeSSHD(t, nil) // ключ мастера пока не пускают
	ev.s.sshAddr = func(string) string { return sshd.addr }

	if rec := ev.do("POST", "/api/nodes/10.0.0.1/ssh", `{"keys_only":true}`, adm); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ещё не поставил") {
		t.Fatalf("без ключа на ноде: %d %s", rec.Code, rec.Body)
	}
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token, protocol.SSHHeader: "keys"})
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/ssh", `{"keys_only":true}`, adm); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "вход по ключу мастера не удался") {
		t.Fatalf("ключ не пускают: %d %s", rec.Code, rec.Body)
	}
	sshd.allowed = masterPub(t, ev)
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/ssh", `{"keys_only":true}`, adm); rec.Code != http.StatusNoContent {
		t.Fatalf("включение: %d %s", rec.Code, rec.Body)
	}
	if !ev.syncAs(token).SSHKeysOnly {
		t.Fatal("нода не получила keys-only")
	}
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.1")
	if n.SSHHostKey == "" {
		t.Fatal("ключ сервера не запомнен")
	}

	// Другой сервер на том же адресе — отказ
	other := newFakeSSHD(t, masterPub(t, ev))
	ev.s.sshAddr = func(string) string { return other.addr }
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/ssh", `{"keys_only":true}`, adm); !strings.Contains(rec.Body.String(), "ключ SSH-сервера ноды изменился") {
		t.Fatalf("подмена сервера: %s", rec.Body)
	}
	// Выключить можно всегда
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/ssh", `{"keys_only":false}`, adm); rec.Code != http.StatusNoContent || ev.syncAs(token).SSHKeysOnly {
		t.Fatal("выключение")
	}

	// Переустановка: без пароля — ключом мастера; с паролем — забываем ключ сервера
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/redeploy", `{}`, adm); rec.Code != http.StatusAccepted {
		t.Fatalf("переустановка по ключу: %d %s", rec.Code, rec.Body)
	}
	if p := ev.deployed[len(ev.deployed)-1]; p.RootPassword != "" || p.KeyFile != ev.s.sshKey.path {
		t.Fatalf("параметры деплоя: %+v", p)
	}
	ev.do("POST", "/api/nodes/10.0.0.1/redeploy", `{"password":"pw"}`, adm)
	ev.db.First(&n, "ip = ?", "10.0.0.1")
	if n.SSHHostKey != "" || n.SSHState != "" {
		t.Fatal("после переустановки с паролем ключ сервера надо забыть")
	}
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/redeploy", `{}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatal("без ключа на ноде нужен пароль")
	}
}

func TestTerminal(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	totpUsed = 0
	token := ev.addNode("10.0.0.1", protocol.RoleExit)
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token, protocol.SSHHeader: "keys-only"})
	sshd := newFakeSSHD(t, masterPub(t, ev))
	ev.s.sshAddr = func(string) string { return sshd.addr }
	ip := map[string]string{"Authorization": adm["Authorization"], "X-Real-IP": "203.0.113.5"}

	if rec := ev.do("POST", "/api/nodes/10.0.0.1/terminal", `{"code":"123456"}`, ip); rec.Code != http.StatusForbidden {
		t.Fatalf("без 2FA терминал закрыт: %d", rec.Code)
	}
	secret := "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	saveTOTP(ev.db, totpState{Enabled: true, Secret: secret})
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/terminal", `{}`, ip); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "need_code") {
		t.Fatal("нужен код (403, не 401 — иначе панель разлогинит)")
	}
	rec := ev.do("POST", "/api/nodes/10.0.0.1/terminal", `{"code":"`+totpCode(secret, ev.now.Unix()/30)+`"}`, ip)
	var tk map[string]string
	json.Unmarshal(rec.Body.Bytes(), &tk)
	if rec.Code != http.StatusOK || len(tk["ticket"]) != 48 {
		t.Fatalf("пропуск: %d %s", rec.Code, rec.Body)
	}
	// Тот же код второй раз не годится
	if rec := ev.do("POST", "/api/nodes/10.0.0.1/terminal", `{"code":"`+totpCode(secret, ev.now.Unix()/30)+`"}`, ip); !strings.Contains(rec.Body.String(), "уже использован") {
		t.Fatalf("повтор кода: %s", rec.Body)
	}

	srv := httptest.NewServer(ev.e)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/terminal?cols=100&rows=30&ticket="
	dial := func(ticket, from string) (*websocket.Conn, error) {
		c, _, err := websocket.DefaultDialer.Dial(wsURL+ticket, http.Header{"X-Real-IP": {from}})
		return c, err
	}
	if _, err := dial(tk["ticket"], "198.51.100.1"); err == nil {
		t.Fatal("пропуск с другого IP")
	}
	// Пропуск одноразовый: после попытки с чужого IP он сгорел — берём новый
	ev.now = ev.now.Add(30 * time.Second)
	json.Unmarshal(ev.do("POST", "/api/nodes/10.0.0.1/terminal", `{"code":"`+totpCode(secret, ev.now.Unix()/30)+`"}`, ip).Body.Bytes(), &tk)
	ws, err := dial(tk["ticket"], "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if _, err := dial(tk["ticket"], "203.0.113.5"); err == nil {
		t.Fatal("пропуск использован повторно")
	}

	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var out bytes.Buffer
	read := func(until string) {
		t.Helper()
		for !strings.Contains(out.String(), until) {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				t.Fatalf("ждали %q, есть %q: %v", until, out.String(), err)
			}
			if kind == websocket.BinaryMessage {
				out.Write(data)
			} else {
				out.WriteString("{" + string(data) + "}")
			}
		}
	}
	read(`"type":"ready"`)
	read("welcome")
	ws.WriteMessage(websocket.BinaryMessage, []byte("uptime\r"))
	read("UPTIME")
	ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":120,"rows":40}`))
	ws.WriteMessage(websocket.BinaryMessage, []byte("exit\r"))
	read("Сессия закрыта: выход из оболочки")
	sshd.mu.Lock()
	if sshd.pty != "xterm-256color 100x30" || len(sshd.sizes) != 1 || sshd.sizes[0] != "120x40" {
		t.Fatalf("pty %q, размеры %v", sshd.pty, sshd.sizes)
	}
	sshd.mu.Unlock()

	time.Sleep(100 * time.Millisecond)
	var logRows []models.TerminalSession
	json.Unmarshal(ev.do("GET", "/api/terminal/log", "", adm).Body.Bytes(), &logRows)
	if len(logRows) != 1 || logRows[0].RemoteIP != "203.0.113.5" || logRows[0].EndedAt == nil || logRows[0].Reason != "выход из оболочки" {
		t.Fatalf("журнал: %+v", logRows)
	}
}
