package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
)

func TestAWGLinkPublishesOnlyAfterProvisioning(t *testing.T) {
	ev := newEnv(t)
	const bridge = "203.0.113.11"
	const exit = "203.0.113.12"
	bridgeToken := ev.addNode(bridge, protocol.RoleBridge)
	ev.syncAs(ev.addNode(exit, protocol.RoleExit))
	ev.syncAs(bridgeToken)
	if err := ev.db.Model(&models.Node{}).Where("ip IN ?", []string{bridge, exit}).Update("ssh_state", "keys").Error; err != nil {
		t.Fatal(err)
	}
	var calls []string
	failBridge := true
	ev.s.provisionAWGLink = func(_ context.Context, ip, _ string, _ runner.SSHRoute, role, address, bridgeIP, config string) error {
		link, err := ev.s.awgLink()
		if err != nil || link.Enabled {
			t.Fatalf("route was published before provisioning or cleanup: %v %v", link, err)
		}
		calls = append(calls, ip)
		if config != "" {
			if bridgeIP != bridge || !strings.Contains(config, "AllowedIPs = ") {
				t.Fatal("invalid AWG tunnel config")
			}
			if role == protocol.RoleBridge && (address != protocol.AWGLinkBridgeIP || !strings.Contains(config, "Endpoint = "+exit+":51824")) {
				t.Fatal("invalid AWG bridge endpoint")
			}
			if ip == bridge && failBridge {
				return errors.New("test failure")
			}
		}
		return nil
	}
	admin := ev.adminToken()
	const path = "/api/awg-link"
	const enable = `{"enabled":true,"bridge":"203.0.113.11","exit":"203.0.113.12"}`
	if rec := ev.do("PUT", path, enable, admin); rec.Code != http.StatusBadGateway {
		t.Fatalf("failed install: %d %s", rec.Code, rec.Body)
	}
	if exits := ev.syncAs(bridgeToken).Exits; len(exits) != 1 || exits[0].AWGXHTTPPath != "" {
		t.Fatalf("published after failed install: %v", exits)
	}
	failBridge = false
	if rec := ev.do("PUT", path, enable, admin); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "PrivateKey") {
		t.Fatalf("successful install: %d %s", rec.Code, rec.Body)
	}
	if exits := ev.syncAs(bridgeToken).Exits; len(exits) != 1 || exits[0].AWGXHTTPPath == "" {
		t.Fatalf("did not publish AWG path: %v", exits)
	}
	if rec := ev.do("PUT", path, `{"enabled":false}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if !slices.Equal(calls, []string{exit, bridge, exit, bridge, bridge, exit}) {
		t.Fatalf("unexpected provisioning order: %v", calls)
	}
}
