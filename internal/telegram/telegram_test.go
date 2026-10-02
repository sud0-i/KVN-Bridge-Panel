package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestParseChats(t *testing.T) {
	got, err := ParseChats(" 111, -100222\n@my_channel ")
	if err != nil || len(got) != 3 || got[1] != "-100222" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ParseChats("111, abc"); err == nil {
		t.Fatal("мусор — ошибка")
	}
}

func TestSendHidesToken(t *testing.T) {
	const token = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0"
	defer func(u string) { APIBase = u }(APIBase)
	APIBase = "http://127.0.0.1:1"
	err := Send(context.Background(), http.DefaultClient, token, "1", "x")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("токен не должен попадать в ошибку (её видно в панели): %v", err)
	}
	if err := Send(context.Background(), http.DefaultClient, "bad/token", "1", "x"); err == nil {
		t.Fatal("неверный токен")
	}
}
