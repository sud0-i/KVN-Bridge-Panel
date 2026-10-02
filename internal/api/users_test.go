package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func (ev *env) user(name string) models.User {
	ev.t.Helper()
	var u models.User
	if err := ev.db.First(&u, "name = ?", name).Error; err != nil {
		ev.t.Fatalf("пользователь %s: %v", name, err)
	}
	return u
}

func TestCreateUserGetsSubToken(t *testing.T) {
	ev := newEnv(t)
	for _, name := range []string{"alice", "bob"} {
		if rec := ev.do("POST", "/api/users", `{"name":"`+name+`"}`, ev.adminToken()); rec.Code != http.StatusCreated {
			t.Fatalf("создание: %d %s", rec.Code, rec.Body)
		}
	}
	a, b := ev.user("alice"), ev.user("bob")
	if len(a.SubToken) != 32 || a.SubToken == b.SubToken || a.SubToken == a.ID {
		t.Fatalf("токены должны быть случайными и не совпадать с ID: %q %q", a.SubToken, b.SubToken)
	}

	// Свой токен (перенос одной ссылки) и проверка формата
	if rec := ev.do("POST", "/api/users", `{"name":"carol","sub_token":"oldtoken123"}`, ev.adminToken()); rec.Code != http.StatusCreated {
		t.Fatalf("свой токен: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("POST", "/api/users", `{"name":"dave","sub_token":"oldtoken123"}`, ev.adminToken()); rec.Code != http.StatusConflict {
		t.Fatalf("занятый токен: ожидали 409, получили %d", rec.Code)
	}
	if rec := ev.do("POST", "/api/users", `{"name":"eve","sub_token":"bad/../token"}`, ev.adminToken()); rec.Code != http.StatusBadRequest {
		t.Fatalf("плохой токен: ожидали 400, получили %d", rec.Code)
	}
}

func TestImportUsers(t *testing.T) {
	ev := newEnv(t)
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.syncAs(bridge)

	text := "# перенос из старой панели\n" +
		"alice 57d2bfa5b82efcc2d127dd64f5dce8c7\n" +
		"\n" +
		"bob, https://cv.example.com/sub/aaaabbbbccccdddd/, 50, 2026-03-01\n" +
		"carol\tcccccccccccccccc\t2026-02-01\n"
	body, _ := json.Marshal(map[string]string{"text": text})
	rec := ev.do("POST", "/api/users/import", string(body), ev.adminToken())
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"created":3`) {
		t.Fatalf("импорт: %d %s", rec.Code, rec.Body)
	}

	bob := ev.user("bob")
	if bob.SubToken != "aaaabbbbccccdddd" || bob.TrafficQuota != 50<<30 || bob.IPLimit != defaultIPLimit {
		t.Fatalf("bob импортирован неверно: %+v", bob)
	}
	if bob.ExpiresAt == nil || !bob.ExpiresAt.Equal(time.Date(2026, 3, 1, 23, 59, 59, 0, time.UTC)) {
		t.Fatalf("срок должен быть до конца дня: %v", bob.ExpiresAt)
	}
	if c := ev.user("carol"); c.TrafficQuota != 0 || c.ExpiresAt == nil {
		t.Fatalf("carol: только срок, без квоты: %+v", c)
	}

	// Старая ссылка отдаёт новую подписку
	sub := ev.do("GET", "/sub/57d2bfa5b82efcc2d127dd64f5dce8c7", "", map[string]string{"User-Agent": "Happ/1.0"})
	if sub.Code != http.StatusOK || sub.Body.Len() == 0 {
		t.Fatalf("старая ссылка не работает: %d", sub.Code)
	}

	// Всё или ничего: одна плохая строка — никто не создан
	bad, _ := json.Marshal(map[string]string{"text": "dave dddddddddddddddd\nalice eeeeeeeeeeeeeeee\nfrank short\ngreg gggggggggggggggg lots"})
	rec = ev.do("POST", "/api/users/import", string(bad), ev.adminToken())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ожидали 400, получили %d", rec.Code)
	}
	var out struct{ Problems []string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Problems) != 3 {
		t.Fatalf("ожидали 3 проблемы (занятое имя, короткий токен, не квота), получили %v", out.Problems)
	}
	var count int64
	ev.db.Model(&models.User{}).Where("name = ?", "dave").Count(&count)
	if count != 0 {
		t.Fatal("при ошибке импорт не должен создавать никого")
	}

	dup, _ := json.Marshal(map[string]string{"text": "x1 tttttttttttttttt\nx2 tttttttttttttttt"})
	if rec := ev.do("POST", "/api/users/import", string(dup), ev.adminToken()); rec.Code != http.StatusBadRequest {
		t.Fatalf("повтор токена внутри списка: ожидали 400, получили %d", rec.Code)
	}
}

func TestSubscriptionByTokenAndHeaders(t *testing.T) {
	ev := newEnv(t)
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	ev.syncAs(bridge)
	exp := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ev.db.Create(&models.User{ID: "u1", Name: "alice", SubToken: "tok12345", Status: "active",
		TrafficUp: 10, TrafficDown: 20, TrafficQuota: 1000, ExpiresAt: &exp})

	rec := ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "Hiddify"})
	if rec.Code != http.StatusOK {
		t.Fatalf("по токену: %d", rec.Code)
	}
	if got := rec.Header().Get("Subscription-Userinfo"); got != "upload=10; download=20; total=1000; expire=1780272000" {
		t.Fatalf("неверный subscription-userinfo: %q", got)
	}
	if rec.Header().Get("Profile-Title") != "base64:S1ZO" || rec.Header().Get("Profile-Update-Interval") != "12" {
		t.Fatalf("нет заголовков профиля: %v", rec.Header())
	}

	// Ссылки, выданные до токенов (по UUID), продолжают работать
	if rec := ev.do("GET", "/sub/u1", "", map[string]string{"User-Agent": "Hiddify"}); rec.Code != http.StatusOK {
		t.Fatalf("по UUID: %d", rec.Code)
	}
	if rec := ev.do("GET", "/sub/nope1234", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("неизвестный токен: %d", rec.Code)
	}

	// Выбравшему квоту — 403, но заголовки с трафиком всё равно отдаём: клиент покажет почему
	ev.db.Model(&models.User{}).Where("id = ?", "u1").Update("traffic_down", 5000)
	rec = ev.do("GET", "/sub/tok12345", "", map[string]string{"User-Agent": "Hiddify"})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Header().Get("Subscription-Userinfo"), "download=5000") {
		t.Fatalf("квота выбрана: %d %q", rec.Code, rec.Header().Get("Subscription-Userinfo"))
	}
}

func TestUpdateResetRotateUser(t *testing.T) {
	ev := newEnv(t)
	ev.db.Create(&models.User{ID: "u1", Name: "alice", SubToken: "tok12345", Status: "active", TrafficUp: 5, TrafficDown: 7})
	ev.db.Create(&models.User{ID: "u2", Name: "bob", Status: "active"})
	admin := ev.adminToken()

	rec := ev.do("PATCH", "/api/users/u1", `{"name":"alice2","ip_limit":3,"traffic_quota":1024,"expires_at":"2026-05-01T00:00:00Z"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("правка: %d %s", rec.Code, rec.Body)
	}
	u := ev.user("alice2")
	if u.IPLimit != 3 || u.TrafficQuota != 1024 || u.ExpiresAt == nil {
		t.Fatalf("правка не сохранилась: %+v", u)
	}
	ev.do("PATCH", "/api/users/u1", `{"name":"alice2","ip_limit":3,"traffic_quota":0,"expires_at":null}`, admin)
	if u := ev.user("alice2"); u.ExpiresAt != nil || u.TrafficQuota != 0 {
		t.Fatalf("null должен снимать срок: %+v", u)
	}
	if rec := ev.do("PATCH", "/api/users/u1", `{"name":"bob"}`, admin); rec.Code != http.StatusConflict {
		t.Fatalf("занятое имя: ожидали 409, получили %d", rec.Code)
	}
	if rec := ev.do("PATCH", "/api/users/zzz", `{"name":"x"}`, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("несуществующий: ожидали 404, получили %d", rec.Code)
	}

	ev.do("POST", "/api/users/u1/reset-traffic", "", admin)
	if u := ev.user("alice2"); u.TrafficUp != 0 || u.TrafficDown != 0 {
		t.Fatalf("трафик не сброшен: %+v", u)
	}

	ev.do("POST", "/api/users/u1/rotate", `{"what":"link"}`, admin)
	if u := ev.user("alice2"); u.SubToken == "tok12345" || u.ID != "u1" {
		t.Fatalf("новая ссылка: токен должен смениться, ключ — нет: %+v", u)
	}
	ev.do("POST", "/api/users/u1/rotate", `{"what":"key"}`, admin)
	if u := ev.user("alice2"); u.ID == "u1" {
		t.Fatal("новый ключ: ID должен смениться")
	}
	if rec := ev.do("POST", "/api/users/u2/rotate", `{"what":"all"}`, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("неизвестный what: %d", rec.Code)
	}
}

// Базы, созданные до токенов подписки: колонка добавляется, токены выдаются при старте
func TestEnsureSubTokensOnOldDB(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "old.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	gdb.Exec(`CREATE TABLE users (id uuid PRIMARY KEY, name text NOT NULL UNIQUE, traffic_up integer DEFAULT 0,
		traffic_down integer DEFAULT 0, traffic_quota integer DEFAULT 0, ip_limit integer DEFAULT 5,
		expires_at datetime, status text DEFAULT 'active', created_at datetime, updated_at datetime)`)
	gdb.Exec(`INSERT INTO users (id, name) VALUES ('u1', 'a'), ('u2', 'b')`)

	if err := gdb.AutoMigrate(&models.User{}); err != nil {
		t.Fatalf("миграция старой базы: %v", err)
	}
	if err := EnsureSubTokens(gdb); err != nil {
		t.Fatal(err)
	}
	var users []models.User
	gdb.Find(&users)
	if len(users) != 2 || users[0].SubToken == "" || users[0].SubToken == users[1].SubToken {
		t.Fatalf("токены не выданы: %+v", users)
	}
}
