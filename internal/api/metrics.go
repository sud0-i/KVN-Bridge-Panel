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

// Показатели серверов (агенты присылают их при каждой синхронизации) и действия
// с серверами из панели (перезапуск служб, перезагрузка).

const (
	metricWindow = 10 * time.Minute // «сейчас» и уведомления — по последним 10 минутам
	// Пороги уведомлений
	alertDisk    = 90.0
	alertMem     = 95.0
	alertCPU     = 90.0
	alertSteal   = 20.0
	alertRetrans = 5.0
	// actionTTL — не выданное за это время действие отменяется (нода была недоступна)
	actionTTL = 5 * time.Minute
)

func (s *Server) storeMetrics(node *models.Node, raw string, now time.Time) {
	var m protocol.NodeMetrics
	if json.Unmarshal([]byte(raw), &m) != nil {
		return
	}
	pct := func(v float64) bool { return v >= 0 && v <= 100 }
	pctp := func(v *float64) bool { return v == nil || pct(*v) }
	nonneg := func(v *float64) bool { return v == nil || (*v >= 0 && *v < 1e12) }
	if !pct(m.Mem) || !pct(m.Disk) || !pctp(m.CPU) || !pctp(m.Steal) || !pctp(m.Retrans) || !nonneg(m.RxBps) || !nonneg(m.TxBps) {
		return
	}
	s.db.Create(&models.NodeMetric{
		Node: node.IP, At: now, CPU: m.CPU, Steal: m.Steal, Mem: m.Mem, MemTotalMB: m.MemTotalMB,
		Disk: m.Disk, DiskTotalGB: m.DiskTotalGB, Load1: m.Load1, Cores: m.Cores,
		RxBps: m.RxBps, TxBps: m.TxBps, Retrans: m.Retrans, Conns: m.Conns, UptimeSec: m.UptimeSec,
	})
}

func (s *Server) cleanupMetrics() {
	s.db.Where("at < ?", s.now().Add(-linkKeep)).Delete(&models.NodeMetric{})
}

// metricPoint — среднее за интервал (CPU, steal, сеть, ретрансмиты) и максимум
// (память, диск — важна вершина, а не среднее)
type metricPoint struct {
	T       time.Time `json:"t"`
	CPU     *float64  `json:"cpu"`
	Steal   *float64  `json:"steal"`
	Mem     float64   `json:"mem"`
	Disk    float64   `json:"disk"`
	RxMbps  *float64  `json:"rx_mbps"`
	TxMbps  *float64  `json:"tx_mbps"`
	Retrans *float64  `json:"retrans"`
	Conns   int       `json:"conns"`
}

type avg struct {
	sum float64
	n   int
}

func (a *avg) add(v *float64) {
	if v != nil {
		a.sum += *v
		a.n++
	}
}

func (a avg) val(scale float64) *float64 {
	if a.n == 0 {
		return nil
	}
	v := math.Round(a.sum/float64(a.n)/scale*10) / 10
	return &v
}

func aggregateMetrics(rows []models.NodeMetric, t time.Time) metricPoint {
	p := metricPoint{T: t}
	var cpu, steal, rx, tx, re avg
	for _, r := range rows {
		cpu.add(r.CPU)
		steal.add(r.Steal)
		rx.add(r.RxBps)
		tx.add(r.TxBps)
		re.add(r.Retrans)
		p.Mem = math.Max(p.Mem, r.Mem)
		p.Disk = math.Max(p.Disk, r.Disk)
		if r.Conns > p.Conns {
			p.Conns = r.Conns
		}
	}
	p.CPU, p.Steal, p.Retrans = cpu.val(1), steal.val(1), re.val(1)
	p.RxMbps, p.TxMbps = rx.val(1e6), tx.val(1e6)
	return p
}

type nodeMetrics struct {
	Node    string             `json:"node"`
	Points  []metricPoint      `json:"points"`
	Current *metricPoint       `json:"current,omitempty"` // за последние 10 минут
	Last    *models.NodeMetric `json:"last,omitempty"`    // последний замер: объёмы, uptime
}

func (s *Server) metricSeries(since time.Time, step time.Duration) ([]nodeMetrics, error) {
	var rows []models.NodeMetric
	if err := s.db.Where("at >= ?", since).Order("at").Find(&rows).Error; err != nil {
		return nil, err
	}
	byNode := map[string][]models.NodeMetric{}
	var order []string
	for _, r := range rows {
		if _, ok := byNode[r.Node]; !ok {
			order = append(order, r.Node)
		}
		byNode[r.Node] = append(byNode[r.Node], r)
	}
	now := s.now()
	out := []nodeMetrics{}
	for _, ip := range order {
		nm := nodeMetrics{Node: ip, Points: []metricPoint{}}
		buckets := map[int64][]models.NodeMetric{}
		var recent []models.NodeMetric
		for _, r := range byNode[ip] {
			b := r.At.Sub(since).Nanoseconds() / step.Nanoseconds()
			buckets[b] = append(buckets[b], r)
			if now.Sub(r.At) <= metricWindow {
				recent = append(recent, r)
			}
		}
		for b := int64(0); b <= int64(now.Sub(since)/step); b++ {
			if rs, ok := buckets[b]; ok {
				nm.Points = append(nm.Points, aggregateMetrics(rs, since.Add(time.Duration(b)*step)))
			}
		}
		if len(recent) > 0 {
			cur := aggregateMetrics(recent, recent[len(recent)-1].At)
			nm.Current = &cur
			last := recent[len(recent)-1]
			nm.Last = &last
		}
		out = append(out, nm)
	}
	return out, nil
}

func (s *Server) getMetrics(c echo.Context) error {
	span, step := 24*time.Hour, 10*time.Minute
	if c.QueryParam("range") == "7d" {
		span, step = 7*24*time.Hour, time.Hour
	}
	since := s.now().Add(-span).Truncate(step)
	series, err := s.metricSeries(since, step)
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, map[string]any{"step_minutes": int(step / time.Minute), "since": since, "nodes": series,
		"alerts": map[string]float64{"disk": alertDisk, "mem": alertMem, "cpu": alertCPU, "steal": alertSteal, "retrans": alertRetrans}})
}

// metricAlerts — серверы, у которых сейчас что-то на пределе (для уведомлений)
func (s *Server) metricAlerts(lang string, names map[string]string) map[string]alert {
	out := map[string]alert{}
	series, err := s.metricSeries(s.now().Add(-metricWindow), metricWindow)
	if err != nil {
		return out
	}
	for _, nm := range series {
		c := nm.Current
		if c == nil {
			continue
		}
		name := nameOr(names, nm.Node)
		over := func(key string, v *float64, limit float64, args ...any) {
			if v != nil && *v >= limit {
				out[key+":"+nm.Node] = alert{Text: notifyText(lang, key, append([]any{name}, args...)...), Resolve: notifyText(lang, "metricOK", name, notifyText(lang, key+"Name"))}
			}
		}
		disk, mem := c.Disk, c.Mem
		over("disk", &disk, alertDisk, disk)
		over("mem", &mem, alertMem, mem)
		over("cpu", c.CPU, alertCPU, deref(c.CPU))
		over("steal", c.Steal, alertSteal, deref(c.Steal))
		over("retrans", c.Retrans, alertRetrans, deref(c.Retrans))
	}
	return out
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// ---------- Действия ----------

var nodeActions = map[string]bool{
	protocol.ActionRestartXray: true, protocol.ActionRestartMieru: true,
	protocol.ActionRestartAgent: true, protocol.ActionReboot: true,
}

func (s *Server) nodeAction(c echo.Context) error {
	var node models.Node
	if err := s.db.First(&node, "ip = ?", c.Param("ip")).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := c.Bind(&req); err != nil || !nodeActions[req.Action] {
		return jsonError(c, http.StatusBadRequest, "Неизвестное действие")
	}
	if !s.isAlive(node) {
		return jsonError(c, http.StatusBadRequest, "Нода не на связи — команду некому выполнить. Перезагрузите сервер в панели хостера.")
	}
	s.db.Model(&node).Updates(map[string]any{"pending_action": req.Action, "pending_at": s.now(), "action_result": ""})
	return c.JSON(http.StatusAccepted, map[string]string{"status": "queued"})
}

// takeAction — действие для ноды (выдаётся один раз; устаревшее отменяется)
func (s *Server) takeAction(node *models.Node) string {
	if node.PendingAction == "" {
		return ""
	}
	action := node.PendingAction
	updates := map[string]any{"pending_action": ""}
	if s.now().Sub(node.PendingAt) > actionTTL {
		updates["action_result"] = fmt.Sprintf("%s: отменено — нода не выходила на связь %d минут", action, int(actionTTL/time.Minute))
		action = ""
	} else {
		updates["last_action"], updates["last_action_at"] = action, s.now()
		updates["action_result"] = action + ": отправлено"
	}
	s.db.Model(&models.Node{}).Where("ip = ?", node.IP).Updates(updates)
	return action
}
