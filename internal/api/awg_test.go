package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func TestAWGProfileAndSharedQuota(t *testing.T) {
	ev := newEnv(t)
	const nodeIP = "203.0.113.21"
	token := ev.addNode(nodeIP, protocol.RoleExit)
	user := models.User{ID: "awg-user", Name: "AWG", TrafficQuota: 100, TrafficUp: 20}
	if err := ev.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	serverKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	status := protocol.AWGStatus{PublicKey: serverKey, Address: "10.90.0.1/27", Port: 51820, Epoch: "initial",
		Options: map[string]string{"HeaderProtectionKey": serverKey, "RandomTrailers": "on", "PostUp": "ignored"}}
	report := func() []protocol.AWGPeer {
		t.Helper()
		body, _ := json.Marshal(status)
		rec := ev.do("POST", "/api/awg", string(body), map[string]string{protocol.NodeTokenHeader: token})
		var peers []protocol.AWGPeer
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &peers) != nil {
			t.Fatalf("sync: %d %s", rec.Code, rec.Body)
		}
		return peers
	}
	report()
	rec := ev.do("POST", "/api/users/"+user.ID+"/awg", `{"node":"203.0.113.21","label":"Router"}`, ev.adminToken())
	var invite struct{ Path string }
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &invite) != nil || invite.Path == "" {
		t.Fatalf("issue: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("GET", invite.Path, "", nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "PrivateKey") {
		t.Fatal("viewing the link should not issue a key")
	}
	rec = ev.do("POST", invite.Path, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "PrivateKey = ") ||
		!strings.Contains(rec.Body.String(), "RandomTrailers = on") || strings.Contains(rec.Body.String(), "PostUp") {
		t.Fatalf("download status = %d", rec.Code)
	}
	if rec := ev.do("POST", invite.Path, "", nil); rec.Code != http.StatusNotFound {
		t.Fatal("invite was reused")
	}
	peers := report()
	if len(peers) != 1 || peers[0].IP != "10.90.0.2" || !protocol.AWGKeyValid(peers[0].PublicKey) {
		t.Fatalf("unexpected peers: %+v", peers)
	}
	status.Peers = []protocol.AWGCounter{{PublicKey: peers[0].PublicKey, Rx: 25, Tx: 35, Handshake: 100}}
	if len(report()) != 1 || len(report()) != 1 {
		t.Fatal("a duplicate snapshot exhausted the quota")
	}
	status.Epoch = "node-restarted"
	status.Peers[0].Rx, status.Peers[0].Tx = 5, 15
	if len(report()) != 0 {
		t.Fatal("AWG must reject a user who exhausted the shared quota")
	}
	if err := ev.db.First(&user, "id = ?", user.ID).Error; err != nil || user.TrafficUp != 50 || user.TrafficDown != 50 {
		t.Fatalf("shared traffic: %d/%d (%v)", user.TrafficUp, user.TrafficDown, err)
	}
	var device models.AWGDevice
	if err := ev.db.First(&device, "user_id = ?", user.ID).Error; err != nil ||
		device.TrafficUp != 30 || device.TrafficDown != 50 || device.Handshake != 100 {
		t.Fatalf("device traffic: %+v (%v)", device, err)
	}
}
