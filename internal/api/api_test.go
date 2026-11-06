package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type env struct {
	t        *testing.T
	e        *echo.Echo
	s        *Server
	db       *gorm.DB
	deployed []runner.DeployParams
	now      time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.AutoMigrate(&models.User{}, &models.Node{}, &models.Setting{}, &models.NotifyMessage{}, &models.LinkSample{}, &models.NodeMetric{}, &models.TerminalSession{}); err != nil {
		t.Fatal(err)
	}

	ev := &env{t: t, e: echo.New(), db: gdb, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	ev.s = New(Config{
		AdminPassword: "secret",
		JWTSecret:     []byte(strings.Repeat("k", 32)),
		MasterURL:     "https://panel.example.com",
		DefaultSNI:    "www.example.com",
		SSHKeyPath:    filepath.Join(t.TempDir(), "id_ed25519"),
	}, gdb)
	ev.s.deploy = func(p runner.DeployParams) { ev.deployed = append(ev.deployed, p) }
	ev.s.now = func() time.Time { return ev.now }
	ev.s.Register(ev.e)
	return ev
}

func (ev *env) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ev.e.ServeHTTP(rec, req)
	return rec
}

func (ev *env) adminToken() map[string]string {
	rec := ev.do("POST", "/api/login", `{"password":"secret"}`, nil)
	if rec.Code != http.StatusOK {
		ev.t.Fatalf("логин: %d %s", rec.Code, rec.Body)
	}
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	return map[string]string{"Authorization": "Bearer " + out["token"]}
}

// addNode создаёт ноду через API и возвращает её токен агента
func (ev *env) addNode(ip, role string) string {
	rec := ev.do("POST", "/api/nodes", `{"ip":"`+ip+`","type":"`+role+`","password":"rootpw"}`, ev.adminToken())
	if rec.Code != http.StatusAccepted {
		ev.t.Fatalf("создание ноды: %d %s", rec.Code, rec.Body)
	}
	return ev.deployed[len(ev.deployed)-1].NodeToken
}

func (ev *env) syncAs(token string) protocol.SyncResponse {
	rec := ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token})
	if rec.Code != http.StatusOK {
		ev.t.Fatalf("sync: %d %s", rec.Code, rec.Body)
	}
	var resp protocol.SyncResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp
}

func TestLogin(t *testing.T) {
	ev := newEnv(t)
	if rec := ev.do("POST", "/api/login", `{"password":"wrong"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("неверный пароль: ожидали 401, получили %d", rec.Code)
	}
	if rec := ev.do("GET", "/api/users", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("без токена: ожидали 401, получили %d", rec.Code)
	}
	if rec := ev.do("GET", "/api/users", "", ev.adminToken()); rec.Code != http.StatusOK {
		t.Fatalf("с токеном: ожидали 200, получили %d", rec.Code)
	}
}

func TestNodeValidation(t *testing.T) {
	ev := newEnv(t)
	auth := ev.adminToken()
	for _, body := range []string{
		`{"ip":"not-an-ip","type":"bridge","password":"x"}`,
		`{"ip":"1.2.3.4","type":"weird","password":"x"}`,
		`{"ip":"1.2.3.4","type":"bridge","password":""}`,
		`{"ip":"1.2.3.4","type":"bridge","password":"x","sni":"bad sni"}`,
	} {
		if rec := ev.do("POST", "/api/nodes", body, auth); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: ожидали 400, получили %d", body, rec.Code)
		}
	}
}

func TestNodeSecretsNotStored(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.1", protocol.RoleBridge)
	p := ev.deployed[0]
	if p.MasterURL != "https://panel.example.com" || p.PrivateKey == "" || token == "" {
		t.Fatalf("неполные параметры деплоя: %+v", p)
	}

	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.1")
	if n.TokenHash == token || n.TokenHash != hashToken(token) {
		t.Fatal("в БД должен лежать только хэш токена")
	}
	rec := ev.do("GET", "/api/nodes", "", ev.adminToken())
	if strings.Contains(rec.Body.String(), n.TokenHash) {
		t.Fatal("хэш токена не должен уходить во фронтенд")
	}
}

func TestSyncCascade(t *testing.T) {
	ev := newEnv(t)
	bridgeToken := ev.addNode("10.0.0.1", protocol.RoleBridge)
	exitToken := ev.addNode("10.0.0.2", protocol.RoleExit)

	past := ev.now.Add(-time.Hour)
	ev.db.Create(&models.User{ID: "ok", Name: "ok", Status: "active"})
	ev.db.Create(&models.User{ID: "blocked", Name: "blocked", Status: "blocked"})
	ev.db.Create(&models.User{ID: "expired", Name: "expired", Status: "active", ExpiresAt: &past})
	ev.db.Create(&models.User{ID: "quota", Name: "quota", Status: "active", TrafficQuota: 100, TrafficDown: 100})

	if rec := ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: "bad"}); rec.Code != http.StatusNotFound {
		t.Fatalf("чужой токен: ожидали 404, получили %d", rec.Code)
	}

	// Экзит ещё ни разу не выходил на связь — мосту его не отдаём
	if got := ev.syncAs(bridgeToken); len(got.Exits) != 0 {
		t.Fatalf("экзит офлайн, а мост его получил: %+v", got.Exits)
	}

	exitResp := ev.syncAs(exitToken)
	var bridge models.Node
	ev.db.First(&bridge, "ip = ?", "10.0.0.1")
	// По умолчанию в подписке есть прямые ссылки, поэтому выходная нода пускает мост и активных юзеров
	if exitResp.Role != protocol.RoleExit || len(exitResp.Clients) != 2 ||
		exitResp.Clients[0].ID != bridge.LinkUUID || exitResp.Clients[1].ID != "ok" {
		t.Fatalf("выходная нода должна пускать мост и активного юзера: %+v", exitResp)
	}

	got := ev.syncAs(bridgeToken)
	if len(got.Clients) != 1 || got.Clients[0].ID != "ok" {
		t.Fatalf("мост должен получить только активного юзера: %+v", got.Clients)
	}
	if len(got.Exits) != 1 || got.Exits[0].Address != "10.0.0.2" || got.Exits[0].UUID != bridge.LinkUUID {
		t.Fatalf("мост должен получить экзит: %+v", got.Exits)
	}

	// Экзит пропал больше чем на NodeAliveTimeout — он остаётся в каскаде: мёртвые ноды
	// отсеивает Xray на мосту, а если мертвы все, трафик блокируется, а не идёт с IP моста
	ev.now = ev.now.Add(NodeAliveTimeout + time.Minute)
	if got := ev.syncAs(bridgeToken); len(got.Exits) != 1 {
		t.Fatalf("пропавший экзит должен остаться в каскаде: %+v", got.Exits)
	}
}

func TestStats(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.db.Create(&models.User{ID: "u1", Name: "u1", Status: "active", TrafficUp: 10})

	body := `[{"email":"u1","up":5,"down":7},{"email":"u1","up":-100,"down":0},{"email":"ghost","up":1,"down":1}]`
	if rec := ev.do("POST", "/api/stats", body, map[string]string{protocol.NodeTokenHeader: token}); rec.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body)
	}
	var u models.User
	ev.db.First(&u, "id = ?", "u1")
	if u.TrafficUp != 15 || u.TrafficDown != 7 {
		t.Fatalf("неверный трафик: up=%d down=%d", u.TrafficUp, u.TrafficDown)
	}
}

func TestSubscription(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.syncAs(token) // мост вышел на связь
	ev.db.Create(&models.User{ID: "u1", Name: `<script>alert(1)</script>`, Status: "active"})

	rec := ev.do("GET", "/sub/u1", "", map[string]string{"User-Agent": "v2rayNG/1.8"})
	raw, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatalf("ожидали base64: %v", err)
	}
	link := string(raw)
	for _, want := range []string{"vless://u1@10.0.0.1:443?", "flow=xtls-rprx-vision", "security=reality", "type=tcp", "sni=www.example.com"} {
		if !strings.Contains(link, want) {
			t.Errorf("в ссылке нет %q: %s", want, link)
		}
	}

	page := ev.do("GET", "/sub/u1", "", browserHeaders).Body.String()
	if !strings.Contains(page, "<!DOCTYPE html>") {
		t.Fatal("браузеру должна отдаваться страница")
	}
	if strings.Contains(page, "<script>alert(1)") {
		t.Fatal("имя пользователя не экранировано (XSS)")
	}

	ev.db.Model(&models.User{}).Where("id = ?", "u1").Update("status", "blocked")
	if rec := ev.do("GET", "/sub/u1", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("заблокированному юзеру подписка не положена, код %d", rec.Code)
	}
}

func (ev *env) putSettings(body string) *httptest.ResponseRecorder {
	return ev.do("PUT", "/api/settings", body, ev.adminToken())
}

func TestSettingsValidation(t *testing.T) {
	ev := newEnv(t)
	for _, body := range []string{
		`{"region":"xx","region_route":"warp"}`,
		`{"region":"ru","region_route":"nowhere"}`,
		`{"region":"ru","region_route":"warp","warp_rules":["bad rule"]}`,
		`{"region":"ru","region_route":"warp","bridge_direct":["geosite:ok","<script>"]}`,
	} {
		if rec := ev.putSettings(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: ожидали 400, получили %d", body, rec.Code)
		}
	}
	if rec := ev.do("GET", "/api/settings", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("настройки без токена: ожидали 401, получили %d", rec.Code)
	}
}

func TestRegionRouting(t *testing.T) {
	ev := newEnv(t)
	bridgeToken := ev.addNode("10.0.0.1", protocol.RoleBridge)
	exitToken := ev.addNode("10.0.0.2", protocol.RoleExit)

	// Россия → через WARP на выходной ноде, плюс свои правила
	rec := ev.putSettings(`{"region":"ru","region_route":"warp","warp_rules":["geosite:google"],"bridge_direct":["domain:example.com"],"direct_exit_links":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("сохранение настроек: %d %s", rec.Code, rec.Body)
	}
	exit := ev.syncAs(exitToken)
	if strings.Join(exit.Warp, ",") != "geosite:google,geosite:category-ru,geoip:ru" {
		t.Fatalf("неверные правила WARP: %v", exit.Warp)
	}
	bridge := ev.syncAs(bridgeToken)
	if strings.Join(bridge.Direct, ",") != "domain:example.com" {
		t.Fatalf("неверные прямые правила моста: %v", bridge.Direct)
	}

	// Та же страна → напрямую с моста
	ev.putSettings(`{"region":"ru","region_route":"bridge","warp_rules":["geosite:google"],"direct_exit_links":true}`)
	if got := ev.syncAs(exitToken).Warp; strings.Join(got, ",") != "geosite:google" {
		t.Fatalf("страна не должна идти в WARP: %v", got)
	}
	if got := ev.syncAs(bridgeToken).Direct; strings.Join(got, ",") != "geosite:category-ru,geoip:ru" {
		t.Fatalf("страна должна идти напрямую с моста: %v", got)
	}
}

// Одиночный режим: без выходных нод правила WARP выполняет сам мост
func TestSingleServerWarp(t *testing.T) {
	ev := newEnv(t)
	bridgeToken := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.putSettings(`{"region":"ru","region_route":"warp","warp_rules":["geosite:openai"]}`)

	if got := ev.syncAs(bridgeToken).Warp; strings.Join(got, ",") != "geosite:openai,geosite:category-ru,geoip:ru" {
		t.Fatalf("мост без выходных нод должен получить правила WARP: %v", got)
	}

	// Выходная нода добавлена, но ещё не вышла на связь (деплой идёт или упал) — одиночный режим остаётся
	exitToken := ev.addNode("10.0.0.2", protocol.RoleExit)
	if b := ev.syncAs(bridgeToken); len(b.Exits) != 0 || len(b.Warp) == 0 {
		t.Fatalf("недеплоенная нода не должна отключать VPN: exits=%d", len(b.Exits))
	}

	// Вышла на связь — каскад: WARP снова на выходной ноде
	ev.syncAs(exitToken)
	bridge := ev.syncAs(bridgeToken)
	if len(bridge.Warp) != 0 || len(bridge.Exits) != 1 {
		t.Fatalf("с выходной нодой мосту WARP не нужен: warp=%v exits=%d", bridge.Warp, len(bridge.Exits))
	}

	// Нода пропала надолго — мост её не выбрасывает: иначе трафик молча пошёл бы с IP моста.
	// Xray сам увидит, что она мертва, и заблокирует трафик.
	ev.now = ev.now.Add(time.Hour)
	if b := ev.syncAs(bridgeToken); len(b.Exits) != 1 || len(b.Warp) != 0 {
		t.Fatalf("упавшая выходная нода должна остаться в каскаде: exits=%d", len(b.Exits))
	}

	// Удалили в панели — снова одиночный режим
	ev.do("DELETE", "/api/nodes/10.0.0.2", "", ev.adminToken())
	if b := ev.syncAs(bridgeToken); len(b.Exits) != 0 || len(b.Warp) == 0 {
		t.Fatalf("после удаления выходной ноды — одиночный режим: exits=%d warp=%v", len(b.Exits), b.Warp)
	}
}

func TestDirectExitLinks(t *testing.T) {
	ev := newEnv(t)
	bridgeToken := ev.addNode("10.0.0.1", protocol.RoleBridge)
	exitToken := ev.addNode("10.0.0.2", protocol.RoleExit)
	ev.syncAs(bridgeToken)
	ev.syncAs(exitToken)
	ev.db.Create(&models.User{ID: "u1", Name: "u1", Status: "active"})

	sub := func() string {
		rec := ev.do("GET", "/sub/u1", "", map[string]string{"User-Agent": "v2rayNG/1.8"})
		raw, _ := base64.StdEncoding.DecodeString(rec.Body.String())
		return string(raw)
	}
	if links := sub(); !strings.Contains(links, "@10.0.0.1:443") || !strings.Contains(links, "@10.0.0.2:443") {
		t.Fatalf("в подписке должны быть мост и прямая ссылка: %s", links)
	}

	ev.putSettings(`{"region":"","region_route":"exit","direct_exit_links":false}`)
	if links := sub(); strings.Contains(links, "@10.0.0.2:443") {
		t.Fatalf("прямая ссылка выключена, а она есть: %s", links)
	}
	for _, c := range ev.syncAs(exitToken).Clients {
		if c.ID == "u1" {
			t.Fatal("без прямых ссылок выходная нода не должна пускать юзеров")
		}
	}
}

func TestNodeStatusHeaders(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.2", protocol.RoleExit)
	ev.do("GET", "/api/sync", "", map[string]string{
		protocol.NodeTokenHeader:   token,
		protocol.WarpHeader:        "1",
		protocol.ConfigErrorHeader: "xray%20%D0%BE%D1%82%D0%B2%D0%B5%D1%80%D0%B3",
	})
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.2")
	if !n.WarpOK || n.ConfigError != "xray отверг" {
		t.Fatalf("состояние ноды не сохранилось: warp=%v err=%q", n.WarpOK, n.ConfigError)
	}
}

func TestPickLang(t *testing.T) {
	cases := map[[2]string]string{
		{"ru-RU,ru;q=0.9,en;q=0.8", ""}: "ru",
		{"en-US,en;q=0.9", ""}:          "en",
		{"de-DE", ""}:                   "en",
		{"en-US", "ru"}:                 "ru",
	}
	for in, want := range cases {
		if got := pickLang(in[0], in[1]); got != want {
			t.Errorf("pickLang(%q, %q) = %s, ожидали %s", in[0], in[1], got, want)
		}
	}
}

func TestUpdateNodeSNI(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.2", protocol.RoleExit)
	ev.do("GET", "/api/sync", "", map[string]string{
		protocol.NodeTokenHeader: token,
		protocol.SNIErrorHeader:  "www.example.com%20недоступен",
	})
	auth := ev.adminToken()

	if rec := ev.do("PATCH", "/api/nodes/10.0.0.2", `{"sni":"bad sni"}`, auth); rec.Code != http.StatusBadRequest {
		t.Fatalf("некорректный SNI: ожидали 400, получили %d", rec.Code)
	}
	if rec := ev.do("PATCH", "/api/nodes/10.0.0.2", `{"sni":"www.apple.com"}`, auth); rec.Code != http.StatusNoContent {
		t.Fatalf("смена SNI: %d %s", rec.Code, rec.Body)
	}
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.2")
	if n.SNI != "www.apple.com" || n.SNIError != "" {
		t.Fatalf("SNI не обновился или осталась старая ошибка: %q / %q", n.SNI, n.SNIError)
	}
	if got := ev.syncAs(token).Reality.SNI; got != "www.apple.com" {
		t.Fatalf("нода должна получить новый SNI, получила %q", got)
	}

	// SNI моста на сервере Мастера менять нельзя
	ev.db.Model(&models.Node{}).Where("ip = ?", "10.0.0.2").Update("reality_dest", "127.0.0.1:8443")
	// Подпись можно менять и у моста на сервере Мастера; пустая — сбрасывает
	if rec := ev.do("PATCH", "/api/nodes/10.0.0.2", `{"label":" 🇷🇺 Москва "}`, auth); rec.Code != http.StatusNoContent {
		t.Fatalf("подпись: %d %s", rec.Code, rec.Body)
	}
	var labeled models.Node
	ev.db.First(&labeled, "ip = ?", "10.0.0.2")
	if labeled.Label != "🇷🇺 Москва" {
		t.Fatalf("подпись не сохранилась: %q", labeled.Label)
	}
	for _, bad := range []string{`{"label":"a\nb"}`, `{"label":"` + strings.Repeat("я", 41) + `"}`, `{}`} {
		if rec := ev.do("PATCH", "/api/nodes/10.0.0.2", bad, auth); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: ожидали 400, получили %d", bad, rec.Code)
		}
	}
	if rec := ev.do("PATCH", "/api/nodes/10.0.0.2", `{"sni":"www.apple.com"}`, auth); rec.Code != http.StatusBadRequest {
		t.Fatalf("SNI локального моста: ожидали 400, получили %d", rec.Code)
	}
}

func TestRedeployNode(t *testing.T) {
	ev := newEnv(t)
	oldToken := ev.addNode("10.0.0.2", protocol.RoleExit)
	var before models.Node
	ev.db.First(&before, "ip = ?", "10.0.0.2")
	auth := ev.adminToken()

	if rec := ev.do("POST", "/api/nodes/10.0.0.2/redeploy", `{}`, auth); rec.Code != http.StatusBadRequest {
		t.Fatalf("без пароля: ожидали 400, получили %d", rec.Code)
	}
	if rec := ev.do("POST", "/api/nodes/10.0.0.2/redeploy", `{"password":"pw"}`, auth); rec.Code != http.StatusAccepted {
		t.Fatalf("переустановка: %d %s", rec.Code, rec.Body)
	}
	p := ev.deployed[len(ev.deployed)-1]
	if p.IP != "10.0.0.2" || p.Role != protocol.RoleExit || p.RootPassword != "pw" || p.NodeToken == oldToken {
		t.Fatalf("неверные параметры переустановки: %+v", p)
	}

	var after models.Node
	ev.db.First(&after, "ip = ?", "10.0.0.2")
	if after.PubKey == before.PubKey || after.SID == before.SID || after.LinkUUID != before.LinkUUID || after.SNI != before.SNI {
		t.Fatalf("ключи должны смениться, а LinkUUID и SNI остаться: до %+v, после %+v", before, after)
	}
	// Старый токен больше не работает, новый — работает
	if rec := ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: oldToken}); rec.Code != http.StatusNotFound {
		t.Fatalf("старый токен: ожидали 404, получили %d", rec.Code)
	}
	ev.syncAs(p.NodeToken)
}

func TestNodeHealthHeaders(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.2", protocol.RoleExit)
	ev.do("GET", "/api/sync", "", map[string]string{
		protocol.NodeTokenHeader:   token,
		protocol.SNIErrorHeader:    "sni%20fail",
		protocol.WarpErrorHeader:   "WARP%3A%20Registration%20Missing",
		protocol.ConfigErrorHeader: strings.Repeat("%D0%AF", 1500), // 1500 кириллических символов
	})
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.2")
	if n.SNIError != "sni fail" || n.WarpError != "WARP: Registration Missing" {
		t.Fatalf("состояние не сохранилось: %q / %q", n.SNIError, n.WarpError)
	}
	if r := []rune(n.ConfigError); len(r) != 1000 || r[0] != 'Я' {
		t.Fatalf("ошибка должна обрезаться по символам до 1000, получили %d", len(r))
	}
	// Обе ошибки уходят во фронтенд
	body := ev.do("GET", "/api/nodes", "", ev.adminToken()).Body.String()
	if !strings.Contains(body, `"SNIError":"sni fail"`) || !strings.Contains(body, `"WarpError"`) {
		t.Fatalf("фронтенд не получает состояние ноды: %s", body)
	}
}

func TestBackupDownload(t *testing.T) {
	ev := newEnv(t)
	ev.db.Create(&models.User{ID: "u1", Name: "alice", Status: "active"})

	if rec := ev.do("GET", "/api/backup", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("бэкап без токена: ожидали 401, получили %d", rec.Code)
	}
	rec := ev.do("GET", "/api/backup", "", ev.adminToken())
	if rec.Code != http.StatusOK {
		t.Fatalf("бэкап: %d %s", rec.Code, rec.Body)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "kvn-backup-") {
		t.Fatalf("ожидали файл kvn-backup-*, получили %q", cd)
	}

	// Скачанный файл — рабочая база с пользователем
	path := filepath.Join(t.TempDir(), "restored.sqlite")
	os.WriteFile(path, rec.Body.Bytes(), 0o600)
	restored, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	var u models.User
	if err := restored.First(&u, "id = ?", "u1").Error; err != nil || u.Name != "alice" {
		t.Fatalf("в бэкапе нет пользователя: %+v, %v", u, err)
	}
}

func TestXHTTP(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.db.Create(&models.User{ID: "u1", Name: "u1", Status: "active"})

	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.1")
	if !strings.HasPrefix(n.XHTTPPath, "/") || len(n.XHTTPPath) < 10 {
		t.Fatalf("новой ноде нужен случайный путь XHTTP, получили %q", n.XHTTPPath)
	}
	if got := ev.syncAs(token).XHTTPPath; got != n.XHTTPPath {
		t.Fatalf("нода должна получить свой путь XHTTP, получила %q", got)
	}

	sub := func() string {
		rec := ev.do("GET", "/sub/u1", "", map[string]string{"User-Agent": "v2rayNG/1.8"})
		raw, _ := base64.StdEncoding.DecodeString(rec.Body.String())
		return string(raw)
	}
	links := sub()
	if !strings.Contains(links, "type=tcp") || !strings.Contains(links, "type=xhttp") ||
		!strings.Contains(links, "path="+url.QueryEscape(n.XHTTPPath)) {
		t.Fatalf("в подписке должны быть TCP и XHTTP: %s", links)
	}
	for _, l := range strings.Split(links, "\n") {
		if strings.Contains(l, "type=xhttp") && strings.Contains(l, "flow=") {
			t.Fatalf("у XHTTP-ссылки не должно быть flow (Vision только для TCP): %s", l)
		}
	}

	// Выключили XHTTP — ни на ноде, ни в подписке его нет
	ev.putSettings(`{"region":"","region_route":"exit","direct_exit_links":true,"xhttp":false}`)
	if got := ev.syncAs(token).XHTTPPath; got != "" {
		t.Fatalf("XHTTP выключен, а нода получила путь %q", got)
	}
	if strings.Contains(sub(), "type=xhttp") {
		t.Fatal("XHTTP выключен, а ссылка в подписке есть")
	}
}

func TestEnsureXHTTPPaths(t *testing.T) {
	ev := newEnv(t)
	ev.addNode("10.0.0.1", protocol.RoleBridge)
	// Нода, созданная до появления XHTTP
	ev.db.Model(&models.Node{}).Where("ip = ?", "10.0.0.1").Update("x_http_path", "")
	var before models.Node
	ev.db.First(&before, "ip = ?", "10.0.0.1")
	if before.XHTTPPath != "" {
		t.Fatalf("не удалось сбросить путь — неверное имя колонки? %q", before.XHTTPPath)
	}

	if err := EnsureXHTTPPaths(ev.db); err != nil {
		t.Fatal(err)
	}
	var after models.Node
	ev.db.First(&after, "ip = ?", "10.0.0.1")
	if !strings.HasPrefix(after.XHTTPPath, "/") {
		t.Fatalf("старой ноде должен выдаться путь, получили %q", after.XHTTPPath)
	}
}
