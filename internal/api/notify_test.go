package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/telegram"
)

const testBotToken = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0"

// Тесты не ходят в настоящий Telegram: по умолчанию — закрытый порт
func init() { telegram.APIBase = "http://127.0.0.1:1" }

func (ev *env) enableNotify(extra string) {
	ev.t.Helper()
	body := `{"enabled":true,"token":"` + testBotToken + `","chats":"111, -222","nodes":true,"updates":true,"users":true` + extra + `}`
	if rec := ev.do("PUT", "/api/notify", body, ev.adminToken()); rec.Code != http.StatusOK {
		ev.t.Fatalf("настройки уведомлений: %d %s", rec.Code, rec.Body)
	}
}

func (ev *env) queued() []models.NotifyMessage {
	var msgs []models.NotifyMessage
	ev.db.Order("id").Find(&msgs)
	return msgs
}

func TestNotifySettings(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	for _, bad := range []string{
		`{"enabled":true,"chats":"111"}`, // нет токена
		`{"enabled":true,"token":"nope","chats":"111"}`,
		`{"enabled":false,"chats":"abc def"}`,
	} {
		if rec := ev.do("PUT", "/api/notify", bad, adm); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: ожидали 400, получили %d", bad, rec.Code)
		}
	}
	ev.enableNotify("")
	body := ev.do("GET", "/api/notify", "", adm).Body.String()
	if strings.Contains(body, testBotToken) || !strings.Contains(body, `"has_token":true`) || !strings.Contains(body, `"111, -222"`) {
		t.Fatalf("токен не должен уходить в панель: %s", body)
	}
	// Пустой токен при сохранении — прежний остаётся
	ev.do("PUT", "/api/notify", `{"enabled":true,"chats":"111"}`, adm)
	if ev.s.loadNotify().Token != testBotToken {
		t.Fatal("токен потерялся")
	}
	if rec := ev.do("GET", "/api/notify", "", nil); rec.Code == http.StatusOK {
		t.Fatal("только для админа")
	}
}

func TestNotifyEvents(t *testing.T) {
	ev := newEnv(t)
	token := ev.addNode("10.0.0.2", protocol.RoleBridge)
	ev.addNode("10.0.0.9", protocol.RoleExit) // ещё не выходила на связь — не тревожим
	ev.syncAs(token)
	ev.enableNotify("")

	ev.s.notifyTick()
	if len(ev.queued()) != 0 {
		t.Fatalf("всё в порядке — сообщать нечего: %+v", ev.queued())
	}

	// Нода пропала
	ev.now = ev.now.Add(NodeAliveTimeout + time.Minute)
	ev.s.notifyTick()
	ev.s.notifyTick() // повторно не сообщаем
	msgs := ev.queued()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "10.0.0.2 не на связи") {
		t.Fatalf("одно сообщение о пропаже: %+v", msgs)
	}
	if !strings.HasPrefix(msgs[0].Text, "KVN\n") {
		t.Fatalf("в начале — название профиля: %q", msgs[0].Text)
	}

	// Вернулась, но с ошибкой конфига
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token, protocol.ConfigErrorHeader: "bad%20rule"})
	ev.s.notifyTick()
	msgs = ev.queued()
	if len(msgs) != 2 || !strings.Contains(msgs[1].Text, "снова на связи") || !strings.Contains(msgs[1].Text, "конфиг не применён — bad rule") {
		t.Fatalf("возврат и ошибка — одним сообщением: %+v", msgs[1:])
	}

	// Пользователь у лимита: сообщаем один раз
	ev.db.Create(&models.User{ID: "u1", Name: "dima", Status: "active", TrafficQuota: 100, TrafficDown: 95})
	ev.s.notifyTick()
	ev.s.notifyTick()
	msgs = ev.queued()
	if len(msgs) != 3 || !strings.Contains(msgs[2].Text, "dima: израсходовано 95%") {
		t.Fatalf("лимит: %+v", msgs[2:])
	}

	// Выключили категорию — о ней молчим
	ev.do("PUT", "/api/notify", `{"enabled":true,"chats":"111","nodes":true,"updates":true,"users":false}`, ev.adminToken())
	ev.db.Create(&models.User{ID: "u2", Name: "anya", Status: "active", TrafficQuota: 100, TrafficDown: 99})
	ev.s.notifyTick()
	if len(ev.queued()) != 3 {
		t.Fatal("пользователи выключены — не сообщаем")
	}
}

func TestNotifyRelayThroughExit(t *testing.T) {
	ev := newEnv(t)
	exitToken := ev.addNode("10.0.0.9", protocol.RoleExit)
	ev.syncAs(exitToken)
	ev.enableNotify("")
	ev.s.enqueue(ev.s.loadNotify(), "hello")

	resp := ev.syncAs(exitToken)
	if resp.Notify == nil || resp.Notify.Token != testBotToken || len(resp.Notify.Chats) != 2 || len(resp.Notify.Messages) != 1 {
		t.Fatalf("выходная нода должна получить сообщение: %+v", resp.Notify)
	}
	id := resp.Notify.Messages[0].ID
	if again := ev.syncAs(exitToken); again.Notify != nil {
		t.Fatal("отданное сообщение не отдаём повторно сразу")
	}

	// Ошибка — отдаём снова
	ack := func(a protocol.NotifyAck) protocol.SyncResponse {
		raw, _ := json.Marshal([]protocol.NotifyAck{a})
		rec := ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: exitToken, protocol.NotifyAckHeader: url.QueryEscape(string(raw))})
		var r protocol.SyncResponse
		json.Unmarshal(rec.Body.Bytes(), &r)
		return r
	}
	r := ack(protocol.NotifyAck{ID: id, Error: "Telegram: Bad Request: chat not found"})
	if r.Notify == nil || r.Notify.Messages[0].ID != id {
		t.Fatal("после ошибки — повтор")
	}
	ack(protocol.NotifyAck{ID: id})
	msgs := ev.queued()
	if msgs[0].SentAt == nil || msgs[0].Error != "" {
		t.Fatalf("доставлено: %+v", msgs[0])
	}
	if st := ev.s.notifyStatus(); st.Pending != 0 || st.Last == nil || st.Last.Via != "10.0.0.9" {
		t.Fatalf("статус: %+v", st)
	}

	// Не подтвердили — через notifyRetry отдаём снова; после notifyMaxAttempts — недоставлено
	ev.s.enqueue(ev.s.loadNotify(), "lost")
	for i := 0; i < notifyMaxAttempts; i++ {
		if r := ev.syncAs(exitToken); r.Notify == nil {
			t.Fatalf("попытка %d: сообщение должно уйти снова", i+1)
		}
		ev.now = ev.now.Add(notifyRetry + time.Second)
	}
	ev.syncAs(exitToken)
	var lost models.NotifyMessage
	ev.db.Where("text LIKE ?", "%lost").First(&lost)
	if !lost.Failed {
		t.Fatalf("после %d попыток — недоставлено: %+v", notifyMaxAttempts, lost)
	}
}

func TestNotifyDirectWithoutExits(t *testing.T) {
	var mu sync.Mutex
	var got []url.Values
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testBotToken+"/sendMessage" {
			w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
			return
		}
		r.ParseForm()
		mu.Lock()
		got = append(got, r.PostForm)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	defer func(u string) { telegram.APIBase = u }(telegram.APIBase)
	telegram.APIBase = tg.URL

	ev := newEnv(t)
	ev.syncAs(ev.addNode("10.0.0.2", protocol.RoleBridge))
	ev.enableNotify("")
	if rec := ev.do("POST", "/api/notify/test", "", ev.adminToken()); rec.Code != http.StatusOK {
		t.Fatalf("тест: %d %s", rec.Code, rec.Body)
	}
	ev.s.deliverDirect() // дожидаемся отправки (тест запускает её в фоне)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Get("chat_id") != "111" || got[1].Get("chat_id") != "-222" || !strings.Contains(got[0].Get("text"), "Тестовое сообщение") {
		t.Fatalf("без выходных нод мастер отправляет сам во все чаты: %+v", got)
	}
	if m := ev.queued()[0]; m.SentAt == nil || m.AssignedTo != notifyMaster {
		t.Fatalf("отмечено доставленным мастером: %+v", m)
	}
}
