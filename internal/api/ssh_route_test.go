package api

import (
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"net/http"
	"testing"
)

func TestDeployUsesSavedSSHRouteAndBridgeWarpOption(t *testing.T) {
	ev := newEnv(t)
	const bridgeIP = "203.0.113.21"
	const exitIP = "203.0.113.22"
	token := ev.addNode(bridgeIP, "bridge")
	auth := ev.adminToken()
	if rec := ev.do("PATCH", "/api/nodes/"+bridgeIP, `{"warp_transit":true}`, auth); rec.Code != http.StatusNoContent {
		t.Fatalf("enable bridge WARP: %d %s", rec.Code, rec.Body)
	}
	if !ev.syncAs(token).WarpTransit {
		t.Fatal("bridge didn't receive the transit flag")
	}
	if err := ev.db.Model(&models.Node{}).Where("ip = ?", bridgeIP).Update("ssh_state", "keys").Error; err != nil {
		t.Fatal(err)
	}
	body := `{"ip":"` + exitIP + `","type":"exit","password":"secret","ssh_port":2222,"ssh_via_bridge":"` + bridgeIP + `"}`
	if rec := ev.do("POST", "/api/nodes", body, auth); rec.Code != http.StatusAccepted {
		t.Fatalf("create with SSH route: %d %s", rec.Code, rec.Body)
	}
	if p := ev.deployed[1]; p.SSH.Port != 2222 || p.SSH.OpenBridge == nil || p.WarpTransit {
		t.Fatalf("incorrect SSH route for exit: %+v", p.SSH)
	}
	if rec := ev.do("POST", "/api/nodes/"+exitIP+"/redeploy", `{"password":"secret"}`, auth); rec.Code != http.StatusAccepted {
		t.Fatalf("redeploy with stored route: %d %s", rec.Code, rec.Body)
	}
	if p := ev.deployed[2]; p.SSH.Port != 2222 || p.SSH.OpenBridge == nil {
		t.Fatalf("saved route lost on redeploy: %+v", p.SSH)
	}
}
