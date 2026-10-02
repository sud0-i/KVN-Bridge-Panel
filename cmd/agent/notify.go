package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/telegram"
)

// Выходная нода пересылает уведомления мастера в Telegram (с моста он недоступен)
// и в следующей синхронизации сообщает, что доставлено.

var telegramClient = &http.Client{Timeout: 15 * time.Second}

func (a *agent) relayNotify(n *protocol.Notify) {
	if n == nil {
		return
	}
	for _, m := range n.Messages {
		var errs []string
		for _, chat := range n.Chats {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := telegram.Send(ctx, telegramClient, n.Token, chat, m.Text); err != nil {
				errs = append(errs, err.Error())
			}
			cancel()
		}
		ack := protocol.NotifyAck{ID: m.ID, Error: strings.Join(errs, "; ")}
		if ack.Error != "" {
			log.Printf("⚠️ Уведомление %d не доставлено: %s", m.ID, ack.Error)
		}
		a.notifyAcks = append(a.notifyAcks, ack)
	}
}

// notifyAckHeader — итоги для мастера (URL-кодированный JSON), пусто — нечего сообщать
func (a *agent) notifyAckHeader() string {
	if len(a.notifyAcks) == 0 {
		return ""
	}
	raw, _ := json.Marshal(a.notifyAcks)
	return url.QueryEscape(string(raw))
}
