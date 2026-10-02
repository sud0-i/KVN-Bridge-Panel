package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func TestNormalizeXrayVersion(t *testing.T) {
	for in, want := range map[string]string{"v26.3.27": "26.3.27", " 26.3.27 ": "26.3.27", "": ""} {
		if got, ok := normalizeXrayVersion(in); !ok || got != want {
			t.Fatalf("%q → %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"latest", "26.3", "26.3.27; rm -rf /", "../26.3.27"} {
		if _, ok := normalizeXrayVersion(bad); ok {
			t.Fatalf("%q должно отклоняться", bad)
		}
	}
}

func TestAgentBinaryAndMaintenance(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.2", protocol.RoleExit)
	node := map[string]string{protocol.NodeTokenHeader: token}

	// Бинарника нет — 404, самообновления в sync нет
	ev.s.agentBin = &agentBinary{path: filepath.Join(t.TempDir(), "missing")}
	if rec := ev.do("GET", "/api/agent/binary", "", node); rec.Code != http.StatusNotFound {
		t.Fatalf("без бинарника: %d", rec.Code)
	}
	resp := ev.syncAs(token)
	if resp.Maintenance == nil || resp.Maintenance.AgentSHA256 != "" || !resp.Maintenance.GeoUpdate || resp.Maintenance.XrayVersion != "" {
		t.Fatalf("по умолчанию: геобазы обновляются, версия Xray не управляется: %+v", resp.Maintenance)
	}

	bin := filepath.Join(t.TempDir(), "agent")
	os.WriteFile(bin, []byte("agent build"), 0o755)
	ev.s.agentBin = &agentBinary{path: bin}
	sum := sha256.Sum256([]byte("agent build"))
	want := hex.EncodeToString(sum[:])

	if rec := ev.do("GET", "/api/agent/binary", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("без токена ноды бинарник не отдаём: %d", rec.Code)
	}
	rec := ev.do("GET", "/api/agent/binary", "", node)
	if rec.Code != http.StatusOK || rec.Body.String() != "agent build" || rec.Header().Get("X-Agent-SHA256") != want {
		t.Fatalf("бинарник: %d %q %q", rec.Code, rec.Body, rec.Header().Get("X-Agent-SHA256"))
	}

	if rec := ev.putSettings(`{"region":"ru","region_route":"bridge","xray_version":"v26.3.27","geo_update":false}`); rec.Code != http.StatusOK {
		t.Fatalf("настройки: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.putSettings(`{"region":"ru","region_route":"bridge","xray_version":"latest"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("неверная версия должна отклоняться: %d", rec.Code)
	}
	resp = ev.syncAs(token)
	if m := resp.Maintenance; m.AgentSHA256 != want || m.XrayVersion != "26.3.27" || m.GeoUpdate {
		t.Fatalf("maintenance: %+v", m)
	}
}

func TestSyncStoresVersions(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.2", protocol.RoleExit)
	ev.do("GET", "/api/sync", "", map[string]string{
		protocol.NodeTokenHeader:    token,
		protocol.AgentVersionHeader: "abc123",
		protocol.XrayVersionHeader:  "26.3.27",
		protocol.GeoUpdatedHeader:   "2026-09-30T03:10:00Z",
		protocol.UpdateErrorHeader:  "xray%3A%20boom",
	})
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.2")
	if n.AgentVersion != "abc123" || n.XrayVersion != "26.3.27" || n.GeoUpdated != "2026-09-30T03:10:00Z" || n.UpdateError != "xray: boom" {
		t.Fatalf("версии не сохранились: %+v", n)
	}
	// Старый агент без этих заголовков ничего не затирает
	ev.syncAs(token)
	ev.db.First(&n, "ip = ?", "10.0.0.2")
	if n.XrayVersion != "26.3.27" || n.UpdateError != "xray: boom" {
		t.Fatalf("старый агент затёр версии: %+v", n)
	}
	// Новый агент без ошибки — ошибка очищается
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token, protocol.AgentVersionHeader: "abc123"})
	ev.db.First(&n, "ip = ?", "10.0.0.2")
	if n.UpdateError != "" {
		t.Fatalf("ошибка должна очиститься: %q", n.UpdateError)
	}
	body := ev.do("GET", "/api/nodes", "", ev.adminToken()).Body.String()
	if !strings.Contains(body, `"XrayVersion":"26.3.27"`) {
		t.Fatalf("фронтенд не видит версию: %s", body)
	}
}

func TestLatestXray(t *testing.T) {
	calls := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]string{"tag_name": "v26.9.1"})
	}))
	defer gh.Close()
	defer func(u string) { xrayReleasesURL = u }(xrayReleasesURL)
	xrayReleasesURL = gh.URL

	ev := newEnv(t)
	if rec := ev.do("GET", "/api/xray/latest", "", nil); rec.Code == http.StatusOK {
		t.Fatal("только для админа")
	}
	for i := 0; i < 2; i++ {
		rec := ev.do("GET", "/api/xray/latest", "", ev.adminToken())
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"26.9.1"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	if calls != 1 {
		t.Fatalf("ответ GitHub кэшируется, запросов: %d", calls)
	}
}

func TestSubPreview(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.2", protocol.RoleBridge)
	ev.syncAs(token)
	if rec := ev.do("GET", "/api/sub-preview", "", nil); rec.Code == http.StatusOK {
		t.Fatal("только для админа")
	}
	rec := ev.do("GET", "/api/sub-preview", "", ev.adminToken())
	var got []struct {
		Name, Kind string
		Direct     bool
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || len(got) == 0 || got[0].Kind != "vless" || got[0].Direct || !strings.Contains(got[0].Name, "10.0.0.2") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCDN(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	exit := ev.addNode("10.0.0.9", protocol.RoleExit)
	ev.syncAs(bridge)
	ev.syncAs(exit)
	ev.db.Create(&models.User{ID: "u1", Name: "u1", Status: "active"})

	if rec := ev.do("PATCH", "/api/nodes/10.0.0.9", `{"cdn":"not a domain"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatalf("неверный домен: %d", rec.Code)
	}
	if rec := ev.do("PATCH", "/api/nodes/10.0.0.9", `{"cdn":"CDN.Example.com"}`, adm); rec.Code != http.StatusNoContent {
		t.Fatalf("CDN-домен: %d %s", rec.Code, rec.Body)
	}
	if ev.syncAs(exit).CDN != nil {
		t.Fatal("пока CDN выключен, входа нет")
	}
	if rec := ev.do("PUT", "/api/settings", `{"region":"ru","region_route":"bridge","exit_link":"cdn"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatal("связь через CDN без включённого CDN — ошибка")
	}
	if rec := ev.do("PUT", "/api/settings", `{"region":"ru","region_route":"bridge","cdn":true,"exit_link":"cdn","geo_update":true}`, adm); rec.Code != http.StatusOK {
		t.Fatalf("настройки: %d %s", rec.Code, rec.Body)
	}
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.9")
	if c := ev.syncAs(exit).CDN; c == nil || c.Domain != "cdn.example.com" || c.Path != n.XHTTPPath {
		t.Fatalf("экзит: вход для CDN, %+v", c)
	}
	if ev.syncAs(bridge).CDN != nil {
		t.Fatal("у моста без CDN-домена входа нет")
	}
	exits := ev.syncAs(bridge).Exits
	if len(exits) != 1 || exits[0].CDN != "cdn.example.com" || exits[0].XHTTPPath != n.XHTTPPath {
		t.Fatalf("мост ходит к экзиту через CDN: %+v", exits)
	}

	// Подписка: вариант через CDN (даже без прямых ссылок — IP экзита не виден)
	ev.do("PUT", "/api/settings", `{"region":"ru","region_route":"bridge","cdn":true,"exit_link":"cdn","direct_exit_links":false,"geo_update":true}`, adm)
	var preview []struct{ Name, Kind string }
	json.Unmarshal(ev.do("GET", "/api/sub-preview", "", adm).Body.Bytes(), &preview)
	found := false
	for _, p := range preview {
		if p.Kind == "cdn" {
			found = p.Name == "KVN CDN 10.0.0.9"
		}
		if strings.Contains(p.Name, "direct") {
			t.Fatalf("прямые ссылки выключены: %v", preview)
		}
	}
	if !found {
		t.Fatalf("в подписке нет варианта через CDN: %v", preview)
	}
	var u models.User
	ev.db.First(&u, "id = ?", "u1")
	body := ev.do("GET", "/sub/"+u.SubToken+"?format=base64", "", map[string]string{"User-Agent": "v2rayNG/1.9"}).Body.String()
	raw, _ := base64.StdEncoding.DecodeString(body)
	if !strings.Contains(string(raw), "@cdn.example.com:8443?") || !strings.Contains(string(raw), "mode=packet-up") || !strings.Contains(string(raw), "security=tls") {
		t.Fatalf("ссылка через CDN: %s", raw)
	}
	sb := ev.do("GET", "/sub/"+u.SubToken, "", map[string]string{"User-Agent": "Karing/1.2"}).Body.String()
	if !strings.Contains(sb, `"server": "cdn.example.com"`) || !strings.Contains(sb, `"packet-up"`) {
		t.Fatalf("Karing получает вариант через CDN: %s", sb)
	}
}

func TestMieru(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	exit := ev.addNode("10.0.0.9", protocol.RoleExit)
	ev.syncAs(bridge)
	ev.syncAs(exit)
	ev.db.Create(&models.User{ID: "u1", Name: "u1", Status: "active", SubToken: "tok1"})
	put := func(body string) int { return ev.do("PUT", "/api/settings", body, adm).Code }

	if ev.syncAs(bridge).Mieru != nil {
		t.Fatal("по умолчанию выключен")
	}
	for _, bad := range []string{"80", "40100-40300", "8440-8450", "abc", "40109-40100"} {
		if put(`{"region":"ru","region_route":"bridge","mieru":true,"mieru_ports":"`+bad+`"}`) != http.StatusBadRequest {
			t.Fatalf("порты %q должны отклоняться", bad)
		}
	}
	if put(`{"region":"ru","region_route":"bridge","mieru":true,"mieru_ports":"","direct_exit_links":false}`) != http.StatusOK {
		t.Fatal("пустые порты — по умолчанию")
	}
	m := ev.syncAs(bridge).Mieru
	if m == nil || m.Ports != DefaultMieruPorts || m.Version != MieruVersion || len(m.Users) != 1 || m.Users[0].Name != "u1" || m.Users[0].Password != "u1" {
		t.Fatalf("мост: %+v", m)
	}
	if ev.syncAs(exit).Mieru != nil {
		t.Fatal("без прямых ссылок mieru на экзите не нужен")
	}
	put(`{"region":"ru","region_route":"bridge","mieru":true,"mieru_ports":"40100-40109","direct_exit_links":true}`)
	if ev.syncAs(exit).Mieru == nil {
		t.Fatal("с прямыми ссылками — и на экзите")
	}

	// В подписке — только когда нода сообщила, что mita работает
	karing := func() string {
		return ev.do("GET", "/sub/tok1", "", map[string]string{"User-Agent": "Karing/1.2"}).Body.String()
	}
	if strings.Contains(karing(), `"mieru"`) {
		t.Fatal("mita ещё не запущен — серверов mieru нет")
	}
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: bridge, protocol.AgentVersionHeader: "x", protocol.MieruHeader: "3.38.0"})
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: exit, protocol.AgentVersionHeader: "x", protocol.MieruErrorHeader: "dpkg%20failed"})
	var n models.Node
	ev.db.First(&n, "ip = ?", "10.0.0.9")
	if n.MieruError != "dpkg failed" || n.MieruVersion != "" {
		t.Fatalf("ошибка mieru у ноды: %+v", n)
	}
	sb := karing()
	if !strings.Contains(sb, `"type": "mieru"`) || !strings.Contains(sb, `"server_ports": [`) || !strings.Contains(sb, `"40100-40109"`) ||
		!strings.Contains(sb, `"username": "u1"`) || !strings.Contains(sb, `"transport": "TCP"`) {
		t.Fatalf("mieru в JSON для Karing: %s", sb)
	}
	if strings.Count(sb, `"type": "mieru"`) != 1 {
		t.Fatal("только у ноды, где mita работает")
	}
	plain := ev.do("GET", "/sub/tok1", "", map[string]string{"User-Agent": "sing-box 1.14"}).Body.String()
	if strings.Contains(plain, "mieru") {
		t.Fatal("в обычном sing-box mieru нет")
	}
	raw, _ := base64.StdEncoding.DecodeString(ev.do("GET", "/sub/tok1?format=base64", "", nil).Body.String())
	var mlink string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "mierus://") {
			mlink = l
		}
	}
	u, err := url.Parse(mlink)
	if err != nil || u.Hostname() == "" || u.User.Username() != "u1" || u.Query().Get("port") != "40100-40109" ||
		u.Query().Get("protocol") != "TCP" || !strings.Contains(u.Fragment, "mieru") {
		t.Fatalf("ссылка mierus:// для mihomo и Throne: %q", mlink)
	}
	if p, _ := u.User.Password(); p != "u1" {
		t.Fatal("пароль — ключ пользователя")
	}
	var preview []struct{ Kind string }
	json.Unmarshal(ev.do("GET", "/api/sub-preview", "", adm).Body.Bytes(), &preview)
	kinds := ""
	for _, p := range preview {
		kinds += p.Kind + " "
	}
	if !strings.Contains(kinds, "mieru") {
		t.Fatalf("превью: %s", kinds)
	}
}
