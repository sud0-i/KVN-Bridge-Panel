package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Качество канала мост → выходные ноды: мост раз в минуту присылает замеры
// (задержка TCP-рукопожатия и потери), мастер хранит их и рисует в панели.

const (
	linkKeep = 8 * 24 * time.Hour
	// LinkLossAlert — потери (%) за последние linkAlertWindow, при которых сообщаем
	LinkLossAlert   = 10.0
	linkAlertWindow = 10 * time.Minute
	linkMinSamples  = 3 // меньше замеров за окно — судить рано
)

// storeLinkSamples сохраняет замеры от моста. Время — время синхронизации:
// замеры копятся у агента не дольше пары минут, точнее не нужно.
func (s *Server) storeLinkSamples(from, raw string, now time.Time) {
	var samples []protocol.LinkSample
	if json.Unmarshal([]byte(raw), &samples) != nil || len(samples) > 120 {
		return
	}
	rows := make([]models.LinkSample, 0, len(samples))
	for _, l := range samples {
		if l.To == "" || l.Sent <= 0 || l.Lost < 0 || l.Lost > l.Sent || l.AvgMs < 0 || l.AvgMs > 60000 {
			continue
		}
		rows = append(rows, models.LinkSample{From: from, To: l.To, At: now, Sent: l.Sent, Lost: l.Lost, AvgMs: l.AvgMs, MaxMs: l.MaxMs})
	}
	if len(rows) > 0 {
		s.db.Create(&rows)
	}
}

// cleanupLinks удаляет старые замеры
func (s *Server) cleanupLinks() {
	s.db.Where("at < ?", s.now().Add(-linkKeep)).Delete(&models.LinkSample{})
}

type linkPoint struct {
	T       time.Time `json:"t"`
	LossPct float64   `json:"loss_pct"`
	AvgMs   float64   `json:"avg_ms"` // 0 — ни одного ответа
	MaxMs   float64   `json:"max_ms"`
	Sent    int       `json:"sent"`
}

type linkSeries struct {
	From    string      `json:"from"`
	To      string      `json:"to"`
	Points  []linkPoint `json:"points"`
	Current *linkPoint  `json:"current,omitempty"` // за последние linkAlertWindow
}

// aggregate сводит замеры в точку: потери — по всем подключениям,
// задержка — средняя по ответившим
func aggregate(rows []models.LinkSample, t time.Time) linkPoint {
	p := linkPoint{T: t}
	var lost, ok int
	var sum float64
	for _, r := range rows {
		p.Sent += r.Sent
		lost += r.Lost
		got := r.Sent - r.Lost
		ok += got
		sum += r.AvgMs * float64(got)
		p.MaxMs = math.Max(p.MaxMs, r.MaxMs)
	}
	if p.Sent > 0 {
		p.LossPct = math.Round(float64(lost)*1000/float64(p.Sent)) / 10
	}
	if ok > 0 {
		p.AvgMs = math.Round(sum*10/float64(ok)) / 10
	}
	p.MaxMs = math.Round(p.MaxMs*10) / 10
	return p
}

// linkSeries — ряды по всем парам мост → экзит за период с шагом step
func (s *Server) linkSeries(since time.Time, step time.Duration) ([]linkSeries, error) {
	var rows []models.LinkSample
	if err := s.db.Where("at >= ?", since).Order("at").Find(&rows).Error; err != nil {
		return nil, err
	}
	type key struct{ from, to string }
	byLink := map[key][]models.LinkSample{}
	var order []key
	for _, r := range rows {
		k := key{r.From, r.To}
		if _, ok := byLink[k]; !ok {
			order = append(order, k)
		}
		byLink[k] = append(byLink[k], r)
	}
	now := s.now()
	out := []linkSeries{}
	for _, k := range order {
		ls := linkSeries{From: k.from, To: k.to, Points: []linkPoint{}}
		buckets := map[int64][]models.LinkSample{}
		var recent []models.LinkSample
		for _, r := range byLink[k] {
			b := r.At.Sub(since).Nanoseconds() / step.Nanoseconds()
			buckets[b] = append(buckets[b], r)
			if now.Sub(r.At) <= linkAlertWindow {
				recent = append(recent, r)
			}
		}
		// Пустые промежутки (мост молчал) оставляем дырами, а не нулями
		n := int64(now.Sub(since) / step)
		for b := int64(0); b <= n; b++ {
			if rs, ok := buckets[b]; ok {
				ls.Points = append(ls.Points, aggregate(rs, since.Add(time.Duration(b)*step)))
			}
		}
		if len(recent) > 0 {
			cur := aggregate(recent, recent[len(recent)-1].At)
			cur.Sent = len(recent)
			ls.Current = &cur
		}
		out = append(out, ls)
	}
	return out, nil
}

func (s *Server) getLinks(c echo.Context) error {
	span, step := 24*time.Hour, 10*time.Minute
	if c.QueryParam("range") == "7d" {
		span, step = 7*24*time.Hour, time.Hour
	}
	// Начало периода — по сетке шага, чтобы столбики не «ползли» между обновлениями
	since := s.now().Add(-span).Truncate(step)
	series, err := s.linkSeries(since, step)
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, map[string]any{"step_minutes": int(step / time.Minute), "since": since, "links": series, "alert_pct": LinkLossAlert})
}

// linkAlerts — каналы с большими потерями за последние минуты (для уведомлений)
func (s *Server) linkAlerts(lang string, names map[string]string) map[string]alert {
	out := map[string]alert{}
	series, err := s.linkSeries(s.now().Add(-linkAlertWindow), linkAlertWindow)
	if err != nil {
		return out
	}
	for _, l := range series {
		c := l.Current
		if c == nil || c.Sent < linkMinSamples || c.LossPct < LinkLossAlert {
			continue
		}
		name := fmt.Sprintf("%s → %s", nameOr(names, l.From), nameOr(names, l.To))
		out["link:"+l.From+">"+l.To] = alert{
			Text:    notifyText(lang, "linkBad", name, c.LossPct, c.AvgMs),
			Resolve: notifyText(lang, "linkOK", name),
		}
	}
	return out
}

func nameOr(names map[string]string, ip string) string {
	if n := names[ip]; n != "" {
		return n
	}
	return ip
}
