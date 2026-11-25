package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"gorm.io/gorm"
)

const awgAvailability = 3 * time.Minute

func awgNetwork(address string) (netip.Prefix, bool) {
	p, err := netip.ParsePrefix(address)
	return p, err == nil && p.Addr().Is4() && p.Addr().IsPrivate() && p.Bits() >= 16 && p.Bits() <= 30
}

func (s *Server) syncAWG(c echo.Context) error {
	node := c.Get(ctxNode).(*models.Node)
	if node.Type != protocol.RoleExit {
		return notFound(c)
	}
	var report protocol.AWGStatus
	if err := c.Bind(&report); err != nil || !protocol.AWGKeyValid(report.PublicKey) ||
		report.Port == 0 || len(report.Epoch) == 0 || len(report.Epoch) > 100 || len(report.Peers) > 512 {
		return jsonError(c, http.StatusBadRequest, "Недопустимое состояние AWG")
	}
	pool, ok := awgNetwork(report.Address)
	if !ok {
		return jsonError(c, http.StatusBadRequest, "Нужна частная IPv4-подсеть AWG")
	}
	current := make(map[string]protocol.AWGCounter, len(report.Peers))
	for _, p := range report.Peers {
		if !protocol.AWGKeyValid(p.PublicKey) || p.Rx < 0 || p.Tx < 0 || p.Handshake < 0 {
			return jsonError(c, http.StatusBadRequest, "Некорректные счётчики AWG")
		}
		current[p.PublicKey] = p
	}
	report.Peers = nil
	report.Options = protocol.AWGClientOptions(report.Options)
	if !protocol.AWGKeyValid(report.Options["HeaderProtectionKey"]) {
		return jsonError(c, http.StatusBadRequest, "Не настроен ключ защиты заголовков AWG 3.1")
	}
	profile, _ := json.Marshal(report)
	desired := make([]protocol.AWGPeer, 0)
	now := s.now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(node).Updates(map[string]any{"awg_profile": string(profile), "awg_seen": now, "awg_port": report.Port}).Error; err != nil {
			return err
		}
		var devices []models.AWGDevice
		if err := tx.Find(&devices, "node_ip = ? AND public_key <> ''", node.IP).Error; err != nil {
			return err
		}
		for _, device := range devices {
			if err := accountAWG(tx, device, report.Epoch, current[device.PublicKey]); err != nil {
				return err
			}
		}
		var users []models.User
		if err := tx.Where("id IN (?)", tx.Model(&models.AWGDevice{}).Select("user_id").
			Where("node_ip = ? AND revoked = ?", node.IP, false)).Find(&users).Error; err != nil {
			return err
		}
		active := make(map[string]bool, len(users))
		for _, u := range users {
			active[u.ID] = u.CanConnect(now)
		}
		for _, device := range devices {
			addr, err := netip.ParseAddr(device.Address)
			if active[device.UserID] && !device.Revoked && err == nil && awgUsableIP(pool, addr) {
				desired = append(desired, protocol.AWGPeer{PublicKey: device.PublicKey, IP: device.Address})
			}
		}
		return nil
	})
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка записи статистики AWG")
	}
	return c.JSON(http.StatusOK, desired)
}

func accountAWG(tx *gorm.DB, d models.AWGDevice, epoch string, p protocol.AWGCounter) error {
	rx, txBytes := p.Rx, p.Tx
	if d.CounterEpoch == epoch {
		if rx >= d.CounterRx {
			rx -= d.CounterRx
		}
		if txBytes >= d.CounterTx {
			txBytes -= d.CounterTx
		}
	}
	if p.PublicKey == "" && d.CounterEpoch == "" {
		return nil
	}
	update := map[string]any{"counter_epoch": epoch, "counter_rx": p.Rx, "counter_tx": p.Tx}
	if p.PublicKey == "" {
		update["counter_epoch"] = ""
	}
	if p.Handshake > d.Handshake {
		update["handshake"] = p.Handshake
	}
	if rx != 0 || txBytes != 0 {
		update["traffic_up"] = gorm.Expr("traffic_up + ?", rx)
		update["traffic_down"] = gorm.Expr("traffic_down + ?", txBytes)
		if err := tx.Model(&models.User{}).Where("id = ?", d.UserID).Updates(map[string]any{
			"traffic_up": gorm.Expr("traffic_up + ?", rx), "traffic_down": gorm.Expr("traffic_down + ?", txBytes),
		}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&d).Updates(update).Error
}

func awgUsableIP(pool netip.Prefix, candidate netip.Addr) bool {
	return pool.Contains(candidate) && candidate != pool.Addr() &&
		candidate != pool.Masked().Addr() && pool.Contains(candidate.Next())
}

func (s *Server) listAWGDevices(c echo.Context) error {
	var devices []models.AWGDevice
	if err := s.db.Where("user_id = ?", c.Param("id")).Order("label, id").Find(&devices).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	var nodes []models.Node
	if err := s.db.Where("type = ? AND awg_seen > ?", protocol.RoleExit, s.now().Add(-awgAvailability)).Find(&nodes).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, map[string]any{"devices": devices, "nodes": nodes})
}

func (s *Server) issueAWGDevice(c echo.Context) error {
	var input struct {
		Node  string `json:"node"`
		Label string `json:"label"`
	}
	if err := c.Bind(&input); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверные параметры устройства")
	}
	label := strings.TrimSpace(input.Label)
	if label == "" || len([]rune(label)) > 40 || strings.ContainsFunc(label, unicode.IsControl) {
		return jsonError(c, http.StatusBadRequest, "Имя устройства должно содержать от 1 до 40 символов")
	}
	var u models.User
	if err := s.db.First(&u, "id = ?", c.Param("id")).Error; err != nil || !u.CanConnect(s.now()) {
		return jsonError(c, http.StatusBadRequest, "Подписка не активна")
	}
	var node models.Node
	if err := s.db.First(&node, "ip = ? AND type = ?", input.Node, protocol.RoleExit).Error; err != nil || s.now().Sub(node.AWGSeen) >= awgAvailability {
		return jsonError(c, http.StatusBadRequest, "Нода AWG сейчас недоступна")
	}
	var status protocol.AWGStatus
	if json.Unmarshal([]byte(node.AWGProfile), &status) != nil {
		return jsonError(c, http.StatusInternalServerError, "Не удалось прочитать профиль ноды")
	}
	pool, ok := awgNetwork(status.Address)
	if !ok {
		return jsonError(c, http.StatusBadRequest, "Неверная сеть AWG на ноде")
	}
	s.awgIssueMu.Lock()
	defer s.awgIssueMu.Unlock()
	if err := s.db.Delete(&models.AWGDevice{}, "node_ip = ? AND public_key = '' AND invite_until <= ?", node.IP, s.now()).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	var used []string
	if err := s.db.Model(&models.AWGDevice{}).Where("node_ip = ?", node.IP).Pluck("address", &used).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	taken := make(map[string]bool, len(used))
	for _, addr := range used {
		taken[addr] = true
	}
	addr := pool.Masked().Addr().Next()
	for pool.Contains(addr) && (!awgUsableIP(pool, addr) || taken[addr.String()]) {
		addr = addr.Next()
	}
	if !awgUsableIP(pool, addr) || len(used) >= 512 {
		return jsonError(c, http.StatusConflict, "Закончились адреса для устройств AWG")
	}
	code := models.NewSubToken()
	d := models.AWGDevice{ID: models.NewSubToken(), UserID: u.ID, NodeIP: node.IP, Address: addr.String(),
		Label: label, InviteHash: hashToken(code), InviteUntil: s.now().Add(15 * time.Minute)}
	if err := s.db.Create(&d).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка выдачи профиля")
	}
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	return c.JSON(http.StatusCreated, map[string]any{
		"device": d, "path": "/sub/" + url.PathEscape(u.SubToken) + "/awg/" + code,
	})
}

func (s *Server) revokeAWGDevice(c echo.Context) error {
	res := s.db.Model(&models.AWGDevice{}).Where("id = ? AND user_id = ?", c.Param("device"), c.Param("id")).
		Updates(map[string]any{"revoked": true, "invite_hash": ""})
	if res.Error != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка отзыва профиля")
	}
	if res.RowsAffected == 0 {
		return notFound(c)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) downloadAWG(c echo.Context) error {
	if isPreviewBot(c.Request().UserAgent()) {
		return notFound(c)
	}
	var u models.User
	var d models.AWGDevice
	sum := hashToken(c.Param("code"))
	if s.db.First(&u, "sub_token = ?", c.Param("token")).Error != nil || !u.CanConnect(s.now()) ||
		s.db.First(&d, "user_id = ? AND invite_hash = ? AND invite_until > ? AND revoked = ?", u.ID, sum, s.now(), false).Error != nil {
		return notFound(c)
	}
	h := c.Response().Header()
	h.Set(echo.HeaderCacheControl, "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	if c.Request().Method == http.MethodGet {
		return c.HTML(http.StatusOK, `<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>AmneziaWG 3.1</title><main><h1>AmneziaWG 3.1</h1><form method="post"><button>Скачать .conf / Download .conf</button></form></main></html>`)
	}
	var n models.Node
	var profile protocol.AWGStatus
	if s.db.First(&n, "ip = ?", d.NodeIP).Error != nil || s.now().Sub(n.AWGSeen) >= awgAvailability ||
		json.Unmarshal([]byte(n.AWGProfile), &profile) != nil {
		return jsonError(c, http.StatusServiceUnavailable, "Нода AWG сейчас недоступна")
	}
	pool, ok := awgNetwork(profile.Address)
	addr, err := netip.ParseAddr(d.Address)
	if !ok || err != nil || !awgUsableIP(pool, addr) {
		return jsonError(c, http.StatusConflict, "Подсеть AWG изменилась. Запросите новый профиль")
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Не удалось создать ключ AWG")
	}
	res := s.db.Model(&d).Where("invite_hash = ? AND revoked = ?", sum, false).
		Updates(map[string]any{"invite_hash": "", "public_key": base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())})
	if res.Error != nil {
		return jsonError(c, http.StatusInternalServerError, "Не удалось сохранить ключ AWG")
	}
	if res.RowsAffected == 0 {
		return notFound(c)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "[Interface]\nPrivateKey = %s\nAddress = %s/32\nDNS = 1.1.1.1\n",
		base64.StdEncoding.EncodeToString(key.Bytes()), d.Address)
	options := protocol.AWGClientOptions(profile.Options)
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&body, "%s = %s\n", k, options[k])
	}
	fmt.Fprintf(&body, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 25\n",
		profile.PublicKey, net.JoinHostPort(nodeAddress(n), strconv.Itoa(int(profile.Port))))
	h.Set(echo.HeaderContentDisposition, `attachment; filename="kvn-amneziawg.conf"`)
	return c.String(http.StatusOK, body.String())
}
