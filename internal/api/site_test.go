package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
)

// hiddenEnv — панель на секретном пути, с интерфейсом и (по желанию) своим сайтом
func hiddenEnv(t *testing.T, site string) *env {
	ev := newEnv(t)
	ui := t.TempDir()
	os.WriteFile(filepath.Join(ui, "index.html"), []byte("<div id=app>panel</div>"), 0o644)
	ev.e = echo.New()
	ev.s = New(Config{
		AdminPassword: "secret",
		JWTSecret:     []byte(strings.Repeat("k", 32)),
		MasterURL:     "https://panel.example.com",
		DefaultSNI:    "www.example.com",
		AdminPath:     "/s3cr3t-path",
		FrontendDir:   ui,
		SiteDir:       site,
	}, ev.db)
	ev.s.deploy = func(p runner.DeployParams) { ev.deployed = append(ev.deployed, p) }
	ev.s.Register(ev.e)
	return ev
}

func TestHiddenPanel(t *testing.T) {
	ev := hiddenEnv(t, "")

	// Снаружи — обычный сайт-заглушка
	if rec := ev.do("GET", "/", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Coming soon") {
		t.Fatalf("корень: %d %s", rec.Code, rec.Body)
	}
	for _, probe := range []struct{ method, path string }{
		{"POST", "/api/login"}, {"GET", "/api/users"}, {"GET", "/api/sync"},
		{"GET", "/sub/57d2bfa5b82efcc2d127dd64f5dce8c7"}, {"GET", "/index.html"}, {"GET", "/wp-admin"},
	} {
		rec := ev.do(probe.method, probe.path, `{"password":"secret"}`, nil)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Not Found") || strings.Contains(rec.Body.String(), `"message"`) {
			t.Errorf("%s %s: ожидали обычную 404, получили %d %s", probe.method, probe.path, rec.Code, rec.Body)
		}
	}

	// Панель — по секретному пути
	if rec := ev.do("GET", "/s3cr3t-path", "", nil); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/s3cr3t-path/" {
		t.Fatalf("без слеша — редирект: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := ev.do("GET", "/s3cr3t-path/", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "panel") {
		t.Fatalf("интерфейс: %d %s", rec.Code, rec.Body)
	}
	rec := ev.do("POST", "/s3cr3t-path/api/login", `{"password":"secret"}`, nil)
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["token"] == "" {
		t.Fatalf("вход: %d %s", rec.Code, rec.Body)
	}
	auth := map[string]string{"Authorization": "Bearer " + out["token"]}
	if rec := ev.do("GET", "/s3cr3t-path/api/users", "", nil); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("API панели без токена отвечает JSON-ошибкой: %d %s", rec.Code, rec.Body)
	}

	// Агенты ходят по старому пути /api/sync со своим токеном
	rec = ev.do("POST", "/s3cr3t-path/api/nodes", `{"ip":"10.0.0.1","type":"bridge","password":"pw"}`, auth)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("создание ноды: %d %s", rec.Code, rec.Body)
	}
	token := ev.deployed[len(ev.deployed)-1].NodeToken
	if rec := ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token}); rec.Code != http.StatusOK {
		t.Fatalf("синхронизация агента: %d", rec.Code)
	}
}

func TestOwnSite(t *testing.T) {
	site := t.TempDir()
	os.WriteFile(filepath.Join(site, "index.html"), []byte("<h1>My blog</h1>"), 0o644)
	ev := hiddenEnv(t, site)
	if rec := ev.do("GET", "/", "", nil); !strings.Contains(rec.Body.String(), "My blog") {
		t.Fatalf("свой сайт вместо заглушки: %s", rec.Body)
	}
	if rec := ev.do("GET", "/s3cr3t-path/", "", nil); !strings.Contains(rec.Body.String(), "panel") {
		t.Fatal("панель по секретному пути должна остаться")
	}
}

func TestNormalizeAdminPath(t *testing.T) {
	for in, want := range map[string]string{"": "", " /abcdefgh/ ": "/abcdefgh", "Ab-12_xyz": "/Ab-12_xyz"} {
		if got, err := NormalizeAdminPath(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"short", "a/b/cdefgh", "../../etc", "пароль-длинный"} {
		if _, err := NormalizeAdminPath(bad); err == nil {
			t.Errorf("%q должен быть отклонён", bad)
		}
	}
}

func TestSubRateLimit(t *testing.T) {
	ev := newEnv(t)
	var last int
	for i := 0; i < 25; i++ {
		last = ev.do("GET", "/sub/nope12345", "", nil).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("после 20 запросов в минуту ожидали 429, получили %d", last)
	}
}

func TestHeadAndPreviewBots(t *testing.T) {
	ev := hiddenEnv(t, "")
	ev.syncAs(ev.addNodeAt("/s3cr3t-path", "10.0.0.1", protocol.RoleBridge))
	ev.db.Create(&models.User{ID: "u1", Name: "alice", SubToken: "tok12345", Status: "active", TrafficDown: 42})

	// Karing перед обновлением шлёт HEAD — нужны 200 и заголовки, без тела
	karing := map[string]string{"User-Agent": "Karing/1.2.24.2704 platform/android;sing-box 1.13.0"}
	rec := ev.do("HEAD", "/sub/tok12345", "", karing)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || !strings.Contains(rec.Header().Get("Subscription-Userinfo"), "download=42") {
		t.Fatalf("HEAD подписки: %d, тело %d байт, %v", rec.Code, rec.Body.Len(), rec.Header())
	}
	if rec := ev.do("GET", "/sub/tok12345", "", karing); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "{") {
		t.Fatalf("GET после HEAD: %d", rec.Code)
	}
	if rec := ev.do("HEAD", "/", "", nil); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD заглушки: %d", rec.Code)
	}
	if rec := ev.do("HEAD", "/nope", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("HEAD несуществующего: %d", rec.Code)
	}

	// Превью ссылки в Telegram — не отдаём ни страницу, ни конфиги
	for _, ua := range []string{"Mozilla/5.0 (compatible; TelegramBot/1.0 like Linux)", "WhatsApp/2.23", "facebookexternalhit/1.1"} {
		rec := ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": ua, "Accept": "text/html"})
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "vless") || strings.Contains(rec.Body.String(), "alice") {
			t.Fatalf("%s получил подписку: %d", ua, rec.Code)
		}
	}
}

// addNodeAt — создание ноды, когда API панели на секретном пути
func (ev *env) addNodeAt(prefix, ip, role string) string {
	ev.t.Helper()
	rec := ev.do("POST", prefix+"/api/login", `{"password":"secret"}`, nil)
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	auth := map[string]string{"Authorization": "Bearer " + out["token"]}
	if rec := ev.do("POST", prefix+"/api/nodes", `{"ip":"`+ip+`","type":"`+role+`","password":"pw"}`, auth); rec.Code != http.StatusAccepted {
		ev.t.Fatalf("создание ноды: %d %s", rec.Code, rec.Body)
	}
	return ev.deployed[len(ev.deployed)-1].NodeToken
}
