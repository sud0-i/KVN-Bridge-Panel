package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/telegram"
)

func TestRelayNotify(t *testing.T) {
	const token = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0"
	var chats []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("chat_id") == "404" {
			w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
			return
		}
		chats = append(chats, r.PostForm.Get("chat_id"))
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	defer func(u string) { telegram.APIBase = u }(telegram.APIBase)
	telegram.APIBase = tg.URL

	a := &agent{}
	if a.notifyAckHeader() != "" {
		t.Fatal("нечего сообщать — без заголовка")
	}
	a.relayNotify(&protocol.Notify{Token: token, Chats: []string{"1", "2"}, Messages: []protocol.NotifyMessage{{ID: 7, Text: "hi"}}})
	a.relayNotify(&protocol.Notify{Token: token, Chats: []string{"404"}, Messages: []protocol.NotifyMessage{{ID: 8, Text: "hi"}}})
	if len(chats) != 2 {
		t.Fatalf("в оба чата: %v", chats)
	}
	raw, _ := url.QueryUnescape(a.notifyAckHeader())
	var acks []protocol.NotifyAck
	json.Unmarshal([]byte(raw), &acks)
	if len(acks) != 2 || acks[0].ID != 7 || acks[0].Error != "" || acks[1].Error != "Telegram: Bad Request: chat not found" {
		t.Fatalf("итоги: %+v", acks)
	}
}
