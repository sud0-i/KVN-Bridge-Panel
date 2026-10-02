package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func TestLinkProbe(t *testing.T) {
	defer func(g time.Duration) { linkGap = g }(linkGap)
	linkGap = 0
	l := newLinkMonitor()
	n := 0
	l.dial = func(addr string, _ time.Duration) (time.Duration, error) {
		n++
		if addr != "203.0.113.9:443" {
			t.Fatalf("адрес: %s", addr)
		}
		if n%4 == 0 { // каждое четвёртое — без ответа
			return 0, errors.New("i/o timeout")
		}
		return time.Duration(10+n) * time.Millisecond, nil
	}
	s := l.probe("203.0.113.9:443")
	if s.To != "203.0.113.9" || s.Sent != linkProbes || s.Lost != 2 {
		t.Fatalf("потери: %+v", s)
	}
	// ответили 1,2,3,5,6,7,9,10 → 11..20 мс без 14 и 18
	if s.MaxMs != 20 || s.AvgMs < 15 || s.AvgMs > 16 {
		t.Fatalf("задержка: %+v", s)
	}
	all := newLinkMonitor()
	all.dial = func(string, time.Duration) (time.Duration, error) { return 0, errors.New("x") }
	if s := all.probe("203.0.113.9:443"); s.Lost != linkProbes || s.AvgMs != 0 {
		t.Fatalf("всё потеряно: %+v", s)
	}
}

func TestLinkHeader(t *testing.T) {
	defer func(g time.Duration) { linkGap = g }(linkGap)
	linkGap = 0
	l := newLinkMonitor()
	if h, _ := l.header(); h != "" {
		t.Fatal("без замеров — без заголовка")
	}
	l.dial = func(string, time.Duration) (time.Duration, error) { return time.Millisecond, nil }
	l.setExits([]protocol.Exit{{Address: "203.0.113.9"}, {Address: "2001:db8::1", Port: 8443}})
	if l.targets[1] != net.JoinHostPort("2001:db8::1", "8443") {
		t.Fatalf("адреса: %v", l.targets)
	}
	l.probeAll()
	h, taken := l.header()
	raw, _ := url.QueryUnescape(h)
	var got []protocol.LinkSample
	json.Unmarshal([]byte(raw), &got)
	if taken != 2 || len(got) != 2 || got[0].To != "203.0.113.9" || got[1].To != "2001:db8::1" {
		t.Fatalf("заголовок: %s", raw)
	}
	l.probeAll() // пришло новое, пока шла синхронизация
	l.sent(taken)
	if h, n := l.header(); h == "" || n != 2 {
		t.Fatal("отправленное убрано, новое осталось")
	}
	l.setExits(nil)
	l.sent(10)
	l.probeAll()
	if h, _ := l.header(); h != "" {
		t.Fatal("без экзитов (выходная нода) не меряем")
	}
}

// Настоящие подключения: открытый порт — без потерь, закрытый — всё потеряно
func TestLinkProbeReal(t *testing.T) {
	defer func(g time.Duration) { linkGap = g }(linkGap)
	linkGap = 0
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	open := ln.Addr().String()
	l := newLinkMonitor()
	if s := l.probe(open); s.Lost != 0 || s.AvgMs <= 0 || s.AvgMs > 100 {
		t.Fatalf("открытый порт: %+v", s)
	}
	ln.Close()
	if s := l.probe(open); s.Lost != linkProbes {
		t.Fatalf("закрытый порт: %+v", s)
	}
}
