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

type sbConfig struct {
	Outbounds []map[string]any `json:"outbounds"`
	Route     map[string]any   `json:"route"`
}

func (ev *env) singbox(ua string) sbConfig {
	ev.t.Helper()
	rec := ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": ua})
	var cfg sbConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil || rec.Code != http.StatusOK {
		ev.t.Fatalf("%s: ожидали JSON sing-box, получили %d %.100s", ua, rec.Code, rec.Body)
	}
	return cfg
}

func TestSingboxSubscription(t *testing.T) {
	ev := newEnv(t)
	ev.syncAs(ev.addNode("10.0.0.1", protocol.RoleBridge))
	ev.syncAs(ev.addNode("10.0.0.2", protocol.RoleExit))
	ev.db.Model(&models.Node{}).Where("ip = ?", "10.0.0.2").Update("label", "🇳🇱 NL")
	ev.db.Create(&models.User{ID: "u1", Name: "alice", SubToken: "tok12345", Status: "active"})

	cfg := ev.singbox("Karing/1.2.19")
	tags := map[string]bool{}
	var vless, xhttp int
	for _, o := range cfg.Outbounds {
		tag := o["tag"].(string)
		if tags[tag] {
			t.Fatalf("повтор тега %q", tag)
		}
		tags[tag] = true
		if o["type"] != "vless" {
			continue
		}
		vless++
		tls := o["tls"].(map[string]any)
		reality := tls["reality"].(map[string]any)
		if o["uuid"] != "u1" || o["server_port"] != float64(443) || reality["public_key"] == "" || tls["server_name"] != "www.example.com" {
			t.Fatalf("неверный outbound: %v", o)
		}
		if tr, ok := o["transport"].(map[string]any); ok {
			xhttp++
			if tr["type"] != "xhttp" || o["flow"] != nil {
				t.Fatalf("XHTTP без flow: %v", o)
			}
		} else if o["flow"] != "xtls-rprx-vision" {
			t.Fatalf("у TCP-варианта нужен flow Vision: %v", o)
		}
	}
	// Мост и прямая ссылка на экзит, у каждого TCP и XHTTP
	if vless != 4 || xhttp != 2 || !tags["🇳🇱 NL direct"] || !tags["🇳🇱 NL direct XHTTP"] || !tags["KVN 10.0.0.1"] {
		t.Fatalf("ожидали 4 сервера (2 XHTTP) с подписями, получили %v", tags)
	}
	// Селектор и urltest ссылаются только на существующие теги, маршрут по умолчанию — в селектор
	for _, o := range cfg.Outbounds {
		if refs, ok := o["outbounds"].([]any); ok {
			for _, r := range refs {
				if !tags[r.(string)] {
					t.Fatalf("%s ссылается на несуществующий %q", o["tag"], r)
				}
			}
		}
	}
	if cfg.Route["final"] != "KVN" {
		t.Fatalf("трафик по умолчанию — в селектор: %v", cfg.Route)
	}

	// Обычный sing-box не умеет XHTTP — такие варианты ему не отдаём
	plain := ev.singbox("SFA/1.12.0 (sing-box 1.12.0)")
	for _, o := range plain.Outbounds {
		if _, ok := o["transport"]; ok {
			t.Fatalf("sing-box без XHTTP получил XHTTP: %v", o)
		}
	}

	// Остальным — base64, с явным переопределением формата
	if rec := ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "v2rayNG/1.9"}); strings.HasPrefix(rec.Body.String(), "{") {
		t.Fatal("v2rayNG должен получать base64")
	}
	if rec := ev.do("GET", "/sub/tok12345?format=base64", "", map[string]string{"User-Agent": "Karing/1.2"}); strings.HasPrefix(rec.Body.String(), "{") {
		t.Fatal("?format=base64 перекрывает определение по User-Agent")
	}
	if rec := ev.do("GET", "/sub/tok12345?format=singbox", "", map[string]string{"User-Agent": "v2rayNG/1.9"}); !strings.HasPrefix(rec.Body.String(), "{") {
		t.Fatal("?format=singbox отдаёт JSON любому клиенту")
	}
}

func TestSubFormat(t *testing.T) {
	for ua, want := range map[string]string{
		"Karing/1.2.19.2209": "singbox", "SFI/1.12 (sing-box)": "singbox",
		"NekoBox/1.3": "base64", "Throne/1.0": "base64", "Happ/3.1": "base64", "v2rayNG/1.9": "base64", "HiddifyNext/2.5": "base64", "": "base64",
	} {
		if got := subFormat(ua, ""); got != want {
			t.Errorf("%q: %s, ожидали %s", ua, got, want)
		}
	}
	if subFormat("v2rayNG", "singbox") != "singbox" || subFormat("Karing", "base64") != "base64" {
		t.Error("?format= должен перекрывать User-Agent")
	}
}

func TestServerNamesUnique(t *testing.T) {
	ev := newEnv(t)
	ev.syncAs(ev.addNode("10.0.0.1", protocol.RoleBridge))
	ev.syncAs(ev.addNode("10.0.0.3", protocol.RoleBridge))
	ev.db.Model(&models.Node{}).Where("1 = 1").Update("label", "RU")
	servers, err := ev.s.subServers()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, srv := range servers {
		if seen[srv.Name] {
			t.Fatalf("одинаковые подписи должны различаться: %q", srv.Name)
		}
		seen[srv.Name] = true
	}
	if !seen["RU"] || !seen["RU 2"] {
		t.Fatalf("ожидали «RU» и «RU 2»: %v", seen)
	}
}

func TestFingerprintSetting(t *testing.T) {
	ev := newEnv(t)
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.syncAs(ev.addNode("10.0.0.2", protocol.RoleExit))
	ev.db.Create(&models.User{ID: "u1", Name: "alice", SubToken: "tok12345", Status: "active"})

	// По умолчанию — chrome, как раньше
	if got := ev.syncAs(bridge).Fingerprint; got != "chrome" {
		t.Fatalf("по умолчанию chrome, получили %q", got)
	}
	if rec := ev.putSettings(`{"region_route":"warp","fingerprint":"opera"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("неизвестный fingerprint: ожидали 400, получили %d", rec.Code)
	}
	if rec := ev.putSettings(`{"region_route":"warp","fingerprint":"firefox","xhttp":true,"direct_exit_links":true}`); rec.Code != http.StatusOK {
		t.Fatalf("сохранение: %d %s", rec.Code, rec.Body)
	}

	// Связка мост → выходная нода
	if got := ev.syncAs(bridge).Fingerprint; got != "firefox" {
		t.Fatalf("мост должен получить firefox, получили %q", got)
	}
	// Ссылки подписки
	raw, _ := base64.StdEncoding.DecodeString(ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "v2rayNG"}).Body.String())
	if strings.Contains(string(raw), "fp=chrome") || strings.Count(string(raw), "fp=firefox") != strings.Count(string(raw), "vless://") {
		t.Fatalf("во всех ссылках должен быть fp=firefox: %s", raw)
	}
	// JSON для Karing
	for _, o := range ev.singbox("Karing/1.2").Outbounds {
		if o["type"] == "vless" && o["tls"].(map[string]any)["utls"].(map[string]any)["fingerprint"] != "firefox" {
			t.Fatalf("Karing должен получить firefox: %v", o["tls"])
		}
	}
}

func TestExitLinkSetting(t *testing.T) {
	ev := newEnv(t)
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.syncAs(ev.addNode("10.0.0.2", protocol.RoleExit))

	if e := ev.syncAs(bridge).Exits[0]; e.XHTTPPath != "" {
		t.Fatalf("по умолчанию — TCP, получили XHTTP %q", e.XHTTPPath)
	}
	if rec := ev.putSettings(`{"region_route":"warp","exit_link":"quic"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("неизвестный транспорт: ожидали 400, получили %d", rec.Code)
	}
	ev.putSettings(`{"region_route":"warp","exit_link":"xhttp","xhttp":true}`)
	if e := ev.syncAs(bridge).Exits[0]; e.XHTTPPath == "" || !strings.HasPrefix(e.XHTTPPath, "/") {
		t.Fatalf("мост должен получить XHTTP-путь экзита: %+v", e)
	}
	// XHTTP-вход на экзите есть только при включённом XHTTP — иначе остаётся TCP
	ev.putSettings(`{"region_route":"warp","exit_link":"xhttp","xhttp":false}`)
	if e := ev.syncAs(bridge).Exits[0]; e.XHTTPPath != "" {
		t.Fatalf("при выключенном XHTTP связь должна остаться TCP: %+v", e)
	}
}
