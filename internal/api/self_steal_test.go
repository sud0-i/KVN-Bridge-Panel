package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
)

func TestEnableSelfStealPreservesKeysAndWaitsForTLS(t *testing.T) {
	ev := newEnv(t)
	const ip = "203.0.113.11"
	const domain = "site.example.com"
	token := ev.addNode(ip, "bridge")
	before := ev.syncAs(token)
	if err := ev.db.Model(&models.Node{}).Where("ip = ?", ip).Update("ssh_state", "keys").Error; err != nil {
		t.Fatal(err)
	}
	auth := ev.adminToken()
	path := "/api/nodes/" + ip + "/self-steal"
	body := `{"domain":"Site.Example.com"}`
	ev.s.provisionSelfSteal = func(_ context.Context, target, name, cdn, key string, _ runner.SSHRoute) error {
		if target != ip || name != domain || key != ev.s.sshKey.path {
			t.Fatalf("provisioning got %q %q %q", target, name, key)
		}
		return errors.New("certificate pending")
	}
	if rec := ev.do("POST", path, body, auth); rec.Code != http.StatusBadGateway {
		t.Fatalf("failed certificate: %d %s", rec.Code, rec.Body)
	}
	if got := ev.syncAs(token).Reality; got != before.Reality {
		t.Fatalf("failed provisioning changed REALITY: %+v", got)
	}

	ev.s.provisionSelfSteal = func(context.Context, string, string, string, string, runner.SSHRoute) error { return nil }
	if rec := ev.do("POST", path, body, auth); rec.Code != http.StatusNoContent {
		t.Fatalf("enable self-steal: %d %s", rec.Code, rec.Body)
	}
	after := ev.syncAs(token)
	if after.Reality.SNI != domain || after.Reality.Dest != selfStealTarget ||
		after.Reality.ShortID != before.Reality.ShortID || after.PanelChannel != "" {
		t.Fatalf("unexpected REALITY config or node role: %+v, channel=%q", after.Reality, after.PanelChannel)
	}
	const cdn = "cdn.example.com"
	if err := ev.db.Model(&models.Node{}).Where("ip = ?", ip).Update("cdn_domain", cdn).Error; err != nil {
		t.Fatal(err)
	}
	called := false
	ev.s.provisionSelfSteal = func(_ context.Context, target, name, alias, key string, _ runner.SSHRoute) error {
		called = true
		if target != ip || name != domain || alias != cdn {
			t.Fatalf("refresh got %q %q %q", target, name, alias)
		}
		return nil
	}
	if rec := ev.do("POST", path, body, auth); rec.Code != http.StatusNoContent || !called {
		t.Fatalf("refresh self-steal: %d %s", rec.Code, rec.Body)
	}
	if got := ev.syncAs(token).Reality; got != after.Reality {
		t.Fatalf("refresh changed REALITY: %+v", got)
	}
}

func TestCreateAndRedeploySelfStealNode(t *testing.T) {
	ev := newEnv(t)
	auth := ev.adminToken()
	const ip = "203.0.113.12"
	const domain = "site.example.com"
	if rec := ev.do("POST", "/api/nodes", `{"ip":"`+ip+`","type":"exit","password":"rootpw","self_steal_domain":"`+domain+`"}`, auth); rec.Code != http.StatusAccepted {
		t.Fatalf("create self-steal node: %d %s", rec.Code, rec.Body)
	}
	p := ev.deployed[0]
	if p.SelfStealDomain != domain {
		t.Fatalf("missing site in deploy params: %q", p.SelfStealDomain)
	}
	if got := ev.syncAs(p.NodeToken).Reality; got.SNI != domain || got.Dest != selfStealTarget || got.Xver != 0 {
		t.Fatalf("unexpected REALITY config: %+v", got)
	}
	if rec := ev.do("POST", "/api/nodes/"+ip+"/redeploy", `{"password":"rootpw"}`, auth); rec.Code != http.StatusAccepted {
		t.Fatalf("redeploy self-steal node: %d %s", rec.Code, rec.Body)
	}
	if ev.deployed[1].SelfStealDomain != domain {
		t.Fatal("redeploy lost the site domain")
	}
}
