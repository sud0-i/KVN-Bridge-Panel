package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
)

func TestInstallWARPOnExistingLocalBridge(t *testing.T) {
	ev := newEnv(t)
	const ip = "203.0.113.25"
	token := ev.addNode(ip, "bridge")
	before := ev.syncAs(token)
	if err := ev.db.Model(&models.Node{}).Where("ip = ?", ip).Updates(map[string]any{
		"local_bridge": true, "ssh_state": "keys", "ssh_port": 2222, "warp_transit": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes/" + ip + "/install-warp"
	if rec := ev.do("POST", path, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", rec.Code, rec.Body)
	}
	auth := ev.adminToken()
	calls := 0
	ev.s.provisionWARP = func(_ context.Context, target, key string, route runner.SSHRoute) error {
		calls++
		if target != ip || key != ev.s.sshKey.path || route.Port != 2222 {
			t.Fatalf("wrong bridge SSH route: %s %+v", target, route)
		}
		if calls == 1 {
			return errors.New("registration failed")
		}
		return nil
	}
	if rec := ev.do("POST", path, "", auth); rec.Code != http.StatusBadGateway ||
		!strings.Contains(rec.Body.String(), "registration failed") {
		t.Fatalf("missing installer error: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("POST", path, "", auth); rec.Code != http.StatusNoContent {
		t.Fatalf("install WARP: %d %s", rec.Code, rec.Body)
	}
	after := ev.syncAs(token)
	if after.Reality != before.Reality || !after.WarpTransit || len(ev.deployed) != 1 {
		t.Fatalf("WARP install unexpectedly changed the bridge: %+v", after)
	}
}
