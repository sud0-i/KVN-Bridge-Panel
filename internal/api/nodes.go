package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"golang.org/x/crypto/curve25519"
	"gorm.io/gorm"
)

var hostnameRe = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func (s *Server) listNodes(c echo.Context) error {
	var nodes []models.Node
	if err := s.db.Order("created_at").Find(&nodes).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	// В панели показываем реальное состояние: давно не выходившая на связь нода — офлайн
	for i := range nodes {
		nodes[i].IsOnline = s.isAlive(nodes[i])
	}
	return c.JSON(http.StatusOK, nodes)
}

func (s *Server) isAlive(n models.Node) bool {
	return n.IsOnline && s.now().Sub(n.LastSeen) < NodeAliveTimeout
}

// aliveNodes — ноды заданной роли, синхронизировавшиеся недавно
// cascadeExits — выходные ноды для моста: все, что хоть раз вышли на связь с текущими ключами.
// Упавшие отсеивает сам Xray (observatory), а если упали все — мост блокирует трафик,
// а не выпускает его со своего IP. Ноды, ещё не вышедшие на связь (деплой идёт или упал),
// не считаются: неудачно добавленная нода не должна отключать VPN.
// Нет ни одной такой ноды — одиночный режим: мост выпускает трафик сам.
func (s *Server) cascadeExits() ([]models.Node, error) {
	var nodes []models.Node
	err := s.db.Where("type = ? AND is_online = ?", protocol.RoleExit, true).Order("created_at").Find(&nodes).Error
	return nodes, err
}

func (s *Server) aliveNodes(role string) ([]models.Node, error) {
	var nodes []models.Node
	err := s.db.Where("type = ? AND is_online = ? AND last_seen > ?", role, true, s.now().Add(-NodeAliveTimeout)).
		Order("created_at").Find(&nodes).Error
	return nodes, err
}

// Добавить и развернуть новую ноду
func (s *Server) createNode(c echo.Context) error {
	var req struct {
		IP       string `json:"ip"`
		Type     string `json:"type"`     // "bridge" или "exit"
		Password string `json:"password"` // Пароль не храним в БД, используем только для Ansible!
		SNI      string `json:"sni"`      // необязательно
		Domain   string `json:"domain"`   // необязательно, для ссылок подписки
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}

	ip := net.ParseIP(strings.TrimSpace(req.IP))
	if ip == nil {
		return jsonError(c, http.StatusBadRequest, "Некорректный IP-адрес")
	}
	if req.Type != protocol.RoleBridge && req.Type != protocol.RoleExit {
		return jsonError(c, http.StatusBadRequest, "Неизвестная роль ноды")
	}
	if req.Password == "" {
		return jsonError(c, http.StatusBadRequest, "Нужен root-пароль сервера")
	}
	sni := strings.TrimSpace(req.SNI)
	if sni == "" {
		sni = s.cfg.DefaultSNI
	}
	if !hostnameRe.MatchString(sni) {
		return jsonError(c, http.StatusBadRequest, "Некорректный SNI")
	}
	domain := strings.TrimSpace(req.Domain)
	if domain != "" && !hostnameRe.MatchString(domain) {
		return jsonError(c, http.StatusBadRequest, "Некорректный домен")
	}

	newNode, secrets, err := CreateNode(s.db, NodeSpec{
		IP:     ip.String(),
		Role:   req.Type,
		SNI:    sni,
		Domain: domain,
	})
	if errors.Is(err, ErrNodeExists) {
		return jsonError(c, http.StatusConflict, "Нода с таким IP уже есть")
	}
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка создания ноды")
	}

	// Приватный ключ и токен уходят только на саму ноду, в БД Мастера их нет
	s.deploy(runner.DeployParams{
		IP:           newNode.IP,
		Role:         newNode.Type,
		RootPassword: req.Password,
		PrivateKey:   secrets.PrivateKey,
		NodeToken:    secrets.Token,
		MasterURL:    s.cfg.MasterURL,
	})

	return c.JSON(http.StatusAccepted, map[string]string{
		"message": "Установка запущена! Это займет 2-3 минуты.",
		"ip":      newNode.IP,
	})
}

// NodeSpec — параметры новой ноды
type NodeSpec struct {
	IP     string
	Role   string
	SNI    string
	Domain string
	// Dest — куда Reality отправляет посторонние подключения (по умолчанию SNI:443)
	Dest string
	// Xver — версия PROXY protocol для Dest (0 — выключен)
	Xver int
}

// NodeSecrets — то, что знает только сама нода
type NodeSecrets struct {
	PrivateKey string
	Token      string
}

var ErrNodeExists = errors.New("нода с таким IP уже есть")

// CreateNode генерирует ключи и токен ноды и сохраняет её в БД.
// Приватный ключ и токен в БД не попадают — их нужно сразу передать на ноду.
func CreateNode(db *gorm.DB, spec NodeSpec) (models.Node, NodeSecrets, error) {
	var exists int64
	if err := db.Model(&models.Node{}).Where("ip = ?", spec.IP).Count(&exists).Error; err != nil {
		return models.Node{}, NodeSecrets{}, err
	}
	if exists > 0 {
		return models.Node{}, NodeSecrets{}, ErrNodeExists
	}

	secrets, err := newSecrets()
	if err != nil {
		return models.Node{}, NodeSecrets{}, err
	}

	node := models.Node{
		IP:          spec.IP,
		Type:        spec.Role,
		Domain:      spec.Domain,
		SNI:         spec.SNI,
		RealityDest: spec.Dest,
		RealityXver: spec.Xver,
		PubKey:      secrets.pubKey,
		SID:         secrets.sid,
		LinkUUID:    uuid.NewString(),
		XHTTPPath:   newXHTTPPath(),
		TokenHash:   hashToken(secrets.Token),
		IsOnline:    false, // Онлайн нода станет после первой синхронизации агента
	}
	if err := db.Create(&node).Error; err != nil {
		return models.Node{}, NodeSecrets{}, err
	}
	return node, secrets.NodeSecrets, nil
}

// updateNode меняет настройки ноды без переустановки (сейчас — SNI).
// Нода подхватит изменения при следующей синхронизации, ссылки в подписке обновятся сами.
func (s *Server) updateNode(c echo.Context) error {
	var node models.Node
	if err := s.db.First(&node, "ip = ?", c.Param("ip")).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	// Меняется только то, что передано: {"sni": …}, {"label": …}, {"cdn": …}
	var req struct {
		SNI   *string `json:"sni"`
		Label *string `json:"label"`
		CDN   *string `json:"cdn"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	updates := map[string]any{}
	if req.SNI != nil {
		sni := strings.TrimSpace(*req.SNI)
		if !hostnameRe.MatchString(sni) {
			return jsonError(c, http.StatusBadRequest, "Некорректный SNI")
		}
		if node.RealityDest != "" {
			// Мост на сервере Мастера маскируется под собственный домен панели
			return jsonError(c, http.StatusBadRequest, "У моста на сервере Мастера SNI — это домен панели, его не меняют")
		}
		// Старая ошибка проверки SNI больше не актуальна — нода перепроверит новый
		updates["sni"], updates["sni_error"] = sni, ""
	}
	if req.Label != nil {
		label := strings.TrimSpace(*req.Label)
		if len([]rune(label)) > 40 || strings.ContainsFunc(label, unicode.IsControl) {
			return jsonError(c, http.StatusBadRequest, "Подпись: до 40 символов, без переводов строк")
		}
		updates["label"] = label
	}
	if req.CDN != nil {
		cdn := strings.ToLower(strings.TrimSpace(*req.CDN))
		if cdn != "" && !hostnameRe.MatchString(cdn) {
			return jsonError(c, http.StatusBadRequest, "CDN-домен — имя вида cdn.example.com")
		}
		updates["cdn_domain"] = cdn
	}
	if len(updates) == 0 {
		return jsonError(c, http.StatusBadRequest, "Нечего менять")
	}
	if err := s.db.Model(&node).Updates(updates).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "DB error")
	}
	return c.NoContent(http.StatusNoContent)
}

// redeployNode переустанавливает ноду на том же сервере: новые ключи и токен,
// те же IP, роль и SNI. Удобно, если деплой упал или сервер переустановили.
func (s *Server) redeployNode(c echo.Context) error {
	var node models.Node
	if err := s.db.First(&node, "ip = ?", c.Param("ip")).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	if node.RealityDest != "" {
		return jsonError(c, http.StatusBadRequest, "Мост на сервере Мастера переустанавливается через install.sh")
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	// Без пароля — по ключу мастера, если он уже стоит на ноде
	keyFile := ""
	if req.Password == "" {
		if !hasPanelKey(node) {
			return jsonError(c, http.StatusBadRequest, "Нужен root-пароль сервера")
		}
		if _, _, err := s.sshKey.load(); err != nil {
			return jsonError(c, http.StatusInternalServerError, "Ключ мастера: "+err.Error())
		}
		keyFile = s.sshKey.path
	}

	secrets, err := newSecrets()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка генерации ключей")
	}
	err = s.db.Model(&node).Updates(map[string]any{
		"pub_key":      secrets.pubKey,
		"s_id":         secrets.sid,
		"token_hash":   hashToken(secrets.Token),
		"is_online":    false,
		"config_error": "",
		"sni_error":    "",
		"warp_error":   "",
	}).Error
	if err == nil && req.Password != "" {
		// С паролем обычно переустанавливают после переустановки ОС — у сервера новый
		// SSH-ключ: забываем старый, а состояние ключей узнаем от нового агента
		err = s.db.Model(&node).Updates(map[string]any{"ssh_host_key": "", "ssh_state": ""}).Error
	}
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "DB error")
	}

	s.deploy(runner.DeployParams{
		IP:           node.IP,
		Role:         node.Type,
		RootPassword: req.Password,
		KeyFile:      keyFile,
		PrivateKey:   secrets.PrivateKey,
		NodeToken:    secrets.Token,
		MasterURL:    s.cfg.MasterURL,
	})
	return c.JSON(http.StatusAccepted, map[string]string{"message": "Переустановка запущена", "ip": node.IP})
}

// nodeSecrets — ключи Reality и токен агента для новой (пере)установки ноды
type nodeSecrets struct {
	NodeSecrets
	pubKey string
	sid    string
}

func newSecrets() (nodeSecrets, error) {
	pub, priv, sid, err := generateRealityKeys()
	if err != nil {
		return nodeSecrets{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return nodeSecrets{}, err
	}
	return nodeSecrets{NodeSecrets: NodeSecrets{PrivateKey: priv, Token: token}, pubKey: pub, sid: sid}, nil
}

func (s *Server) deleteNode(c echo.Context) error {
	res := s.db.Where("ip = ?", c.Param("ip")).Delete(&models.Node{})
	if res.Error != nil {
		return jsonError(c, http.StatusInternalServerError, "DB error")
	}
	if res.RowsAffected == 0 {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	return c.NoContent(http.StatusNoContent)
}

// generateRealityKeys создаёт пару ключей x25519 и Short ID для Reality
func generateRealityKeys() (pubKey, privKey, sid string, err error) {
	var priv [32]byte
	if _, err = rand.Read(priv[:]); err != nil {
		return
	}
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return
	}

	// Xray использует Base64-URL кодировку без паддинга (символов =)
	pubKey = base64.RawURLEncoding.EncodeToString(pub)
	privKey = base64.RawURLEncoding.EncodeToString(priv[:])
	sid, err = randomHex(8)
	return
}

// newXHTTPPath — случайный путь XHTTP, чтобы его нельзя было угадать снаружи
func newXHTTPPath() string {
	h, err := randomHex(8)
	if err != nil {
		h = uuid.NewString()[:16]
	}
	return "/" + h
}

// EnsureXHTTPPaths выдаёт путь XHTTP нодам, созданным до появления XHTTP
func EnsureXHTTPPaths(db *gorm.DB) error {
	var nodes []models.Node
	if err := db.Where("x_http_path = '' OR x_http_path IS NULL").Find(&nodes).Error; err != nil {
		return err
	}
	for _, n := range nodes {
		if err := db.Model(&models.Node{}).Where("ip = ?", n.IP).Update("x_http_path", newXHTTPPath()).Error; err != nil {
			return err
		}
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
