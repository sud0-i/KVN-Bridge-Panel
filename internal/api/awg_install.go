package api

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func (s *Server) installAWG(c echo.Context) error {
	ip := c.Param("ip")
	var node models.Node
	if err := s.db.First(&node, "ip = ?", ip).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	if node.Type != protocol.RoleExit || !s.isAlive(node) || !hasPanelKey(node) {
		return jsonError(c, http.StatusBadRequest, "Нужна подключённая выходная нода с SSH-ключом панели")
	}
	if s.now().Sub(node.AWGSeen) < awgAvailability {
		return jsonError(c, http.StatusConflict, "Агент уже управляет интерфейсом AWG на этой ноде")
	}
	if _, busy := s.awgJobs.LoadOrStore(ip, struct{}{}); busy {
		return jsonError(c, http.StatusConflict, "AmneziaWG уже устанавливается на этой ноде")
	}
	defer s.awgJobs.Delete(ip)
	route, err := s.nodeSSHRoute(node.IP, node.SSHPort, node.SSHViaBridge)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if _, _, err := s.sshKey.load(); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ключ панели: "+err.Error())
	}
	if err := s.provisionAWG(context.WithoutCancel(c.Request().Context()), ip, s.sshKey.path, route); err != nil {
		return jsonError(c, http.StatusBadGateway, "Установка AWG: "+err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}
