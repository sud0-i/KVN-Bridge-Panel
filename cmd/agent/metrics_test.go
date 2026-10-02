package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func fakeProc(t *testing.T, dir string, cpu, rx, tx, out, re string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	files := map[string]string{
		"stat":         "cpu  " + cpu + "\ncpu0 1 2 3 4\n",
		"meminfo":      "MemTotal:        2048000 kB\nMemFree: 100 kB\nMemAvailable:    512000 kB\n",
		"loadavg":      "0.42 0.30 0.20 1/123 4567\n",
		"uptime":       "86400.55 100.00\n",
		"net/route":    "Iface\tDestination\tGateway\neth0\t00000000\t0100A8C0\neth0\t0000A8C0\t00000000\n",
		"net/dev":      "Inter-|\n face |\n    lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n  eth0: " + rx + " 0 0 0 0 0 0 0 " + tx + " 0 0 0 0 0 0 0\ndocker0: 5000 0 0 0 0 0 0 0 5000 0 0 0 0 0 0 0\n",
		"net/snmp":     "Tcp: RtoAlgorithm OutSegs RetransSegs\nTcp: 1 " + out + " " + re + "\n",
		"net/sockstat": "sockets: used 36\nTCP: inuse 57 orphan 0 tw 2 alloc 60 mem 4\n",
	}
	for name, body := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
}

func TestMetricsSample(t *testing.T) {
	dir := t.TempDir()
	defer func(p string) { procRoot = p }(procRoot)
	procRoot = dir
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := newMetricsSampler()
	m.now = func() time.Time { return now }
	m.disk = func(string) (uint64, uint64, error) { return 40 << 30, 10 << 30, nil }

	// user nice system idle iowait irq softirq steal
	fakeProc(t, dir, "100 0 100 700 50 0 0 50 0 0", "1000000", "2000000", "10000", "100")
	first := m.sample()
	if first.CPU != nil || first.RxBps != nil || first.Retrans != nil {
		t.Fatal("первый замер — без скоростей")
	}
	if first.Mem != 75 || first.MemTotalMB != 2000 || first.Disk != 75 || first.DiskTotalGB != 40 || first.Load1 != 0.42 ||
		first.Conns != 57 || first.UptimeSec != 86400 {
		t.Fatalf("мгновенные показатели: %+v", first)
	}

	now = now.Add(time.Minute)
	// +1000 тиков: busy 300 (user 200, system 50, steal 50), idle 600, iowait 100
	fakeProc(t, dir, "300 0 150 1300 150 0 0 100 0 0", "61000000", "2000000", "15000", "200")
	s := m.sample()
	if *s.CPU != 30 || *s.Steal != 5 {
		t.Fatalf("процессор: cpu %v steal %v", *s.CPU, *s.Steal)
	}
	// 60 МБ за минуту на eth0 = 8 Мбит/с; lo и docker0 не считаются
	if *s.RxBps != 8e6 || *s.TxBps != 0 {
		t.Fatalf("сеть: %v %v", *s.RxBps, *s.TxBps)
	}
	if *s.Retrans != 2 {
		t.Fatalf("ретрансмиты: %v", *s.Retrans)
	}

	// Почти без трафика — доля ретрансмитов не считается
	now = now.Add(time.Minute)
	fakeProc(t, dir, "300 0 150 2300 150 0 0 100 0 0", "61000000", "2000000", "15100", "250")
	if s := m.sample(); s.Retrans != nil || *s.CPU != 0 {
		t.Fatalf("простой: %+v", s)
	}
	if h := m.header(); !strings.Contains(h, "mem_total_mb") {
		t.Fatal("заголовок")
	}
}

func TestRunAction(t *testing.T) {
	var calls []string
	defer func(r func(string, ...string) ([]byte, error)) { runCmd = r }(runCmd)
	runCmd = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	exited := false
	defer func(e func()) { exitNow = e }(exitNow)
	exitNow = func() { exited = true }

	a := &agent{masterURL: "http://127.0.0.1:1", pending: map[string]*protocol.UserTraffic{}, mieru: &mieruState{applied: "x"}}
	a.runAction(protocol.ActionRestartXray)
	a.runAction(protocol.ActionRestartMieru)
	a.runAction(protocol.ActionReboot)
	if strings.Join(calls, "|") != "systemctl restart xray|systemctl restart mita|systemctl reboot" {
		t.Fatalf("вызовы: %v", calls)
	}
	if a.actionResult != "reboot: ok" || a.mieru.applied != "" {
		t.Fatalf("итог: %q", a.actionResult)
	}
	a.runAction(protocol.ActionRestartAgent)
	if !exited {
		t.Fatal("перезапуск агента — выход, systemd поднимет")
	}
	a.runAction("rm -rf /")
	if !strings.Contains(a.actionResult, "неизвестное действие") {
		t.Fatal("чужие команды не выполняются")
	}
}
