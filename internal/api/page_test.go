package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

var browserHeaders = map[string]string{
	"User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)",
	"Accept":     "text/html,application/xhtml+xml",
}

func TestSubscriptionPage(t *testing.T) {
	ev := newEnv(t)
	ev.syncAs(ev.addNode("10.0.0.1", protocol.RoleBridge))
	exp := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ev.db.Create(&models.User{ID: "u1", Name: "alice", SubToken: "tok12345", Status: "active",
		TrafficDown: 3 << 30, TrafficQuota: 10 << 30, ExpiresAt: &exp})

	page := ev.do("GET", "/sub/tok12345", "", browserHeaders).Body.String()
	for _, want := range []string{
		`href="karing://install-config?url=http%3a%2f%2fexample.com%2fsub%2ftok12345&amp;name=KVN"`,
		`href="happ://add/http://example.com/sub/tok12345"`,
		`href="https://apps.apple.com/app/karing/id6472431552"`,
		`class="tab on" data-tab="ios"`, // iPhone — сразу вкладка iOS
		"3.00 GB", "10.00 GB", "01.06.2026",
		"data:image/png;base64,",
	} {
		if !strings.Contains(strings.ToLower(page), strings.ToLower(want)) {
			t.Errorf("на странице нет %s", want)
		}
	}
	if strings.Contains(page, "ZgotmplZ") {
		t.Fatal("html/template вырезал ссылку")
	}

	// Клиент с «Mozilla» в User-Agent, но без text/html (так делают некоторые приложения) — получает подписку
	rec := ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "Mozilla/5.0 Happ/3.1"})
	if _, err := base64.StdEncoding.DecodeString(rec.Body.String()); err != nil || rec.Body.Len() == 0 {
		t.Fatalf("приложению нужна base64-подписка, а не страница: %.80s", rec.Body.String())
	}

	// Неактивному — страница с причиной и без ссылок
	ev.now = exp.Add(time.Hour)
	rec = ev.do("GET", "/sub/tok12345", "", browserHeaders)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), subTexts["en"]["reasonExpired"]) ||
		strings.Contains(rec.Body.String(), "vless://") {
		t.Fatalf("истёкшая подписка: %d", rec.Code)
	}
}

func TestPageSettings(t *testing.T) {
	ev := newEnv(t)
	admin := ev.adminToken()

	var got struct {
		Page     PageSettings
		Defaults PageSettings
	}
	json.Unmarshal(ev.do("GET", "/api/page-settings", "", admin).Body.Bytes(), &got)
	if got.Page.Title != "KVN" || len(got.Page.Clients) != len(DefaultClients()) {
		t.Fatalf("по умолчанию — встроенные клиенты: %+v", got.Page)
	}

	for name, body := range map[string]string{
		"пустое название":    `{"title":" "}`,
		"javascript":         `{"title":"X","clients":[{"name":"a","deeplink":"javascript://%0aalert(1)","downloads":{"ios":"https://x.com"}}]}`,
		"http как deeplink":  `{"title":"X","clients":[{"name":"a","deeplink":"http://x.com/{url}"}]}`,
		"скачивание по http": `{"title":"X","clients":[{"name":"a","downloads":{"ios":"http://x.com"}}]}`,
		"платформа":          `{"title":"X","clients":[{"name":"a","downloads":{"tv":"https://x.com"}}]}`,
		"поддержка":          `{"title":"X","support_url":"javascript:alert(1)"}`,
	} {
		if rec := ev.do("PUT", "/api/page-settings", body, admin); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: ожидали 400, получили %d", name, rec.Code)
		}
	}

	body := `{"title":"Мой VPN","support_url":"tg://resolve?domain=me","clients":[{"name":"Only","deeplink":"only://import/{url}","downloads":{"android":"https://example.com/only.apk","ios":""}}]}`
	if rec := ev.do("PUT", "/api/page-settings", body, admin); rec.Code != http.StatusOK {
		t.Fatalf("сохранение: %d %s", rec.Code, rec.Body)
	}
	p := ev.s.loadPage()
	if len(p.Clients) != 1 || len(p.Clients[0].Downloads) != 1 {
		t.Fatalf("сохранённый список заменяет встроенный, пустые ссылки отбрасываются: %+v", p)
	}

	ev.syncAs(ev.addNode("10.0.0.1", protocol.RoleBridge))
	ev.db.Create(&models.User{ID: "u1", Name: "bob", SubToken: "tok12345", Status: "active"})
	rec := ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "Hiddify"})
	if rec.Header().Get("Profile-Title") != "base64:"+base64.StdEncoding.EncodeToString([]byte("Мой VPN")) {
		t.Fatalf("название профиля из настроек: %q", rec.Header().Get("Profile-Title"))
	}
	page := ev.do("GET", "/sub/tok12345", "", browserHeaders).Body.String()
	if !strings.Contains(page, `href="tg://resolve?domain=me"`) || strings.Contains(page, "Karing") {
		t.Fatal("страница должна брать приложения и поддержку из настроек")
	}
	// Для iOS приложений нет: вкладки iOS нет, открыта первая доступная
	if strings.Contains(page, `data-tab="ios"`) || !strings.Contains(page, `class="tab on" data-tab="android"`) {
		t.Fatal("вкладка платформы без приложений не нужна, открыться должна первая доступная")
	}
}

func TestDetectPlatform(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)": "ios",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8)":               "android",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64)":              "windows",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)":           "macos",
		"Mozilla/5.0 (X11; Linux x86_64)":                        "linux",
	} {
		if got := detectPlatform(ua); got != want {
			t.Errorf("%s: %s, ожидали %s", ua, got, want)
		}
	}
}
