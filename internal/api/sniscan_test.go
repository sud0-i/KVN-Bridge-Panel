package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func TestSNIScan(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	exit := ev.addNode("10.0.0.9", protocol.RoleExit)
	if ip := ev.syncAs(exit).NodeIP; ip != "10.0.0.9" {
		t.Fatalf("адрес ноды для подбора: %q", ip)
	}
	if rec := ev.do("POST", "/api/nodes/10.0.0.9/action", `{"action":"scan-sni"}`, adm); rec.Code != http.StatusAccepted {
		t.Fatalf("подбор: %d %s", rec.Code, rec.Body)
	}
	if a := ev.syncAs(exit).Action; a != protocol.ActionScanSNI {
		t.Fatalf("выдано: %q", a)
	}

	scan := protocol.SNIScan{At: ev.now, Subnet: "10.0.0.0/24", Scanned: 7, Found: []protocol.SNICandidate{{SNI: "shop.example", IP: "10.0.0.20", MS: 3}}}
	raw, _ := json.Marshal(scan)
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: exit, protocol.SNIScanHeader: url.QueryEscape(string(raw))})
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: exit, protocol.SNIScanHeader: url.QueryEscape("{мусор")})
	var nd models.Node
	ev.db.First(&nd, "ip = ?", "10.0.0.9")
	if nd.SNIScan != string(raw) {
		t.Fatalf("итог подбора: %q", nd.SNIScan)
	}
	if !strings.Contains(ev.do("GET", "/api/nodes", "", adm).Body.String(), "shop.example") {
		t.Fatal("итог виден в панели")
	}

	// Мосту на сервере мастера подбор не нужен
	ev.db.Model(&nd).Update("reality_dest", "127.0.0.1:8443")
	if rec := ev.do("POST", "/api/nodes/10.0.0.9/action", `{"action":"scan-sni"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatalf("мост на мастере: %d", rec.Code)
	}
}
