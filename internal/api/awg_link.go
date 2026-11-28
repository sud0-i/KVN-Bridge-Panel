package api

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"gorm.io/gorm"
)

const awgLinkSettingKey = "awg_link"

type awgLinkState struct {
	Bridge  string `json:"bridge"`
	Exit    string `json:"exit"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) awgLink() (awgLinkState, error) {
	var stored models.Setting
	var link awgLinkState
	err := s.db.First(&stored, "key = ?", awgLinkSettingKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return link, nil
	}
	if err != nil {
		return link, err
	}
	err = json.Unmarshal([]byte(stored.Value), &link)
	return link, err
}

func (s *Server) saveAWGLink(link awgLinkState) error {
	value, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return s.db.Save(&models.Setting{Key: awgLinkSettingKey, Value: string(value)}).Error
}

func (s *Server) getAWGLink(c echo.Context) error {
	link, err := s.awgLink()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка чтения туннеля AWG")
	}
	return c.JSON(http.StatusOK, link)
}

func (s *Server) setAWGLink(c echo.Context) error {
	var request awgLinkState
	if err := c.Bind(&request); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if _, busy := s.awgJobs.LoadOrStore(awgLinkSettingKey, struct{}{}); busy {
		return jsonError(c, http.StatusConflict, "Туннель AWG уже настраивается")
	}
	defer s.awgJobs.Delete(awgLinkSettingKey)

	link, err := s.awgLink()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка чтения туннеля AWG")
	}
	if !request.Enabled && link.Bridge == "" {
		return c.JSON(http.StatusOK, link)
	}
	if !request.Enabled && link.Enabled {
		link.Enabled = false
		if err := s.saveAWGLink(link); err != nil {
			return jsonError(c, http.StatusInternalServerError, "Ошибка записи туннеля AWG")
		}
	}
	if request.Enabled {
		if link.Bridge != "" && (link.Bridge != request.Bridge || link.Exit != request.Exit) {
			return jsonError(c, http.StatusConflict, "Сначала отключите текущую пару AWG")
		}
		if link.Enabled {
			return c.JSON(http.StatusOK, link)
		}
		link.Bridge, link.Exit = request.Bridge, request.Exit
	}
	var bridge, exit models.Node
	if s.db.First(&bridge, "ip = ? AND type = ?", link.Bridge, protocol.RoleBridge).Error != nil ||
		s.db.First(&exit, "ip = ? AND type = ?", link.Exit, protocol.RoleExit).Error != nil {
		return jsonError(c, http.StatusBadRequest, "Нужны мост и выходная нода")
	}
	for _, node := range []models.Node{bridge, exit} {
		ip, err := netip.ParseAddr(node.IP)
		if err != nil || !ip.Is4() || !hasPanelKey(node) {
			return jsonError(c, http.StatusBadRequest, "Нужны IPv4-адреса серверов и SSH-ключ панели")
		}
	}
	if request.Enabled {
		if !s.isAlive(bridge) || !s.isAlive(exit) ||
			!s.loadRouting().XHTTP || exit.XHTTPPath == "" || bridge.LinkUUID == "" {
			return jsonError(c, http.StatusBadRequest, "Нужны подключённые мост и выходная нода с включённым XHTTP")
		}
	}
	bridgeRoute, err := s.nodeSSHRoute(bridge.IP, bridge.SSHPort, bridge.SSHViaBridge)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	exitRoute, err := s.nodeSSHRoute(exit.IP, exit.SSHPort, exit.SSHViaBridge)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if _, _, err := s.sshKey.load(); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ключ панели: "+err.Error())
	}

	ctx := context.WithoutCancel(c.Request().Context())
	apply := func(node models.Node, route runner.SSHRoute, address, config string) error {
		return s.provisionAWGLink(ctx, node.IP, s.sshKey.path, route, node.Type, address, bridge.IP, config)
	}
	link.Enabled = false
	if err := s.saveAWGLink(link); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка записи туннеля AWG")
	}
	if !request.Enabled {
		bridgeErr := apply(bridge, bridgeRoute, protocol.AWGLinkBridgeIP, "")
		exitErr := apply(exit, exitRoute, protocol.AWGLinkExitIP, "")
		if err := errors.Join(bridgeErr, exitErr); err != nil {
			return jsonError(c, http.StatusBadGateway, "Маршрут отключён. Повторите удаление туннеля: "+err.Error())
		}
		link = awgLinkState{}
	} else {
		bridgeConfig, exitConfig, err := awgLinkConfigs(exit.IP)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "Не удалось создать ключи туннеля")
		}
		if err := apply(exit, exitRoute, protocol.AWGLinkExitIP, exitConfig); err != nil {
			return jsonError(c, http.StatusBadGateway, "Туннель на ноде: "+err.Error())
		}
		if err := apply(bridge, bridgeRoute, protocol.AWGLinkBridgeIP, bridgeConfig); err != nil {
			return jsonError(c, http.StatusBadGateway, "Туннель на мосту: "+err.Error())
		}
		link.Enabled = true
	}
	if err := s.saveAWGLink(link); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка записи туннеля AWG")
	}
	return c.JSON(http.StatusOK, link)
}

func awgLinkConfigs(exitIP string) (string, string, error) {
	keyAgreement := ecdh.X25519()
	bridge, err := keyAgreement.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	exit, err := keyAgreement.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	header := make([]byte, 32)
	if _, err := rand.Read(header); err != nil {
		return "", "", err
	}
	encode := base64.StdEncoding.EncodeToString
	format := func(local, remote *ecdh.PrivateKey, address string, port int) string {
		return fmt.Sprintf(
			"[Interface]\nPrivateKey = %s\nListenPort = %d\nHeaderProtectionKey = %s\nS1 = 16\nS2 = 16\nS3 = 16\nS4 = 16\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\nRandomTrailers = on\n\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n",
			encode(local.Bytes()), port, encode(header), encode(remote.PublicKey().Bytes()), address)
	}
	bridgeConf := format(bridge, exit, protocol.AWGLinkExitIP, 0)
	bridgeConf += "Endpoint = " + net.JoinHostPort(exitIP, "51824") + "\nPersistentKeepalive = 25\n"
	return bridgeConf, format(exit, bridge, protocol.AWGLinkBridgeIP, 51824), nil
}
