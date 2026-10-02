// Package xray собирает config.json для Xray из ответа Мастера.
package xray

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

const (
	Flow = "xtls-rprx-vision"
	// APIPort — локальный порт gRPC API Xray (статистика). Слушает только 127.0.0.1.
	APIPort = 10085
	// DefaultFingerprint — отпечаток TLS по умолчанию (имитация Chrome через uTLS)
	DefaultFingerprint = "chrome"
	// ProbeURL — по нему мост регулярно проверяет, что выходная нода выпускает в интернет.
	// Не Google: geosite:google часто уходит через WARP, и сбой WARP «убил» бы все ноды.
	ProbeURL = "https://cp.cloudflare.com/generate_204"
	// xhttpSocket — внутренний (abstract unix) сокет XHTTP-входа. Снаружи его не видно:
	// XHTTP-клиенты приходят на тот же 443, проходят Reality и попадают сюда через fallback.
	xhttpSocket = "@kvn-xhttp"
)

type obj = map[string]any

// Build возвращает готовый config.json для роли ноды
func Build(s protocol.SyncResponse, privateKey string) ([]byte, error) {
	if privateKey == "" {
		return nil, fmt.Errorf("пустой приватный ключ Reality")
	}
	if s.Reality.SNI == "" {
		return nil, fmt.Errorf("мастер не прислал SNI")
	}

	dest := s.Reality.Dest
	if dest == "" {
		dest = s.Reality.SNI + ":443"
	}

	clients := make([]obj, 0, len(s.Clients))
	for _, c := range s.Clients {
		clients = append(clients, obj{"id": c.ID, "email": c.Email, "flow": Flow})
	}

	inbound := obj{
		"tag":      "vless-in",
		"port":     443,
		"protocol": "vless",
		"settings": obj{"clients": clients, "decryption": "none"},
		"streamSettings": obj{
			"network":  "tcp",
			"security": "reality",
			"realitySettings": obj{
				"show":        false,
				"dest":        dest,
				"xver":        s.Reality.Xver,
				"serverNames": []string{s.Reality.SNI},
				"privateKey":  privateKey,
				"shortIds":    []string{s.Reality.ShortID},
			},
		},
		// Сниффинг нужен, чтобы маршрутизировать по доменам (geosite)
		"sniffing": obj{
			"enabled":      true,
			"destOverride": []string{"http", "tls", "quic"},
			"routeOnly":    true,
		},
	}

	inbounds := []obj{inbound, {
		"tag":      "api",
		"listen":   "127.0.0.1",
		"port":     APIPort,
		"protocol": "dokodemo-door",
		"settings": obj{"address": "127.0.0.1"},
	}}
	userInbounds := []string{"vless-in"}

	if s.XHTTPPath != "" {
		// Запасной транспорт: подключения, которые прошли Reality, но не являются
		// VLESS поверх TCP (это XHTTP), Xray передаёт на внутренний XHTTP-вход
		inbound["settings"].(obj)["fallbacks"] = []obj{{"dest": xhttpSocket}}

		xhttpClients := make([]obj, 0, len(s.Clients))
		for _, c := range s.Clients {
			// XTLS Vision работает только поверх TCP — у XHTTP-клиентов flow нет
			xhttpClients = append(xhttpClients, obj{"id": c.ID, "email": c.Email})
		}
		inbounds = append(inbounds, obj{
			"tag":      "xhttp-in",
			"listen":   xhttpSocket,
			"protocol": "vless",
			"settings": obj{"clients": xhttpClients, "decryption": "none"},
			"streamSettings": obj{
				"network":       "xhttp",
				"xhttpSettings": obj{"path": s.XHTTPPath},
			},
			"sniffing": inbound["sniffing"],
		})
		userInbounds = append(userInbounds, "xhttp-in")
	}

	// Hysteria2 на UDP 443 (рядом с TCP 443 Reality). Сертификат самоподписанный,
	// клиенты проверяют его по отпечатку. Пароль пользователя — его ID, как у VLESS,
	// email тот же — трафик считается в ту же статистику.
	if h := s.Hysteria; h != nil && h.CertFile != "" && h.KeyFile != "" {
		hyClients := make([]obj, 0, len(s.Clients))
		for _, c := range s.Clients {
			hyClients = append(hyClients, obj{"auth": c.ID, "email": c.Email})
		}
		stream := obj{
			"network":  "hysteria",
			"security": "tls",
			"tlsSettings": obj{
				"alpn":         []string{"h3"},
				"certificates": []obj{{"certificateFile": h.CertFile, "keyFile": h.KeyFile}},
			},
			"hysteriaSettings": obj{"version": 2},
		}
		if h.Obfs != "" {
			// salamander: пакеты не похожи на QUIC/HTTP3 — выглядят как случайный шум
			stream["finalmask"] = obj{"udp": []obj{{"type": "salamander", "settings": obj{"password": h.Obfs}}}}
		}
		inbounds = append(inbounds, obj{
			"tag":            "hy2-in",
			"listen":         "0.0.0.0",
			"port":           protocol.HysteriaPort,
			"protocol":       "hysteria",
			"settings":       obj{"version": 2, "clients": hyClients},
			"streamSettings": stream,
			"sniffing":       inbound["sniffing"],
		})
		userInbounds = append(userInbounds, "hy2-in")
	}

	// Вход для CDN: XHTTP поверх обычного TLS на CDNPort. Reality здесь не подходит —
	// TLS с клиентом держит CDN, а к ноде он приходит своим соединением.
	if c := s.CDN; c != nil && c.CertFile != "" && c.KeyFile != "" && c.Path != "" {
		cdnClients := make([]obj, 0, len(s.Clients))
		for _, cl := range s.Clients {
			cdnClients = append(cdnClients, obj{"id": cl.ID, "email": cl.Email})
		}
		inbounds = append(inbounds, obj{
			"tag":      "cdn-in",
			"listen":   "0.0.0.0",
			"port":     protocol.CDNPort,
			"protocol": "vless",
			"settings": obj{"clients": cdnClients, "decryption": "none"},
			"streamSettings": obj{
				"network":  "xhttp",
				"security": "tls",
				"tlsSettings": obj{
					"alpn":         []string{"h2", "http/1.1"},
					"certificates": []obj{{"certificateFile": c.CertFile, "keyFile": c.KeyFile}},
				},
				"xhttpSettings": obj{"path": c.Path, "mode": "auto"},
			},
			"sniffing": inbound["sniffing"],
		})
		userInbounds = append(userInbounds, "cdn-in")
	}

	// Трафик mieru: mita выпускает его в этот локальный SOCKS-вход, дальше — как у всех
	if m := s.Mieru; m != nil && m.SocksUser != "" && m.SocksPass != "" {
		inbounds = append(inbounds, obj{
			"tag":      "mieru-in",
			"listen":   "127.0.0.1",
			"port":     protocol.MieruSocksPort,
			"protocol": "socks",
			"settings": obj{
				"auth":     "password",
				"accounts": []obj{{"user": m.SocksUser, "pass": m.SocksPass}},
				"udp":      true,
			},
			"sniffing": inbound["sniffing"],
		})
		userInbounds = append(userInbounds, "mieru-in")
	}

	rules := []obj{
		{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
		{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "block"},
	}
	outbounds := []obj{
		{"tag": "direct", "protocol": "freedom"},
		{"tag": "block", "protocol": "blackhole"},
	}
	var balancers []obj
	var observatory obj

	switch s.Role {
	case protocol.RoleBridge:
		// Что указано в настройках — выпускаем с моста напрямую, минуя выходные ноды
		domains, ips := splitRules(s.Direct)
		if len(domains) > 0 {
			rules = append(rules, obj{"type": "field", "domain": domains, "outboundTag": "direct"})
		}
		if len(ips) > 0 {
			rules = append(rules, obj{"type": "field", "ip": ips, "outboundTag": "direct"})
		}

		if len(s.Exits) > 0 {
			for i, e := range s.Exits {
				outbounds = append(outbounds, exitOutbound(fmt.Sprintf("exit-%d", i), e, s.Fingerprint))
			}
			// Случайная живая выходная нода. Живость проверяет observatory; Xray учитывает
			// её только при заданном fallbackTag. Если мертвы все — блокируем, а не пускаем
			// напрямую: иначе трафик молча уйдёт с IP моста (outbound по умолчанию — direct).
			//
			// Проверка «burst»: нода мертва, только если провалились ВСЕ проверки за последние
			// ~2 минуты. Обычный observatory считал мёртвой после одного сбоя — а на участке
			// РФ → заграница DPI иногда сбрасывает отдельные соединения, и при одной выходной
			// ноде мост из-за единичного сброса блокировал весь трафик.
			balancers = append(balancers, obj{
				"tag":         "exits",
				"selector":    []string{"exit-"},
				"strategy":    obj{"type": "random"},
				"fallbackTag": "block",
			})
			observatory = obj{
				"subjectSelector": []string{"exit-"},
				"pingConfig": obj{
					"destination": ProbeURL,
					"interval":    "10s", // в среднем одна проверка на ноду раз в 10 секунд
					"sampling":    6,     // решение — по последним 6 проверкам (окно ~2 минуты)
					"timeout":     "5s",
				},
			}
			rules = append(rules, obj{
				"type":        "field",
				"inboundTag":  userInbounds,
				"balancerTag": "exits",
			})
		}
		// Если выходных нод нет — трафик уйдёт в первый outbound ("direct"), а правила
		// «через WARP» мост выполняет сам (одиночный режим)
		if len(s.Exits) == 0 {
			outbounds, rules = addWarp(outbounds, rules, s.Warp)
		}
	case protocol.RoleExit:
		// Выбранное выпускаем через WARP (локальный SOCKS), остальное — с IP ноды
		outbounds, rules = addWarp(outbounds, rules, s.Warp)
	default:
		return nil, fmt.Errorf("неизвестная роль ноды: %q", s.Role)
	}

	routing := obj{"domainStrategy": "IPIfNonMatch", "rules": rules}

	if len(balancers) > 0 {
		routing["balancers"] = balancers
	}

	cfg := obj{
		"log": obj{
			"loglevel": "warning",
			"access":   "none",
		},
		"stats": obj{},
		"api":   obj{"tag": "api", "services": []string{"StatsService", "HandlerService"}},
		"policy": obj{
			"levels": obj{"0": obj{"statsUserUplink": true, "statsUserDownlink": true}},
		},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing":   routing,
	}
	if observatory != nil {
		cfg["burstObservatory"] = observatory
	}

	return json.MarshalIndent(cfg, "", "  ")
}

// addWarp выпускает трафик по правилам через локальный прокси Cloudflare WARP
func addWarp(outbounds, rules []obj, warp []string) ([]obj, []obj) {
	if len(warp) == 0 {
		return outbounds, rules
	}
	outbounds = append(outbounds, obj{
		"tag":      "warp",
		"protocol": "socks",
		"settings": obj{"servers": []obj{{"address": "127.0.0.1", "port": protocol.WarpProxyPort}}},
	})
	domains, ips := splitRules(warp)
	if len(domains) > 0 {
		rules = append(rules, obj{"type": "field", "domain": domains, "outboundTag": "warp"})
	}
	if len(ips) > 0 {
		rules = append(rules, obj{"type": "field", "ip": ips, "outboundTag": "warp"})
	}
	return outbounds, rules
}

// splitRules делит правила на доменные и IP-шные: так их различает Xray
func splitRules(list []string) (domains, ips []string) {
	for _, v := range list {
		v = strings.TrimSpace(v)
		switch {
		case v == "":
		case strings.HasPrefix(v, "geoip:"), strings.Contains(v, "/"), net.ParseIP(v) != nil:
			ips = append(ips, v)
		default:
			domains = append(domains, v)
		}
	}
	return domains, ips
}

func exitOutbound(tag string, e protocol.Exit, fingerprint string) obj {
	if fingerprint == "" {
		fingerprint = DefaultFingerprint
	}
	port := e.Port
	if port == 0 {
		port = 443
	}
	user := obj{"id": e.UUID, "encryption": "none", "flow": Flow}
	stream := obj{
		"network":  "tcp",
		"security": "reality",
		"realitySettings": obj{
			"serverName":  e.SNI,
			"publicKey":   e.PublicKey,
			"shortId":     e.ShortID,
			"fingerprint": fingerprint,
		},
	}
	if e.CDN != "" && e.XHTTPPath != "" {
		// Через CDN: обычный TLS до CDN (сертификат настоящий — проверяем), дальше CDN
		// сам ходит на CDNPort экзита. packet-up — режим, который проходит через любой CDN.
		delete(user, "flow")
		return obj{
			"tag":      tag,
			"protocol": "vless",
			"settings": obj{"vnext": []obj{{"address": e.CDN, "port": protocol.CDNPort, "users": []obj{user}}}},
			"streamSettings": obj{
				"network":  "xhttp",
				"security": "tls",
				"tlsSettings": obj{
					"serverName":  e.CDN,
					"fingerprint": fingerprint,
					"alpn":        []string{"h2", "http/1.1"},
				},
				"xhttpSettings": obj{
					"host": e.CDN,
					"path": e.XHTTPPath,
					"mode": "packet-up",
					"xmux": obj{"maxConcurrency": "16-32"},
				},
			},
		}
	}
	if e.XHTTPPath != "" {
		// XHTTP на тот же 443 экзита: Reality → fallback на его XHTTP-вход.
		// Vision поверх XHTTP не работает — flow не указываем.
		delete(user, "flow")
		stream["network"] = "xhttp"
		stream["xhttpSettings"] = obj{
			"path": e.XHTTPPath,
			"mode": "auto",
			// Через мост идут сотни потоков разных пользователей: по умолчанию xmux держит
			// один поток на соединение, и выигрыша нет. 16–32 потока на соединение — и
			// рукопожатие Reality с экзитом (десятки-сотни мс) не повторяется на каждый сайт.
			// Проверено вживую: 30 запросов подряд — 2 соединения вместо 30.
			"xmux": obj{"maxConcurrency": "16-32"},
		}
	}
	return obj{
		"tag":      tag,
		"protocol": "vless",
		"settings": obj{
			"vnext": []obj{{
				"address": e.Address,
				"port":    port,
				"users":   []obj{user},
			}},
		},
		"streamSettings": stream,
	}
}
