package main

import (
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Мониторинг канала мост → выходные ноды. Раз в минуту мост делает серию
// TCP-подключений к 443 каждого экзита — туда же, куда идёт настоящий трафик, —
// и считает задержку (время рукопожатия TCP) и долю подключений без ответа.
// ICMP здесь не годится: на пути его часто режут или отвечают в последнюю очередь.

const (
	linkProbes   = 10          // подключений за замер
	linkTimeout  = time.Second // без ответа дольше — потеря (повтор SYN у Linux — через 1 с)
	linkInterval = time.Minute
)

// linkGap — пауза между подключениями (в тестах — 0)
var linkGap = 250 * time.Millisecond

type linkMonitor struct {
	mu      sync.Mutex
	targets []string // host:port выходных нод
	ready   []protocol.LinkSample
	// dial подменяется в тестах
	dial func(addr string, timeout time.Duration) (time.Duration, error)
}

func newLinkMonitor() *linkMonitor {
	return &linkMonitor{dial: func(addr string, timeout time.Duration) (time.Duration, error) {
		start := time.Now()
		c, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return 0, err
		}
		rtt := time.Since(start)
		c.Close()
		return rtt, nil
	}}
}

// setExits — какие экзиты мерить (из синхронизации); у выходных нод список пуст
func (l *linkMonitor) setExits(exits []protocol.Exit) {
	var t []string
	for _, e := range exits {
		port := e.Port
		if port == 0 {
			port = 443
		}
		t = append(t, net.JoinHostPort(e.Address, strconv.Itoa(port)))
	}
	l.mu.Lock()
	l.targets = t
	l.mu.Unlock()
}

// probeAll меряет все экзиты параллельно и откладывает результат до синхронизации
func (l *linkMonitor) probeAll() {
	l.mu.Lock()
	targets := append([]string(nil), l.targets...)
	l.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	results := make([]protocol.LinkSample, len(targets))
	var wg sync.WaitGroup
	for i, addr := range targets {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			results[i] = l.probe(addr)
		}(i, addr)
	}
	wg.Wait()
	l.mu.Lock()
	l.ready = append(l.ready, results...)
	// Мастер недоступен долго — не копим бесконечно
	if len(l.ready) > 60 {
		l.ready = l.ready[len(l.ready)-60:]
	}
	l.mu.Unlock()
}

func (l *linkMonitor) probe(addr string) protocol.LinkSample {
	host, _, _ := net.SplitHostPort(addr)
	s := protocol.LinkSample{To: host, Sent: linkProbes}
	var sum time.Duration
	for i := 0; i < linkProbes; i++ {
		if i > 0 {
			time.Sleep(linkGap)
		}
		rtt, err := l.dial(addr, linkTimeout)
		if err != nil {
			s.Lost++
			continue
		}
		sum += rtt
		if ms := float64(rtt.Microseconds()) / 1000; ms > s.MaxMs {
			s.MaxMs = ms
		}
	}
	if ok := s.Sent - s.Lost; ok > 0 {
		s.AvgMs = float64(sum.Microseconds()) / 1000 / float64(ok)
	}
	return s
}

// header — накопленные замеры для мастера; taken — подтвердить после успешной отправки
func (l *linkMonitor) header() (value string, taken int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ready) == 0 {
		return "", 0
	}
	raw, _ := json.Marshal(l.ready)
	return url.QueryEscape(string(raw)), len(l.ready)
}

// sent убирает отправленные замеры (новые, пришедшие за время запроса, остаются)
func (l *linkMonitor) sent(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.ready) {
		n = len(l.ready)
	}
	l.ready = l.ready[n:]
}

func (l *linkMonitor) run() {
	for {
		l.probeAll()
		time.Sleep(linkInterval)
	}
}
