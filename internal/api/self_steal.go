package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
)

func validSelfStealDomain(domain string) bool {
	return len(domain) <= 253 && hostnameRe.MatchString(domain)
}

// enableSelfSteal keeps the existing SNI and keys until the new site has working TLS.
func (s *Server) enableSelfSteal(c echo.Context) error {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if !validSelfStealDomain(domain) {
		return jsonError(c, http.StatusBadRequest, "Укажите корректный домен своего сайта")
	}

	ip := c.Param("ip")
	if _, busy := s.selfStealJobs.LoadOrStore(ip, struct{}{}); busy {
		return jsonError(c, http.StatusConflict, "Сайт для этой ноды уже настраивается")
	}
	defer s.selfStealJobs.Delete(ip)

	var node models.Node
	if err := s.db.First(&node, "ip = ?", ip).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	if node.IsLocalBridge() || !s.isAlive(node) || !hasPanelKey(node) {
		return jsonError(c, http.StatusBadRequest, "Нужна подключённая удалённая нода с SSH-ключом панели")
	}
	if node.SelfStealDomain != "" && node.SelfStealDomain != domain {
		return jsonError(c, http.StatusConflict, "На этой ноде уже настроен собственный сайт")
	}
	if _, _, err := s.sshKey.load(); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ключ панели: "+err.Error())
	}

	route, err := s.nodeSSHRoute(node.IP, node.SSHPort, node.SSHViaBridge)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}

	// Завершаем операцию даже если администратор закрыл окно во время выпуска сертификата.
	refreshing := node.SelfStealDomain == domain
	node.SelfStealDomain = domain
	site := s.publicSiteSettings(node)
	if err := s.provisionSelfSteal(context.WithoutCancel(c.Request().Context()), ip, domain, node.CDNDomain, site, s.sshKey.path, route); err != nil {
		return jsonError(c, http.StatusBadGateway, "Не удалось настроить сайт: "+err.Error())
	}
	if refreshing {
		return c.NoContent(http.StatusNoContent)
	}
	res := s.db.Model(&node).Where("token_hash = ? AND sni = ?", node.TokenHash, node.SNI).
		Updates(map[string]any{
			"self_steal_domain": domain, "sni": domain, "reality_dest": selfStealTarget, "reality_xver": 0, "sni_error": "",
		})
	if res.Error != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	if res.RowsAffected == 0 {
		return jsonError(c, http.StatusConflict, "Настройки ноды изменились. Повторите операцию")
	}
	return c.NoContent(http.StatusNoContent)
}
