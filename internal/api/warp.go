package api

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// installBridgeWARP adds the SOCKS transport to an existing bridge without
// replacing its agent, credentials, or VPN configuration.
func (s *Server) installBridgeWARP(c echo.Context) error {
	ip := c.Param("ip")
	var node models.Node
	if err := s.db.First(&node, "ip = ?", ip).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	if node.Type != protocol.RoleBridge || !s.isAlive(node) || !hasPanelKey(node) {
		return jsonError(c, http.StatusBadRequest, "Нужен подключённый мост с SSH-ключом панели")
	}

	if _, busy := s.warpJobs.LoadOrStore(ip, struct{}{}); busy {
		return jsonError(c, http.StatusConflict, "WARP уже устанавливается на этом мосту")
	}
	defer s.warpJobs.Delete(ip)

	route, err := s.nodeSSHRoute(node.IP, node.SSHPort, node.SSHViaBridge)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if _, _, err := s.sshKey.load(); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ключ панели: "+err.Error())
	}
	if err := s.provisionWARP(context.WithoutCancel(c.Request().Context()), ip, s.sshKey.path, route); err != nil {
		return jsonError(c, http.StatusBadGateway, "Не удалось запустить WARP: "+err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}
