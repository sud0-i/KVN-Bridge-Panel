package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTOTPCodeRFC6238(t *testing.T) {
	// RFC 6238, приложение B: секрет "12345678901234567890", SHA1 (последние 6 цифр)
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpCode(secret, ts/30); got != want {
			t.Fatalf("t=%d: %s, ожидали %s", ts, got, want)
		}
	}
}

func TestTOTPLogin(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	totpUsed = 0
	code := func(secret string, shift time.Duration) string {
		return totpCode(secret, ev.now.Add(shift).Unix()/30)
	}
	ip := 0
	login := func(body string) (int, map[string]any) {
		ip++ // лимит попыток входа — по IP, а тест быстрый
		rec := ev.do("POST", "/api/login", body, map[string]string{"X-Real-IP": fmt.Sprintf("198.51.100.%d", ip)})
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if rec := ev.do("POST", "/api/2fa/enable", `{"code":"123456"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatal("без QR включить нельзя")
	}
	var setup map[string]string
	json.Unmarshal(ev.do("POST", "/api/2fa/setup", "", adm).Body.Bytes(), &setup)
	secret := setup["secret"]
	if len(secret) != 32 || !strings.HasPrefix(setup["uri"], "otpauth://totp/KVN:panel.example.com?secret="+secret) {
		t.Fatalf("setup: %v", setup)
	}
	// Пока не подтверждено — вход по паролю как раньше
	if c, _ := login(`{"password":"secret"}`); c != http.StatusOK {
		t.Fatal("до подтверждения 2FA не действует")
	}
	if rec := ev.do("POST", "/api/2fa/enable", `{"code":"000000"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatal("неверный код подтверждения")
	}
	if rec := ev.do("POST", "/api/2fa/enable", `{"code":"`+code(secret, 5*time.Minute)+`"}`, adm); !strings.Contains(rec.Body.String(), "Часы сервера расходятся") {
		t.Fatalf("сбитые часы: %s", rec.Body)
	}
	rec := ev.do("POST", "/api/2fa/enable", `{"code":"`+code(secret, 0)+`"}`, adm)
	var en struct{ Recovery []string }
	json.Unmarshal(rec.Body.Bytes(), &en)
	if rec.Code != http.StatusOK || len(en.Recovery) != recoveryN {
		t.Fatalf("включение: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(ev.do("GET", "/api/2fa", "", adm).Body.String(), secret) {
		t.Fatal("секрет в панель не отдаём")
	}

	// Вход: пароль без кода → просим код
	if c, out := login(`{"password":"secret"}`); c != http.StatusUnauthorized || out["need_code"] != true {
		t.Fatalf("без кода: %d %v", c, out)
	}
	if c, out := login(`{"password":"wrong","code":"` + code(secret, 0) + `"}`); c != http.StatusUnauthorized || out["need_code"] != nil {
		t.Fatal("неверный пароль — без подсказки про код")
	}
	ev.now = ev.now.Add(time.Minute)
	if c, _ := login(`{"password":"secret","code":"` + code(secret, 0) + `"}`); c != http.StatusOK {
		t.Fatal("верный код")
	}
	if c, out := login(`{"password":"secret","code":"` + code(secret, 0) + `"}`); c != http.StatusUnauthorized || !strings.Contains(out["error"].(string), "уже использован") {
		t.Fatalf("повтор того же кода: %v", out)
	}
	ev.now = ev.now.Add(time.Minute)
	if c, _ := login(`{"password":"secret","code":"` + code(secret, -30*time.Second) + `"}`); c != http.StatusOK {
		t.Fatal("допуск ±30 с")
	}
	// Резервный код — один раз
	if c, _ := login(`{"password":"secret","code":"` + strings.ToUpper(en.Recovery[0]) + `"}`); c != http.StatusOK {
		t.Fatal("резервный код")
	}
	if c, _ := login(`{"password":"secret","code":"` + en.Recovery[0] + `"}`); c != http.StatusUnauthorized {
		t.Fatal("резервный код второй раз не принимается")
	}
	if !strings.Contains(ev.do("GET", "/api/2fa", "", adm).Body.String(), `"recovery_left":7`) {
		t.Fatal("осталось 7 резервных кодов")
	}

	// Выключение — только с кодом; аварийный сброс
	if rec := ev.do("POST", "/api/2fa/disable", `{"code":"000000"}`, adm); rec.Code != http.StatusBadRequest {
		t.Fatal("выключение без верного кода")
	}
	if err := ResetTOTP(ev.db); err != nil {
		t.Fatal(err)
	}
	if c, _ := login(`{"password":"secret"}`); c != http.StatusOK {
		t.Fatal("после сброса — снова по паролю")
	}
}
