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

func f(v float64) *float64 { return &v }

func (ev *env) sendMetrics(token string, m protocol.NodeMetrics) protocol.SyncResponse {
	raw, _ := json.Marshal(m)
	rec := ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: token, protocol.MetricsHeader: url.QueryEscape(string(raw))})
	var resp protocol.SyncResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp
}

func TestMetrics(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	exit := ev.addNode("10.0.0.9", protocol.RoleExit)
	ev.db.Model(&models.Node{}).Where("ip = ?", "10.0.0.9").Update("label", "FIN")

	for i := 0; i < 60; i++ {
		ev.sendMetrics(exit, protocol.NodeMetrics{CPU: f(10), Steal: f(1), Mem: 40, MemTotalMB: 1000, Disk: 50, DiskTotalGB: 20,
			RxBps: f(8e6), TxBps: f(16e6), Retrans: f(0.5), Conns: 100, UptimeSec: int64(1000 + i*60)})
		ev.now = ev.now.Add(time.Minute)
	}
	// Вечер: соседи забирают процессор, сеть теряет пакеты
	for i := 0; i < 10; i++ {
		ev.sendMetrics(exit, protocol.NodeMetrics{CPU: f(60), Steal: f(35), Mem: 41, Disk: 50, Retrans: f(8)})
		ev.now = ev.now.Add(time.Minute)
	}
	ev.sendMetrics(exit, protocol.NodeMetrics{Mem: 150}) // мусор не сохраняется
	var n int64
	ev.db.Model(&models.NodeMetric{}).Count(&n)
	if n != 70 {
		t.Fatalf("сохранено %d", n)
	}

	var resp struct {
		StepMinutes int `json:"step_minutes"`
		Nodes       []nodeMetrics
	}
	json.Unmarshal(ev.do("GET", "/api/metrics", "", adm).Body.Bytes(), &resp)
	if resp.StepMinutes != 10 || len(resp.Nodes) != 1 {
		t.Fatalf("%+v", resp)
	}
	nm := resp.Nodes[0]
	if p := nm.Points[0]; *p.CPU != 10 || *p.RxMbps != 8 || *p.TxMbps != 16 || p.Mem != 40 {
		t.Fatalf("точка: %+v", p)
	}
	if c := nm.Current; c == nil || *c.Steal != 35 || *c.Retrans != 8 || c.Mem != 41 {
		t.Fatalf("сейчас: %+v", c)
	}
	if nm.Last == nil || nm.Last.Node != "10.0.0.9" {
		t.Fatal("последний замер")
	}
	if rec := ev.do("GET", "/api/metrics", "", nil); rec.Code == http.StatusOK {
		t.Fatal("только для админа")
	}

	ev.enableNotify("")
	ev.s.notifyTick()
	msgs := ev.queued()
	last := msgs[len(msgs)-1].Text
	if !strings.Contains(last, "FIN: соседи по серверу забирают 35% процессора") || !strings.Contains(last, "8.0% TCP-пакетов") {
		t.Fatalf("уведомление: %q", last)
	}
	ev.s.cleanupMetrics()
	ev.now = ev.now.Add(linkKeep + time.Hour)
	ev.s.cleanupMetrics()
	ev.db.Model(&models.NodeMetric{}).Count(&n)
	if n != 0 {
		t.Fatal("старые замеры удаляются")
	}
}

func TestNodeAction(t *testing.T) {
	ev := newEnv(t)
	adm := ev.adminToken()
	exit := ev.addNode("10.0.0.9", protocol.RoleExit)
	act := func(body string) int { return ev.do("POST", "/api/nodes/10.0.0.9/action", body, adm).Code }

	if act(`{"action":"reboot"}`) != http.StatusBadRequest {
		t.Fatal("нода не на связи — некому выполнить")
	}
	ev.syncAs(exit)
	if act(`{"action":"rm -rf /"}`) != http.StatusBadRequest {
		t.Fatal("только известные действия")
	}
	if act(`{"action":"restart-xray"}`) != http.StatusAccepted {
		t.Fatal("в очередь")
	}
	if a := ev.syncAs(exit).Action; a != protocol.ActionRestartXray {
		t.Fatalf("выдано: %q", a)
	}
	if a := ev.syncAs(exit).Action; a != "" {
		t.Fatal("выдаётся один раз")
	}
	ev.do("GET", "/api/sync", "", map[string]string{protocol.NodeTokenHeader: exit, protocol.ActionHeader: url.QueryEscape("restart-xray: ok")})
	var nd models.Node
	ev.db.First(&nd, "ip = ?", "10.0.0.9")
	if nd.ActionResult != "restart-xray: ok" || nd.LastAction != "restart-xray" {
		t.Fatalf("итог: %+v", nd)
	}
	// Нода пропала дольше TTL — перезагрузка позже не случится
	act(`{"action":"reboot"}`)
	ev.now = ev.now.Add(actionTTL + time.Minute)
	if a := ev.syncAs(exit).Action; a != "" {
		t.Fatal("устаревшее действие отменяется")
	}
	ev.db.First(&nd, "ip = ?", "10.0.0.9")
	if !strings.Contains(nd.ActionResult, "отменено") {
		t.Fatalf("итог: %q", nd.ActionResult)
	}
}
