package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/telegram"
)

// Уведомления в Telegram. Раз в минуту мастер сравнивает текущие проблемы с теми,
// о которых уже сообщил, и ставит новые (и исправленные) в очередь. Отправляет их
// выходная нода при синхронизации: с российского моста Telegram недоступен.
// Если живых выходных нод нет (одиночный сервер), мастер отправляет сам.

const (
	notifyKey      = "notify"
	notifyStateKey = "notify_state"
	// notifyRetry — через сколько неподтверждённое сообщение отдаём другой ноде
	notifyRetry = 3 * time.Minute
	// notifyMaxAttempts — после стольких попыток сообщение считается недоставленным
	notifyMaxAttempts = 5
	// notifyBatch — сколько сообщений отдаём ноде за одну синхронизацию
	notifyBatch = 10
	// notifyStartDelay — после запуска мастера ждём, пока ноды выйдут на связь,
	// иначе после простоя мастера все ноды выглядели бы упавшими
	notifyStartDelay = 2 * time.Minute
	notifyMaster     = "master"
)

// NotifySettings — что и куда сообщать
type NotifySettings struct {
	Enabled bool   `json:"enabled"`
	Chats   string `json:"chats"`   // ID чатов через запятую
	Nodes   bool   `json:"nodes"`   // нода пропала/вернулась, ошибки конфига, маскировки, WARP
	Updates bool   `json:"updates"` // ошибки обновления агента, Xray, геобаз
	Users   bool   `json:"users"`   // лимит трафика, окончание подписки
	Lang    string `json:"lang"`    // ru | en
	// Token — токен бота; в панель не отдаём
	Token string `json:"-"`
}

type notifyStored struct {
	NotifySettings
	Token string `json:"token"`
}

func (s *Server) loadNotify() NotifySettings {
	n := NotifySettings{Nodes: true, Updates: true, Users: true, Lang: "ru"}
	var st models.Setting
	if err := s.db.First(&st, "key = ?", notifyKey).Error; err == nil {
		stored := notifyStored{NotifySettings: n}
		if json.Unmarshal([]byte(st.Value), &stored) == nil {
			n = stored.NotifySettings
			n.Token = stored.Token
		}
	}
	return n
}

func (n NotifySettings) active() bool { return n.Enabled && n.Token != "" && n.Chats != "" }

// ---------- API панели ----------

type notifyStatus struct {
	Pending int        `json:"pending"`
	Last    *lastNotif `json:"last,omitempty"`
}

type lastNotif struct {
	Text      string     `json:"text"`
	CreatedAt time.Time  `json:"created_at"`
	SentAt    *time.Time `json:"sent_at"`
	Failed    bool       `json:"failed"`
	Error     string     `json:"error"`
	Via       string     `json:"via"`
}

func (s *Server) notifyStatus() notifyStatus {
	var st notifyStatus
	var pending int64
	s.db.Model(&models.NotifyMessage{}).Where("sent_at IS NULL AND failed = ?", false).Count(&pending)
	st.Pending = int(pending)
	var m models.NotifyMessage
	if s.db.Order("id desc").First(&m).Error == nil {
		st.Last = &lastNotif{Text: m.Text, CreatedAt: m.CreatedAt, SentAt: m.SentAt, Failed: m.Failed, Error: m.Error, Via: m.AssignedTo}
	}
	return st
}

func (s *Server) getNotify(c echo.Context) error {
	n := s.loadNotify()
	return c.JSON(http.StatusOK, map[string]any{"settings": n, "has_token": n.Token != "", "status": s.notifyStatus()})
}

type notifyRequest struct {
	NotifySettings
	Token      string `json:"token"`       // новый токен; пусто — оставить прежний
	ClearToken bool   `json:"clear_token"` // забыть токен
}

func (s *Server) saveNotify(c echo.Context) error {
	var req notifyRequest
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	n := req.NotifySettings
	n.Token = s.loadNotify().Token
	switch token := strings.TrimSpace(req.Token); {
	case req.ClearToken:
		n.Token = ""
	case token != "":
		if !telegram.ValidToken(token) {
			return jsonError(c, http.StatusBadRequest, "Токен бота выглядит как 123456789:AA… — его выдаёт @BotFather")
		}
		n.Token = token
	}
	chats, err := telegram.ParseChats(n.Chats)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	n.Chats = strings.Join(chats, ", ")
	if n.Lang != "en" {
		n.Lang = "ru"
	}
	if n.Enabled && (n.Token == "" || len(chats) == 0) {
		return jsonError(c, http.StatusBadRequest, "Для уведомлений нужны токен бота и хотя бы один чат")
	}
	raw, _ := json.Marshal(notifyStored{NotifySettings: n, Token: n.Token})
	if err := s.db.Save(&models.Setting{Key: notifyKey, Value: string(raw)}).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, map[string]any{"settings": n, "has_token": n.Token != "", "status": s.notifyStatus()})
}

// testNotify ставит в очередь пробное сообщение
func (s *Server) testNotify(c echo.Context) error {
	n := s.loadNotify()
	if !n.active() {
		return jsonError(c, http.StatusBadRequest, "Сначала сохраните токен бота и чат и включите уведомления")
	}
	s.enqueue(n, notifyText(n.Lang, "test"))
	go s.deliverDirect()
	return c.JSON(http.StatusOK, s.notifyStatus())
}

// ---------- События ----------

var notifyTexts = map[string]map[string]string{
	"ru": {
		"test":        "✅ Тестовое сообщение: уведомления работают",
		"terminal":    "🖥 Вход в терминал %s из панели (с %s)",
		"offline":     "🔴 %s не на связи (последний раз %s)",
		"online":      "🟢 %s снова на связи",
		"config":      "⚠️ %s: конфиг не применён — %s",
		"sni":         "⚠️ %s: сайт маскировки недоступен — %s",
		"warp":        "⚠️ %s: WARP не работает — %s",
		"fixed":       "✅ %s: исправлено",
		"update":      "⚠️ %s: ошибка обновления — %s",
		"updated":     "✅ %s: обновление прошло",
		"quota90":     "📊 %s: израсходовано %d%% трафика (%s из %s ГБ)",
		"quota100":    "⛔️ %s: трафик закончился (%s ГБ) — подключение отключено",
		"expires":     "⏳ %s: подписка заканчивается %s",
		"expired":     "⛔️ %s: подписка закончилась %s",
		"linkBad":     "📉 %s: потери %.0f%% за 10 минут, задержка %.0f мс",
		"linkOK":      "📈 %s: канал восстановился",
		"disk":        "💾 %s: диск заполнен на %.0f%%",
		"mem":         "🧠 %s: память занята на %.0f%%",
		"cpu":         "🔥 %s: процессор загружен на %.0f%% (10 минут)",
		"steal":       "🐢 %s: соседи по серверу забирают %.0f%% процессора (steal) — хостер перегружен",
		"retrans":     "📉 %s: %.1f%% TCP-пакетов уходят повторно — потери в сети хостера",
		"diskName":    "диск",
		"memName":     "память",
		"cpuName":     "процессор",
		"stealName":   "steal",
		"retransName": "сеть",
		"metricOK":    "✅ %s: %s в норме",
	},
	"en": {
		"test":        "✅ Test message: notifications work",
		"offline":     "🔴 %s is unreachable (last seen %s)",
		"online":      "🟢 %s is back online",
		"config":      "⚠️ %s: config not applied — %s",
		"sni":         "⚠️ %s: camouflage site unreachable — %s",
		"warp":        "⚠️ %s: WARP is down — %s",
		"fixed":       "✅ %s: fixed",
		"update":      "⚠️ %s: update failed — %s",
		"updated":     "✅ %s: update succeeded",
		"quota90":     "📊 %s: %d%% of traffic used (%s of %s GB)",
		"quota100":    "⛔️ %s: traffic used up (%s GB) — access disabled",
		"expires":     "⏳ %s: subscription ends %s",
		"expired":     "⛔️ %s: subscription ended %s",
		"linkBad":     "📉 %s: %.0f%% packet loss over 10 minutes, latency %.0f ms",
		"linkOK":      "📈 %s: link recovered",
		"disk":        "💾 %s: disk %.0f%% full",
		"mem":         "🧠 %s: memory %.0f%% used",
		"cpu":         "🔥 %s: CPU at %.0f%% (10 minutes)",
		"steal":       "🐢 %s: neighbours take %.0f%% of CPU (steal) — the host is overloaded",
		"retrans":     "📉 %s: %.1f%% of TCP packets are retransmitted — loss in the hoster's network",
		"diskName":    "disk",
		"memName":     "memory",
		"cpuName":     "CPU",
		"stealName":   "steal",
		"retransName": "network",
		"metricOK":    "✅ %s: %s back to normal",
		"terminal":    "🖥 Terminal opened on %s from the panel (from %s)",
	},
}

func notifyText(lang, key string, args ...any) string {
	t, ok := notifyTexts[lang]
	if !ok {
		t = notifyTexts["ru"]
	}
	return fmt.Sprintf(t[key], args...)
}

// alert — текущая проблема и что сказать, когда она уйдёт (пусто — ничего)
type alert struct {
	Text    string
	Resolve string
}

// nodeName — нода в уведомлениях: «подпись (IP)» или просто IP
func nodeName(nd models.Node) string {
	if nd.Label != "" {
		return nd.Label + " (" + nd.IP + ")"
	}
	return nd.IP
}

func gbStr(b int64) string { return fmt.Sprintf("%.1f", float64(b)/1073741824) }

// alerts — всё, о чём сейчас стоит сообщить; ключ определяет, сообщали ли уже
func (s *Server) alerts(n NotifySettings) (map[string]alert, error) {
	out := map[string]alert{}
	now := s.now()
	lang := n.Lang
	tm := func(t time.Time) string { return t.UTC().Format("02.01 15:04 UTC") }

	if n.Nodes || n.Updates {
		var nodes []models.Node
		if err := s.db.Find(&nodes).Error; err != nil {
			return nil, err
		}
		for _, nd := range nodes {
			name := nodeName(nd)
			// Нода, ещё ни разу не выходившая на связь, — это идущая установка, а не авария
			if nd.LastSeen.IsZero() {
				continue
			}
			alive := s.isAlive(nd)
			if n.Nodes {
				if !alive {
					out["offline:"+nd.IP] = alert{notifyText(lang, "offline", name, tm(nd.LastSeen)), notifyText(lang, "online", name)}
					continue // остальное состояние у упавшей ноды устарело
				}
				if nd.ConfigError != "" {
					out["config:"+nd.IP] = alert{notifyText(lang, "config", name, nd.ConfigError), notifyText(lang, "fixed", name)}
				}
				if nd.SNIError != "" {
					out["sni:"+nd.IP] = alert{notifyText(lang, "sni", name, nd.SNIError), notifyText(lang, "fixed", name)}
				}
				if nd.WarpError != "" {
					out["warp:"+nd.IP] = alert{notifyText(lang, "warp", name, nd.WarpError), notifyText(lang, "fixed", name)}
				}
			}
			if n.Updates && alive && nd.UpdateError != "" {
				out["update:"+nd.IP] = alert{notifyText(lang, "update", name, nd.UpdateError), notifyText(lang, "updated", name)}
			}
		}
	}

	// Канал мост → выходная нода: большие потери за последние минуты
	if n.Nodes {
		var nodes []models.Node
		s.db.Find(&nodes)
		names := map[string]string{}
		for _, nd := range nodes {
			names[nd.IP] = nd.Label
		}
		for k, a := range s.linkAlerts(lang, names) {
			out[k] = a
		}
		for k, a := range s.metricAlerts(lang, names) {
			out[k] = a
		}
	}

	if n.Users {
		var users []models.User
		if err := s.db.Where("status = ?", "active").Find(&users).Error; err != nil {
			return nil, err
		}
		for _, u := range users {
			used := u.TrafficUp + u.TrafficDown
			if u.TrafficQuota > 0 {
				switch pct := used * 100 / u.TrafficQuota; {
				case pct >= 100:
					out["quota100:"+u.ID] = alert{Text: notifyText(lang, "quota100", u.Name, gbStr(u.TrafficQuota))}
				case pct >= 90:
					out["quota90:"+u.ID] = alert{Text: notifyText(lang, "quota90", u.Name, pct, gbStr(used), gbStr(u.TrafficQuota))}
				}
			}
			if u.ExpiresAt != nil {
				date := u.ExpiresAt.UTC().Format("02.01.2006")
				switch left := u.ExpiresAt.Sub(now); {
				case left <= 0:
					out["expired:"+u.ID] = alert{Text: notifyText(lang, "expired", u.Name, date)}
				case left < 3*24*time.Hour:
					out["expires:"+u.ID] = alert{Text: notifyText(lang, "expires", u.Name, date)}
				}
			}
		}
	}
	return out, nil
}

// notifyTick — раз в минуту: новые проблемы и исправленные — в очередь, затем доставка
func (s *Server) notifyTick() {
	n := s.loadNotify()
	if !n.active() {
		return
	}
	cur, err := s.alerts(n)
	if err != nil {
		log.Printf("⚠️ Уведомления: %v", err)
		return
	}
	prev := map[string]string{} // ключ → что сказать при исправлении
	var st models.Setting
	if s.db.First(&st, "key = ?", notifyStateKey).Error == nil {
		_ = json.Unmarshal([]byte(st.Value), &prev)
	}

	var lines []string
	keys := make([]string, 0, len(cur))
	for k := range cur {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, seen := prev[k]; !seen {
			lines = append(lines, cur[k].Text)
		}
	}
	gone := make([]string, 0)
	for k := range prev {
		if _, still := cur[k]; !still && prev[k] != "" {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	for _, k := range gone {
		lines = append(lines, prev[k])
	}

	state := map[string]string{}
	for k, a := range cur {
		state[k] = a.Resolve
	}
	raw, _ := json.Marshal(state)
	s.db.Save(&models.Setting{Key: notifyStateKey, Value: string(raw)})

	if len(lines) > 0 {
		s.enqueue(n, strings.Join(lines, "\n"))
	}
	s.deliverDirect()
	// Старые отправленные сообщения не копим
	s.db.Where("created_at < ?", s.now().Add(-30*24*time.Hour)).Delete(&models.NotifyMessage{})
}

// enqueue ставит сообщение в очередь; в начале — название профиля, чтобы отличать панели
func (s *Server) enqueue(n NotifySettings, text string) {
	if title := s.loadPage().Title; title != "" {
		text = title + "\n" + text
	}
	s.db.Create(&models.NotifyMessage{Text: text, CreatedAt: s.now()})
}

// RunNotifier — фоновая проверка событий; первая — когда ноды успеют выйти на связь
func (s *Server) RunNotifier(ctx context.Context) {
	timer := time.NewTimer(notifyStartDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.cleanupLinks()
			s.cleanupMetrics()
			s.notifyTick()
			timer.Reset(time.Minute)
		}
	}
}

// ---------- Доставка ----------

// takeMessages — сообщения, которые можно отдать отправителю via, помеченные за ним
func (s *Server) takeMessages(via string) []models.NotifyMessage {
	now := s.now()
	// Слишком много попыток — больше не пытаемся
	s.db.Model(&models.NotifyMessage{}).
		Where("sent_at IS NULL AND failed = ? AND attempts >= ?", false, notifyMaxAttempts).
		Updates(map[string]any{"failed": true})
	var msgs []models.NotifyMessage
	s.db.Where("sent_at IS NULL AND failed = ? AND (assigned_to = '' OR assigned_at < ?)", false, now.Add(-notifyRetry)).
		Order("id").Limit(notifyBatch).Find(&msgs)
	for i := range msgs {
		msgs[i].AssignedTo, msgs[i].AssignedAt = via, now
		msgs[i].Attempts++
		s.db.Model(&models.NotifyMessage{}).Where("id = ?", msgs[i].ID).
			Updates(map[string]any{"assigned_to": via, "assigned_at": now, "attempts": msgs[i].Attempts})
	}
	return msgs
}

// ack — итог отправки сообщения отправителем via
func (s *Server) ack(via string, id uint, errText string) {
	q := s.db.Model(&models.NotifyMessage{}).Where("id = ? AND assigned_to = ?", id, via)
	if errText == "" {
		now := s.now()
		q.Updates(map[string]any{"sent_at": &now, "error": ""})
		return
	}
	// Ошибка: отдадим снова (той же или другой ноде), пока не кончатся попытки
	q.Updates(map[string]any{"error": errText, "assigned_to": ""})
}

// notifyForExit — что переслать выходной ноде при синхронизации
func (s *Server) notifyForExit(node *models.Node) *protocol.Notify {
	n := s.loadNotify()
	if !n.active() {
		return nil
	}
	chats, err := telegram.ParseChats(n.Chats)
	if err != nil || len(chats) == 0 {
		return nil
	}
	msgs := s.takeMessages(node.IP)
	if len(msgs) == 0 {
		return nil
	}
	out := &protocol.Notify{Token: n.Token, Chats: chats}
	for _, m := range msgs {
		out.Messages = append(out.Messages, protocol.NotifyMessage{ID: m.ID, Text: m.Text})
	}
	return out
}

// handleNotifyAcks разбирает итоги отправки из заголовка агента
func (s *Server) handleNotifyAcks(node *models.Node, header string) {
	if header == "" {
		return
	}
	var acks []protocol.NotifyAck
	if err := json.Unmarshal([]byte(header), &acks); err != nil {
		return
	}
	for _, a := range acks {
		errText := a.Error
		if r := []rune(errText); len(r) > 300 {
			errText = string(r[:300])
		}
		s.ack(node.IP, a.ID, errText)
	}
}

// deliverMu — минутная проверка и кнопка «Тест» не должны отправлять одновременно
var deliverMu sync.Mutex

var telegramClient = &http.Client{Timeout: 15 * time.Second}

// deliverDirect — мастер отправляет сам, если пересылать некому (нет живых выходных нод)
func (s *Server) deliverDirect() {
	deliverMu.Lock()
	defer deliverMu.Unlock()
	exits, err := s.aliveNodes(protocol.RoleExit)
	if err != nil || len(exits) > 0 {
		return
	}
	n := s.loadNotify()
	if !n.active() {
		return
	}
	chats, err := telegram.ParseChats(n.Chats)
	if err != nil {
		return
	}
	for _, m := range s.takeMessages(notifyMaster) {
		var errs []string
		for _, chat := range chats {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := telegram.Send(ctx, telegramClient, n.Token, chat, m.Text); err != nil {
				errs = append(errs, err.Error())
			}
			cancel()
		}
		s.ack(notifyMaster, m.ID, strings.Join(errs, "; "))
	}
}
