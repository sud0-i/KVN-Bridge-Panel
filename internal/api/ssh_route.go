package api

import (
	"fmt"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"golang.org/x/crypto/ssh"
)

func (s *Server) nodeSSHRoute(ip string, port int, via string) (runner.SSHRoute, error) {
	route := runner.SSHRoute{Port: port}
	if port < 0 || port > 65535 {
		return route, fmt.Errorf("SSH-порт должен быть от 1 до 65535")
	}
	if via == "" {
		return route, nil
	}
	if ip == via {
		return route, fmt.Errorf("Нода не может использовать саму себя как SSH-мост")
	}
	var bridge models.Node
	if err := s.db.First(&bridge, "ip = ? AND type = ?", via, protocol.RoleBridge).Error; err != nil {
		return route, fmt.Errorf("SSH-мост не найден")
	}
	if bridge.SSHViaBridge != "" || !hasPanelKey(bridge) {
		return route, fmt.Errorf("Для WARP нужен мост с ключом панели и прямым доступом по SSH")
	}
	route.OpenBridge = func() (*ssh.Client, error) {
		client, err := s.connectSSH(bridge, runner.SSHRoute{Port: bridge.SSHPort})
		if err != nil {
			return nil, fmt.Errorf("SSH-мост %s: %w", bridge.IP, err)
		}
		return client, nil
	}
	return route, nil
}
