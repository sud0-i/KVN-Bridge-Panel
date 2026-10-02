package api

import (
	"net/http"
	"net/url"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"gorm.io/gorm"
)

// sync — агент запрашивает всё, что нужно для конфига его ноды
func (s *Server) sync(c echo.Context) error {
	node := c.Get(ctxNode).(*models.Node)
	now := s.now()

	// Сам факт синхронизации — это heartbeat ноды; заодно запоминаем её состояние
	header := func(name string) string {
		v, _ := url.QueryUnescape(c.Request().Header.Get(name))
		if r := []rune(v); len(r) > 1000 {
			v = string(r[:1000])
		}
		return v
	}
	updates := map[string]any{
		"is_online":    true,
		"last_seen":    now,
		"warp_ok":      c.Request().Header.Get(protocol.WarpHeader) == "1",
		"config_error": header(protocol.ConfigErrorHeader),
		"sni_error":    header(protocol.SNIErrorHeader),
		"warp_error":   header(protocol.WarpErrorHeader),
	}
	// Версии и обновления: агенты без самообновления этих заголовков не шлют — не затираем
	for col, h := range map[string]string{
		"agent_version": protocol.AgentVersionHeader,
		"xray_version":  protocol.XrayVersionHeader,
		"geo_updated":   protocol.GeoUpdatedHeader,
	} {
		if v := header(h); v != "" {
			updates[col] = v
		}
	}
	if c.Request().Header.Get(protocol.AgentVersionHeader) != "" {
		updates["update_error"] = header(protocol.UpdateErrorHeader)
		// Агенты с mieru всегда шлют его состояние: пусто — mita не работает
		updates["mieru_version"] = header(protocol.MieruHeader)
		updates["mieru_error"] = header(protocol.MieruErrorHeader)
	}
	if cert := parseHy2Cert(c.Request().Header.Get(protocol.HysteriaCertHeader)); cert != "" {
		updates["hy2_cert"] = cert
	}
	s.db.Model(&models.Node{}).Where("ip = ?", node.IP).Updates(updates)
	if raw, err := url.QueryUnescape(c.Request().Header.Get(protocol.MetricsHeader)); err == nil && raw != "" {
		s.storeMetrics(node, raw, now)
	}
	if st := header(protocol.SSHHeader); st != "" {
		s.db.Model(&models.Node{}).Where("ip = ?", node.IP).Update("ssh_state", st)
	}
	if res := header(protocol.ActionHeader); res != "" {
		s.db.Model(&models.Node{}).Where("ip = ?", node.IP).Update("action_result", res)
	}
	if raw, err := url.QueryUnescape(c.Request().Header.Get(protocol.LinksHeader)); err == nil && raw != "" && node.Type == protocol.RoleBridge {
		s.storeLinkSamples(node.IP, raw, now)
	}
	if acks, err := url.QueryUnescape(c.Request().Header.Get(protocol.NotifyAckHeader)); err == nil {
		s.handleNotifyAcks(node, acks)
	}
	routing := s.loadRouting()

	resp := protocol.SyncResponse{
		Role:    node.Type,
		Reality: protocol.Reality{SNI: node.SNI, ShortID: node.SID, Dest: node.RealityDest, Xver: node.RealityXver},
		Clients: []protocol.Client{},
	}
	if routing.XHTTP {
		resp.XHTTPPath = node.XHTTPPath
	}
	resp.Fingerprint = routing.fingerprint()
	resp.Hysteria = routing.hysteria()
	// Mieru — на мостах и, если в подписке есть прямые ссылки, на выходных нодах
	if routing.Mieru && (node.Type == protocol.RoleBridge || routing.DirectExitLinks) {
		users, err := s.userClients()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "DB error")
		}
		m := &protocol.Mieru{Version: MieruVersion, Ports: routing.MieruPorts, Users: []protocol.MieruUser{}}
		for _, u := range users {
			// Имя — как email в Xray (по нему считается трафик), пароль — ключ пользователя
			m.Users = append(m.Users, protocol.MieruUser{Name: u.Email, Password: u.ID})
		}
		resp.Mieru = m
	}
	if routing.CDN && node.CDNDomain != "" {
		resp.CDN = &protocol.CDN{Domain: node.CDNDomain, Path: node.XHTTPPath}
	}
	resp.Maintenance = s.maintenance(routing)
	resp.Action = s.takeAction(node)
	resp.SSHKeys = s.authorizedKeys()
	resp.SSHKeysOnly = node.SSHKeysOnly

	switch node.Type {
	case protocol.RoleBridge:
		// К мосту подключаются пользователи
		users, err := s.userClients()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "DB error")
		}
		resp.Clients = users

		exits, err := s.cascadeExits()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "DB error")
		}
		resp.Direct = routing.bridgeDirect()
		// Одиночный режим: выходных нод нет, мост сам выпускает трафик в интернет —
		// и правила «через WARP» выполняет тоже он
		if len(exits) == 0 {
			resp.Warp = routing.exitWarp()
		}
		for _, e := range exits {
			exit := protocol.Exit{
				Address:   e.IP,
				Port:      443,
				UUID:      node.LinkUUID,
				PublicKey: e.PubKey,
				SNI:       e.SNI,
				ShortID:   e.SID,
			}
			switch {
			case routing.ExitLink == ExitLinkCDN && routing.CDN && e.CDNDomain != "":
				exit.CDN, exit.XHTTPPath = e.CDNDomain, e.XHTTPPath
			// XHTTP-вход на экзите есть, только если XHTTP включён
			case (routing.ExitLink == ExitLinkXHTTP || routing.ExitLink == ExitLinkCDN) && routing.XHTTP:
				exit.XHTTPPath = e.XHTTPPath
			}
			resp.Exits = append(resp.Exits, exit)
		}

	case protocol.RoleExit:
		resp.Warp = routing.exitWarp()
		// Выходная нода видит Telegram — она и пересылает уведомления
		resp.Notify = s.notifyForExit(node)

		// К выходной ноде подключаются мосты (даже ещё не вышедшие на связь)
		var bridges []models.Node
		if err := s.db.Where("type = ?", protocol.RoleBridge).Find(&bridges).Error; err != nil {
			return jsonError(c, http.StatusInternalServerError, "DB error")
		}
		for _, b := range bridges {
			if b.LinkUUID == "" {
				continue
			}
			resp.Clients = append(resp.Clients, protocol.Client{ID: b.LinkUUID, Email: "bridge-" + b.IP})
		}
		// …и сами пользователи, если в подписке выдаются прямые ссылки
		if routing.DirectExitLinks {
			users, err := s.userClients()
			if err != nil {
				return jsonError(c, http.StatusInternalServerError, "DB error")
			}
			resp.Clients = append(resp.Clients, users...)
		}
	}

	return c.JSON(http.StatusOK, resp)
}

// userClients — пользователи, которым сейчас разрешено подключаться
func (s *Server) userClients() ([]protocol.Client, error) {
	var users []models.User
	if err := s.db.Where("status = ?", "active").Find(&users).Error; err != nil {
		return nil, err
	}
	now := s.now()
	out := []protocol.Client{}
	for _, u := range users {
		if u.CanConnect(now) {
			out = append(out, protocol.Client{ID: u.ID, Email: u.ID})
		}
	}
	return out, nil
}

// stats — агент присылает прирост трафика пользователей
func (s *Server) stats(c echo.Context) error {
	var req []protocol.UserTraffic
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "bad format")
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, st := range req {
			if st.Up < 0 || st.Down < 0 || (st.Up == 0 && st.Down == 0) {
				continue
			}
			// Атомарный инкремент: прибавляем новые байты к старым
			err := tx.Model(&models.User{}).Where("id = ?", st.Email).Updates(map[string]any{
				"traffic_up":   gorm.Expr("traffic_up + ?", st.Up),
				"traffic_down": gorm.Expr("traffic_down + ?", st.Down),
			}).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "DB error")
	}
	return c.String(http.StatusOK, "OK")
}
