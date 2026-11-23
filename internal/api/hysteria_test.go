package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func TestHy2TrustedTLSWaitsForAgent(t *testing.T) {
	ev := newEnv(t)
	const ip = "10.0.0.1"
	const name = "site.example.com"
	token := ev.addNode(ip, protocol.RoleBridge)
	admin := ev.adminToken()
	path := "/api/nodes/" + ip
	if rec := ev.do("PATCH", path, `{"hy2_public_tls":true}`, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("enable without own domain: %d", rec.Code)
	}
	if err := ev.db.Model(&models.Node{}).Where("ip = ?", ip).Update("self_steal_domain", name).Error; err != nil {
		t.Fatal(err)
	}
	if rec := ev.do("PATCH", path, `{"hy2_public_tls":true}`, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	ev.putSettings(`{"hysteria":true}`)
	cert := testHy2Cert(t, name)
	if got := ev.syncWithCert(token, cert).Hysteria; got == nil || got.PublicTLSName != name || got.CaddyInDocker {
		t.Fatalf("desired settings: %+v", got)
	}
	getNode := func() models.Node {
		var n models.Node
		if err := ev.db.First(&n, "ip = ?", ip).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := getNode(); !n.Hy2PublicTLS || n.Hy2TrustedName != "" || !strings.Contains(hy2Link("client", subServer{Node: n}), "pinSHA256=") {
		t.Fatalf("subscription switched before agent confirmation: %+v", n)
	}
	report := ev.do("GET", "/api/sync", "", map[string]string{
		protocol.NodeTokenHeader: token, protocol.HysteriaTLSHeader: name,
		protocol.HysteriaCertHeader: base64.StdEncoding.EncodeToString([]byte(cert)),
	})
	if report.Code != http.StatusOK {
		t.Fatalf("report: %d %s", report.Code, report.Body)
	}
	n := getNode()
	link, err := url.Parse(hy2Link("client", subServer{Node: n}))
	if err != nil || link.Hostname() != name || link.Query().Get("sni") != name ||
		link.Query().Has("insecure") || link.Query().Has("pinSHA256") {
		t.Fatalf("trusted profile: %v %v", link, err)
	}
	raw, err := singboxConfig("client", "Example", []subServer{{Node: n, Hy2: true}}, true)
	if err != nil || strings.Contains(string(raw), `"certificate"`) || !strings.Contains(string(raw), name) {
		t.Fatalf("trusted sing-box profile: %s %v", raw, err)
	}
	if rec := ev.do("PATCH", path, `{"hy2_public_tls":false}`, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if getNode().Hy2TrustedName != name {
		t.Fatal("public TLS should remain published until the agent switches back")
	}
	ev.syncWithCert(token, cert)
	if getNode().Hy2TrustedName != "" {
		t.Fatal("agent rollback to pin was not recorded")
	}
}

func testHy2Cert(t *testing.T, name string) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func (ev *env) syncWithCert(token, certPEM string) protocol.SyncResponse {
	ev.t.Helper()
	rec := ev.do("GET", "/api/sync", "", map[string]string{
		protocol.NodeTokenHeader:    token,
		protocol.HysteriaCertHeader: base64.StdEncoding.EncodeToString([]byte(certPEM)),
	})
	var resp protocol.SyncResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp
}

func TestHysteria(t *testing.T) {
	ev := newEnv(t)
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.db.Create(&models.User{ID: "u1", Name: "alice", SubToken: "tok12345", Status: "active"})
	cert := testHy2Cert(t, "www.example.com")

	// По умолчанию выключено: ни настроек на ноде, ни серверов в подписке
	if resp := ev.syncWithCert(bridge, cert); resp.Hysteria != nil {
		t.Fatal("Hysteria2 по умолчанию выключен")
	}
	raw, _ := base64.StdEncoding.DecodeString(ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "v2rayNG"}).Body.String())
	if strings.Contains(string(raw), "hysteria2://") {
		t.Fatal("выключенный Hysteria2 не должен попадать в подписку")
	}

	ev.putSettings(`{"region_route":"warp","hysteria":true,"hysteria_obfs":true}`)
	resp := ev.syncWithCert(bridge, cert)
	if resp.Hysteria == nil || len(resp.Hysteria.Obfs) < 16 {
		t.Fatalf("нода должна получить Hysteria2 с паролем salamander: %+v", resp.Hysteria)
	}
	// Пароль salamander стабилен между сохранениями и не уходит в панель
	obfs := resp.Hysteria.Obfs
	ev.putSettings(`{"region_route":"warp","hysteria":true,"hysteria_obfs":true,"xhttp":true}`)
	if again := ev.syncWithCert(bridge, cert); again.Hysteria.Obfs != obfs {
		t.Fatal("пароль salamander не должен меняться при каждом сохранении — клиенты отвалятся")
	}
	if strings.Contains(ev.do("GET", "/api/settings", "", ev.adminToken()).Body.String(), obfs) {
		t.Fatal("пароль salamander не нужен в панели")
	}

	// Ссылка hysteria2:// с пином нашего сертификата
	raw, _ = base64.StdEncoding.DecodeString(ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "v2rayNG"}).Body.String())
	link := ""
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "hysteria2://") {
			link = l
		}
	}
	for _, want := range []string{"hysteria2://u1@", ":443/", "sni=www.example.com", "pinSHA256=" + hy2Pin(cert), "obfs=salamander", "obfs-password=" + obfs, "insecure=1"} {
		if !strings.Contains(link, want) {
			t.Errorf("в ссылке нет %q: %s", want, link)
		}
	}

	// JSON для Karing: сертификат целиком (sing-box проверяет им сервер) и salamander
	var hy map[string]any
	for _, o := range ev.singbox("Karing/1.2").Outbounds {
		if o["type"] == "hysteria2" {
			hy = o
		}
	}
	tls, _ := hy["tls"].(map[string]any)
	if hy == nil || hy["password"] != "u1" || tls["server_name"] != "www.example.com" || len(tls["certificate"].([]any)) < 3 ||
		hy["obfs"].(map[string]any)["password"] != obfs {
		t.Fatalf("неверный outbound hysteria2: %v", hy)
	}

	// Мусор вместо сертификата не сохраняется
	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"))} {
		if parseHy2Cert(bad) != "" {
			t.Errorf("принят негодный сертификат: %q", bad)
		}
	}
}
