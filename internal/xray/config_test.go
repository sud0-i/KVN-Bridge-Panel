package xray

import (
	"encoding/json"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func build(t *testing.T, s protocol.SyncResponse) map[string]any {
	t.Helper()
	raw, err := Build(s, "priv")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	return cfg
}

func TestBridgeWithExitsUsesBalancer(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "www.example.com", ShortID: "abcd"},
		Clients: []protocol.Client{{ID: "u1", Email: "u1"}},
		Exits: []protocol.Exit{
			{Address: "1.1.1.1", UUID: "link", PublicKey: "pk1", SNI: "a.com", ShortID: "01"},
			{Address: "2.2.2.2", UUID: "link", PublicKey: "pk2", SNI: "b.com", ShortID: "02"},
		},
	})

	outbounds := cfg["outbounds"].([]any)
	if len(outbounds) != 4 { // direct, block, exit-0, exit-1
		t.Fatalf("ожидали 4 outbound, получили %d", len(outbounds))
	}
	routing := cfg["routing"].(map[string]any)
	balancers, ok := routing["balancers"].([]any)
	if !ok {
		t.Fatal("нет балансировщика по экзитам")
	}
	// Мёртвые ноды отсеиваются (observatory), а если мертвы все — трафик блокируется, а не идёт напрямую
	if b := balancers[0].(map[string]any); b["fallbackTag"] != "block" {
		t.Fatalf("без fallbackTag Xray игнорирует observatory и при падении всех нод пускает трафик напрямую: %v", b)
	}
	// Терпимая проверка: одна неудача (сброс от DPI) не должна «убивать» ноду и блокировать весь трафик
	obs, ok := cfg["burstObservatory"].(map[string]any)
	if !ok || obs["subjectSelector"].([]any)[0] != "exit-" {
		t.Fatalf("нужна проверка живости выходных нод: %v", cfg["burstObservatory"])
	}
	if ping := obs["pingConfig"].(map[string]any); ping["sampling"].(float64) < 3 {
		t.Fatalf("решение о смерти ноды должно приниматься по нескольким проверкам: %v", ping)
	}
	if _, ok := cfg["observatory"]; ok {
		t.Fatal("обычный observatory считает ноду мёртвой после одного сбоя")
	}

	in := cfg["inbounds"].([]any)[0].(map[string]any)
	reality := in["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	if reality["shortIds"].([]any)[0] != "abcd" || reality["privateKey"] != "priv" {
		t.Fatalf("неверные параметры Reality: %v", reality)
	}
	client := in["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	if client["flow"] != Flow {
		t.Fatalf("у клиента нет flow %s", Flow)
	}
}

func TestBridgeWithoutExitsFallsBackToDirect(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "www.example.com"},
	})
	if _, ok := cfg["routing"].(map[string]any)["balancers"]; ok {
		t.Fatal("балансировщик без экзитов не нужен")
	}
	if _, ok := cfg["burstObservatory"]; ok {
		t.Fatal("без экзитов проверять некого")
	}
	first := cfg["outbounds"].([]any)[0].(map[string]any)
	if first["tag"] != "direct" {
		t.Fatalf("первым outbound должен быть direct, а не %v", first["tag"])
	}
}

func TestExitConfig(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:    protocol.RoleExit,
		Reality: protocol.Reality{SNI: "www.example.com"},
		Clients: []protocol.Client{{ID: "link", Email: "bridge-1.1.1.1"}},
	})
	if len(cfg["outbounds"].([]any)) != 2 {
		t.Fatal("экзит должен выпускать трафик напрямую")
	}
}

func TestBuildErrors(t *testing.T) {
	ok := protocol.SyncResponse{Role: protocol.RoleExit, Reality: protocol.Reality{SNI: "a.com"}}
	if _, err := Build(ok, ""); err == nil {
		t.Error("пустой приватный ключ должен давать ошибку")
	}
	bad := ok
	bad.Role = "unknown"
	if _, err := Build(bad, "priv"); err == nil {
		t.Error("неизвестная роль должна давать ошибку")
	}
}

func TestRealityDest(t *testing.T) {
	reality := func(r protocol.Reality) map[string]any {
		cfg := build(t, protocol.SyncResponse{Role: protocol.RoleBridge, Reality: r})
		in := cfg["inbounds"].([]any)[0].(map[string]any)
		return in["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	}

	def := reality(protocol.Reality{SNI: "www.example.com"})
	if def["dest"] != "www.example.com:443" || def["xver"] != float64(0) {
		t.Fatalf("по умолчанию dest = SNI:443 без PROXY protocol, получили %v / %v", def["dest"], def["xver"])
	}

	// Режим «мастер + мост»: посторонних отправляем в локальный Caddy с панелью
	local := reality(protocol.Reality{SNI: "panel.example.com", Dest: "127.0.0.1:8443", Xver: 1})
	if local["dest"] != "127.0.0.1:8443" || local["xver"] != float64(1) {
		t.Fatalf("неверный dest/xver: %v / %v", local["dest"], local["xver"])
	}
	if local["serverNames"].([]any)[0] != "panel.example.com" {
		t.Fatalf("serverNames должен быть доменом панели: %v", local["serverNames"])
	}
}

func TestBridgeDirectRules(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "www.example.com"},
		Direct:  []string{"geosite:private", "domain:example.com", "geoip:xx", "10.0.0.0/8", " "},
	})
	var domains, ips []any
	for _, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
		rule := r.(map[string]any)
		if rule["outboundTag"] != "direct" {
			continue
		}
		if d, ok := rule["domain"]; ok {
			domains = d.([]any)
		}
		if ip, ok := rule["ip"]; ok {
			ips = ip.([]any)
		}
	}
	if len(domains) != 2 || len(ips) != 2 {
		t.Fatalf("ожидали 2 доменных и 2 IP-правила, получили %v / %v", domains, ips)
	}

	// Без настроек никаких «прямых» правил нет
	empty := build(t, protocol.SyncResponse{Role: protocol.RoleBridge, Reality: protocol.Reality{SNI: "www.example.com"}})
	for _, r := range empty["routing"].(map[string]any)["rules"].([]any) {
		if r.(map[string]any)["outboundTag"] == "direct" {
			t.Fatalf("лишнее прямое правило: %v", r)
		}
	}
}

func TestExitWarp(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:    protocol.RoleExit,
		Reality: protocol.Reality{SNI: "www.example.com"},
		Warp:    []string{"geosite:category-ru", "geoip:ru"},
	})
	var warpOut bool
	for _, o := range cfg["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "warp" {
			warpOut = true
		}
	}
	var warpRules int
	for _, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
		if r.(map[string]any)["outboundTag"] == "warp" {
			warpRules++
		}
	}
	if !warpOut || warpRules != 2 {
		t.Fatalf("ожидали outbound warp и 2 правила (домены + IP), получили %v / %d", warpOut, warpRules)
	}

	plain := build(t, protocol.SyncResponse{Role: protocol.RoleExit, Reality: protocol.Reality{SNI: "www.example.com"}})
	for _, o := range plain["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "warp" {
			t.Fatal("без правил WARP не нужен")
		}
	}
}

func TestXHTTPFallback(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:      protocol.RoleBridge,
		Reality:   protocol.Reality{SNI: "www.example.com"},
		Clients:   []protocol.Client{{ID: "u1", Email: "u1"}},
		Exits:     []protocol.Exit{{Address: "1.1.1.1", UUID: "link", PublicKey: "pk", SNI: "a.com"}},
		XHTTPPath: "/secret",
	})
	ins := cfg["inbounds"].([]any)
	main := ins[0].(map[string]any)
	fb := main["settings"].(map[string]any)["fallbacks"].([]any)[0].(map[string]any)
	if fb["dest"] != xhttpSocket {
		t.Fatalf("Reality-вход должен передавать не-TCP подключения в XHTTP: %v", fb)
	}
	if ins[1].(map[string]any)["tag"] != "api" {
		t.Fatal("API должен остаться вторым входом")
	}
	xh := ins[2].(map[string]any)
	if xh["listen"] != xhttpSocket || xh["streamSettings"].(map[string]any)["xhttpSettings"].(map[string]any)["path"] != "/secret" {
		t.Fatalf("неверный XHTTP-вход: %v", xh)
	}
	client := xh["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	if _, ok := client["flow"]; ok || client["email"] != "u1" {
		t.Fatalf("у XHTTP-клиента нет flow, а email тот же (общая статистика): %v", client)
	}
	// Трафик XHTTP-клиентов тоже уходит на выходные ноды
	for _, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
		rule := r.(map[string]any)
		if rule["balancerTag"] == "exits" {
			tags := rule["inboundTag"].([]any)
			if len(tags) != 2 || tags[1] != "xhttp-in" {
				t.Fatalf("балансировщик должен брать и XHTTP-вход: %v", tags)
			}
		}
	}

	plain := build(t, protocol.SyncResponse{Role: protocol.RoleBridge, Reality: protocol.Reality{SNI: "www.example.com"}})
	if len(plain["inbounds"].([]any)) != 2 {
		t.Fatal("без пути XHTTP лишнего входа быть не должно")
	}
}

func TestBridgeWarpOnlyWithoutExits(t *testing.T) {
	hasWarp := func(cfg map[string]any) bool {
		for _, o := range cfg["outbounds"].([]any) {
			if o.(map[string]any)["tag"] == "warp" {
				return true
			}
		}
		return false
	}
	single := build(t, protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "www.example.com"},
		Warp:    []string{"geosite:openai", "geoip:ru"},
	})
	if !hasWarp(single) {
		t.Fatal("одиночный мост должен выпускать трафик по правилам через WARP")
	}
	// Первым outbound по-прежнему direct: остальной трафик — с IP сервера
	if single["outbounds"].([]any)[0].(map[string]any)["tag"] != "direct" {
		t.Fatal("outbound по умолчанию должен остаться direct")
	}

	cascade := build(t, protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "www.example.com"},
		Exits:   []protocol.Exit{{Address: "1.1.1.1", UUID: "link", PublicKey: "pk", SNI: "a.com"}},
		Warp:    []string{"geosite:openai"},
	})
	if hasWarp(cascade) {
		t.Fatal("при выходных нодах WARP на мосту не используется")
	}
}

func TestExitFingerprint(t *testing.T) {
	fp := func(cfg map[string]any) any {
		for _, o := range cfg["outbounds"].([]any) {
			ob := o.(map[string]any)
			if ob["tag"] == "exit-0" {
				return ob["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)["fingerprint"]
			}
		}
		return nil
	}
	exits := []protocol.Exit{{Address: "1.1.1.1", UUID: "link", PublicKey: "pk", SNI: "a.com"}}
	if got := fp(build(t, protocol.SyncResponse{Role: protocol.RoleBridge, Reality: protocol.Reality{SNI: "x.com"}, Exits: exits})); got != "chrome" {
		t.Fatalf("по умолчанию chrome, получили %v", got)
	}
	if got := fp(build(t, protocol.SyncResponse{Role: protocol.RoleBridge, Reality: protocol.Reality{SNI: "x.com"}, Exits: exits, Fingerprint: "safari"})); got != "safari" {
		t.Fatalf("мост должен подключаться к ноде с выбранным fingerprint, получили %v", got)
	}
}

func TestExitOverXHTTP(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "x.com"},
		Exits:   []protocol.Exit{{Address: "1.1.1.1", UUID: "link", PublicKey: "pk", SNI: "a.com", XHTTPPath: "/p"}},
	})
	for _, o := range cfg["outbounds"].([]any) {
		ob := o.(map[string]any)
		if ob["tag"] != "exit-0" {
			continue
		}
		stream := ob["streamSettings"].(map[string]any)
		user := ob["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
		xh, _ := stream["xhttpSettings"].(map[string]any)
		if stream["network"] != "xhttp" || xh["path"] != "/p" || user["flow"] != nil || stream["security"] != "reality" {
			t.Fatalf("связь с экзитом по XHTTP (без flow, поверх Reality): %v / %v", stream, user)
		}
		if xm, _ := xh["xmux"].(map[string]any); xm["maxConcurrency"] == nil {
			t.Fatal("без xmux.maxConcurrency XHTTP открывает соединение на каждый поток — выигрыша нет")
		}
		return
	}
	t.Fatal("нет outbound exit-0")
}

func TestHysteriaInbound(t *testing.T) {
	base := protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "x.com"},
		Clients: []protocol.Client{{ID: "u1", Email: "u1"}},
	}
	find := func(cfg map[string]any) map[string]any {
		for _, in := range cfg["inbounds"].([]any) {
			if in.(map[string]any)["tag"] == "hy2-in" {
				return in.(map[string]any)
			}
		}
		return nil
	}
	// Без сертификата (агент ещё не выпустил) входа нет
	noCert := base
	noCert.Hysteria = &protocol.Hysteria{}
	if find(build(t, noCert)) != nil {
		t.Fatal("без сертификата Hysteria2 не поднимаем")
	}

	withObfs := base
	withObfs.Hysteria = &protocol.Hysteria{Obfs: "pw", CertFile: "/c.crt", KeyFile: "/c.key"}
	cfg := build(t, withObfs)
	in := find(cfg)
	if in == nil || in["port"] != float64(443) || in["protocol"] != "hysteria" {
		t.Fatalf("нужен вход hysteria на 443: %v", in)
	}
	client := in["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	if client["auth"] != "u1" || client["email"] != "u1" {
		t.Fatalf("пароль — ID пользователя, email — тот же, что у VLESS (общая статистика): %v", client)
	}
	stream := in["streamSettings"].(map[string]any)
	if stream["finalmask"] == nil {
		t.Fatal("salamander не включён")
	}
	// Трафик Hysteria идёт по тем же правилам, что VLESS (для моста — тот же вход в правилах)
	for _, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
		rule := r.(map[string]any)
		if rule["balancerTag"] == "exits" {
			t.Fatal("в этом конфиге нет экзитов, балансировщика быть не должно")
		}
	}
	plain := base
	plain.Hysteria = &protocol.Hysteria{CertFile: "/c.crt", KeyFile: "/c.key"}
	if find(build(t, plain))["streamSettings"].(map[string]any)["finalmask"] != nil {
		t.Fatal("без пароля salamander маскировки быть не должно")
	}
}

func TestCDNInbound(t *testing.T) {
	base := protocol.SyncResponse{
		Role:    protocol.RoleExit,
		Reality: protocol.Reality{SNI: "x.com"},
		Clients: []protocol.Client{{ID: "u1", Email: "u1"}},
	}
	find := func(cfg map[string]any) map[string]any {
		for _, in := range cfg["inbounds"].([]any) {
			if m := in.(map[string]any); m["tag"] == "cdn-in" {
				return m
			}
		}
		return nil
	}
	if find(build(t, base)) != nil {
		t.Fatal("без CDN входа нет")
	}
	noCert := base
	noCert.CDN = &protocol.CDN{Domain: "cdn.x.com", Path: "/p"}
	if find(build(t, noCert)) != nil {
		t.Fatal("без сертификата входа нет")
	}
	with := base
	with.CDN = &protocol.CDN{Domain: "cdn.x.com", Path: "/p", CertFile: "/c", KeyFile: "/k"}
	in := find(build(t, with))
	if in == nil {
		t.Fatal("вход для CDN должен быть")
	}
	st := in["streamSettings"].(map[string]any)
	if in["port"] != float64(protocol.CDNPort) || st["network"] != "xhttp" || st["security"] != "tls" ||
		st["xhttpSettings"].(map[string]any)["path"] != "/p" {
		t.Fatalf("XHTTP поверх TLS на %d: %v", protocol.CDNPort, in)
	}
	c := in["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	if c["email"] != "u1" || c["flow"] != nil {
		t.Fatalf("те же пользователи, без Vision: %v", c)
	}
}

func TestExitOverCDN(t *testing.T) {
	cfg := build(t, protocol.SyncResponse{
		Role:        protocol.RoleBridge,
		Reality:     protocol.Reality{SNI: "x.com"},
		Fingerprint: "firefox",
		Exits:       []protocol.Exit{{Address: "1.1.1.1", UUID: "link", PublicKey: "pk", SNI: "a.com", XHTTPPath: "/p", CDN: "cdn.a.com"}},
	})
	for _, o := range cfg["outbounds"].([]any) {
		ob := o.(map[string]any)
		if ob["tag"] != "exit-0" {
			continue
		}
		vnext := ob["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)
		st := ob["streamSettings"].(map[string]any)
		tls := st["tlsSettings"].(map[string]any)
		x := st["xhttpSettings"].(map[string]any)
		if vnext["address"] != "cdn.a.com" || vnext["port"] != float64(protocol.CDNPort) || st["security"] != "tls" ||
			tls["serverName"] != "cdn.a.com" || tls["fingerprint"] != "firefox" || x["mode"] != "packet-up" || x["host"] != "cdn.a.com" {
			t.Fatalf("через CDN: %v", ob)
		}
		if vnext["users"].([]any)[0].(map[string]any)["flow"] != nil {
			t.Fatal("Vision поверх XHTTP не работает")
		}
		return
	}
	t.Fatal("нет выхода на экзит")
}

func TestMieruInbound(t *testing.T) {
	find := func(cfg map[string]any) map[string]any {
		for _, in := range cfg["inbounds"].([]any) {
			if m := in.(map[string]any); m["tag"] == "mieru-in" {
				return m
			}
		}
		return nil
	}
	base := protocol.SyncResponse{Role: protocol.RoleBridge, Reality: protocol.Reality{SNI: "x.com"}}
	if find(build(t, base)) != nil {
		t.Fatal("без mieru входа нет")
	}
	base.Mieru = &protocol.Mieru{Ports: "40100", SocksUser: "kvn", SocksPass: "secret"}
	in := find(build(t, base))
	if in == nil || in["listen"] != "127.0.0.1" || in["port"] != float64(protocol.MieruSocksPort) || in["protocol"] != "socks" {
		t.Fatalf("локальный SOCKS для mita: %v", in)
	}
	acc := in["settings"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	if acc["user"] != "kvn" || acc["pass"] != "secret" {
		t.Fatal("с паролем")
	}
	// Трафик mita идёт в каскад вместе с остальными пользователями
	base.Exits = []protocol.Exit{{Address: "1.1.1.1", UUID: "link", PublicKey: "pk", SNI: "a.com"}}
	cfg := build(t, base)
	for _, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
		rule := r.(map[string]any)
		if tags, ok := rule["inboundTag"].([]any); ok {
			for _, tag := range tags {
				if tag == "mieru-in" {
					return
				}
			}
		}
	}
	t.Fatal("mieru-in должен идти через выходные ноды")
}
