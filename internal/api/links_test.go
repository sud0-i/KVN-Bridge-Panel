package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func (ev *env) sendLinks(token string, samples ...protocol.LinkSample) {
	raw, _ := json.Marshal(samples)
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token, protocol.LinksHeader: url.QueryEscape(string(raw))})
}

func TestLinks(t *testing.T) {
	ev := newEnv(t)
	bridge := ev.addNode("10.0.0.1", protocol.RoleBridge)
	exit := ev.addNode("10.0.0.9", protocol.RoleExit)
	ev.syncAs(exit)
	ev.db.Model(&models.Node{}).Where("ip = ?", "10.0.0.9").Update("label", "FIN")
	start := ev.now

	// Час нормальной связи, потом 10 минут с потерями
	for i := 0; i < 60; i++ {
		ev.sendLinks(bridge, protocol.LinkSample{To: "10.0.0.9", Sent: 10, Lost: 0, AvgMs: 18, MaxMs: 22})
		ev.now = ev.now.Add(time.Minute)
	}
	for i := 0; i < 10; i++ {
		ev.sendLinks(bridge, protocol.LinkSample{To: "10.0.0.9", Sent: 10, Lost: 4, AvgMs: 25, MaxMs: 60})
		ev.now = ev.now.Add(time.Minute)
	}
	// Мусор и замеры от не-моста не сохраняются
	ev.sendLinks(bridge, protocol.LinkSample{To: "10.0.0.9", Sent: 10, Lost: 11})
	ev.sendLinks(exit, protocol.LinkSample{To: "10.0.0.1", Sent: 10})
	var count int64
	ev.db.Model(&models.LinkSample{}).Count(&count)
	if count != 70 {
		t.Fatalf("сохранено %d замеров, ожидали 70", count)
	}

	rec := ev.do("GET", "/api/links", "", ev.adminToken())
	var resp struct {
		StepMinutes int `json:"step_minutes"`
		Links       []linkSeries
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusOK || resp.StepMinutes != 10 || len(resp.Links) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	l := resp.Links[0]
	if l.From != "10.0.0.1" || l.To != "10.0.0.9" || len(l.Points) < 7 || len(l.Points) > 8 {
		t.Fatalf("ряд: %+v (%d точек)", l, len(l.Points))
	}
	if p := l.Points[0]; p.LossPct != 0 || p.AvgMs != 18 || !p.T.Before(start.Add(time.Minute)) {
		t.Fatalf("первая точка: %+v", p)
	}
	if c := l.Current; c == nil || c.LossPct != 40 || c.AvgMs != 25 || c.MaxMs != 60 {
		t.Fatalf("сейчас: %+v", c)
	}
	if rec := ev.do("GET", "/api/links?range=7d", "", ev.adminToken()); !strings.Contains(rec.Body.String(), `"step_minutes":60`) {
		t.Fatal("неделя — по часам")
	}
	if rec := ev.do("GET", "/api/links", "", nil); rec.Code == http.StatusOK {
		t.Fatal("только для админа")
	}

	// Уведомление о потерях и о восстановлении
	ev.enableNotify("")
	ev.s.notifyTick()
	msgs := ev.queued()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Text, "10.0.0.1 → FIN: потери 40%") {
		t.Fatalf("о потерях: %+v", msgs)
	}
	before := len(msgs)
	for i := 0; i < 11; i++ {
		ev.sendLinks(bridge, protocol.LinkSample{To: "10.0.0.9", Sent: 10, AvgMs: 18})
		ev.now = ev.now.Add(time.Minute)
	}
	ev.syncAs(exit) // чтобы выходная нода не считалась пропавшей
	ev.syncAs(bridge)
	ev.s.notifyTick()
	msgs = ev.queued()
	if len(msgs) != before+1 || !strings.Contains(msgs[len(msgs)-1].Text, "канал восстановился") {
		t.Fatalf("о восстановлении: %+v", msgs[before:])
	}

	// Старое удаляется
	ev.now = ev.now.Add(linkKeep + time.Hour)
	ev.s.cleanupLinks()
	ev.db.Model(&models.LinkSample{}).Count(&count)
	if count != 0 {
		t.Fatalf("старые замеры не удалены: %d", count)
	}
}
