// Package telegram — отправка сообщений через Bot API. Используется мастером
// (если он сам видит Telegram) и агентами выходных нод, которые пересылают
// уведомления за мост: с российского сервера api.telegram.org недоступен.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// APIBase — адрес Bot API. TELEGRAM_API задаёт свой (локальный Bot API сервер, зеркало)
var APIBase = apiBase()

func apiBase() string {
	if v := strings.TrimRight(os.Getenv("TELEGRAM_API"), "/"); v != "" {
		return v
	}
	return "https://api.telegram.org"
}

// tokenRe — токен от @BotFather: 123456789:AA… (ни слеша, ни пробела — он идёт в путь URL)
var tokenRe = regexp.MustCompile(`^[0-9]{5,15}:[A-Za-z0-9_-]{30,64}$`)

func ValidToken(t string) bool { return tokenRe.MatchString(t) }

// chatRe — числовой ID чата (группы — с минусом) или @username канала
var chatRe = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)

// ParseChats — ID чатов через запятую, пробел или с новой строки
func ParseChats(s string) ([]string, error) {
	var out []string
	for _, c := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		if !chatRe.MatchString(c) {
			return nil, fmt.Errorf("неверный ID чата: %q", c)
		}
		out = append(out, c)
	}
	return out, nil
}

// Send отправляет текст в чат. Ошибку Telegram возвращает его же словами
// («Unauthorized», «chat not found»), чтобы её было понятно в панели.
func Send(ctx context.Context, client *http.Client, token, chatID, text string) error {
	if !ValidToken(token) {
		return fmt.Errorf("неверный токен бота")
	}
	form := url.Values{"chat_id": {chatID}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBase+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		// В ошибке net/http есть URL, а в нём — токен: не отдаём его дальше
		return fmt.Errorf("Telegram недоступен: %s", strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("Telegram ответил %d", resp.StatusCode)
	}
	if !out.OK {
		return fmt.Errorf("Telegram: %s", out.Description)
	}
	return nil
}
