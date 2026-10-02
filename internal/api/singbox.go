package api

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/xray"
)

// JSON отдаём только тем, кому он нужен: Karing получал JSON от старой панели RIXX
// (формат профиля после переезда не меняется), а официальные приложения sing-box
// base64 не читают вовсе. NekoBox, Exclave и Throne сами разбирают ссылки vless://,
// включая XHTTP, — им удобнее обычная подписка.
var singboxClients = []string{"karing", "sing-box", "singbox"}

// subFormat — "singbox" или "base64" по User-Agent; ?format=singbox|base64 перекрывает
func subFormat(ua, override string) string {
	switch override {
	case "singbox", "base64":
		return override
	}
	ua = strings.ToLower(ua)
	for _, name := range singboxClients {
		if strings.Contains(ua, name) {
			return "singbox"
		}
	}
	return "base64"
}

// supportsXHTTP — XHTTP есть в форке sing-box у Karing, в обычном sing-box его нет
func supportsXHTTP(ua string) bool {
	return strings.Contains(strings.ToLower(ua), "karing")
}

// singboxConfig — минимальный конфиг sing-box: только outbound'ы и маршрут.
// DNS и входы клиент настраивает сам — так конфиг не зависит от версии sing-box.
func singboxConfig(userID, title string, servers []subServer, withXHTTP bool) ([]byte, error) {
	type obj = map[string]any
	var proxies []obj
	var tags []string
	for _, srv := range servers {
		// XHTTP и mieru есть только в форке sing-box у Karing
		if (srv.XHTTPPath != "" || srv.MieruPorts != "") && !withXHTTP {
			continue
		}
		n := srv.Node
		if srv.Hy2 {
			// sing-box проверяет самоподписанный сертификат, если передать его целиком
			hy := obj{
				"type":        "hysteria2",
				"tag":         srv.Name,
				"server":      nodeAddress(n),
				"server_port": protocol.HysteriaPort,
				"password":    userID,
				"tls": obj{
					"enabled":     true,
					"server_name": hy2ServerName(n.Hy2Cert),
					"alpn":        []string{"h3"},
					"certificate": strings.Split(strings.TrimSpace(n.Hy2Cert), "\n"),
				},
			}
			if srv.Obfs != "" {
				hy["obfs"] = obj{"type": "salamander", "password": srv.Obfs}
			}
			proxies = append(proxies, hy)
			tags = append(tags, srv.Name)
			continue
		}
		if srv.MieruPorts != "" {
			// Имя и пароль mieru — ID пользователя (как email и ключ в Xray)
			mieru := obj{
				"type":         "mieru",
				"tag":          srv.Name,
				"server":       nodeAddress(n),
				"transport":    "TCP",
				"username":     userID,
				"password":     userID,
				"multiplexing": "MULTIPLEXING_LOW",
			}
			if strings.Contains(srv.MieruPorts, "-") {
				mieru["server_ports"] = []string{srv.MieruPorts} // клиент прыгает по портам
			} else {
				port, _ := strconv.Atoi(srv.MieruPorts)
				mieru["server_port"] = port
			}
			proxies = append(proxies, mieru)
			tags = append(tags, srv.Name)
			continue
		}
		if srv.CDN {
			// Через CDN: обычный TLS с проверкой настоящего сертификата CDN
			proxies = append(proxies, obj{
				"type":        "vless",
				"tag":         srv.Name,
				"server":      n.CDNDomain,
				"server_port": protocol.CDNPort,
				"uuid":        userID,
				"tls": obj{
					"enabled":     true,
					"server_name": n.CDNDomain,
					"alpn":        []string{"h2", "http/1.1"},
					"utls":        obj{"enabled": true, "fingerprint": srv.fingerprint()},
				},
				"transport": obj{"type": "xhttp", "host": n.CDNDomain, "path": srv.XHTTPPath, "mode": "packet-up"},
			})
			tags = append(tags, srv.Name)
			continue
		}
		out := obj{
			"type":        "vless",
			"tag":         srv.Name,
			"server":      nodeAddress(n),
			"server_port": 443,
			"uuid":        userID,
			"tls": obj{
				"enabled":     true,
				"server_name": n.SNI,
				"utls":        obj{"enabled": true, "fingerprint": srv.fingerprint()},
				"reality":     obj{"enabled": true, "public_key": n.PubKey, "short_id": n.SID},
			},
		}
		if srv.XHTTPPath != "" {
			out["transport"] = obj{"type": "xhttp", "path": srv.XHTTPPath, "mode": "auto"}
		} else {
			out["flow"] = xray.Flow
		}
		proxies = append(proxies, out)
		tags = append(tags, srv.Name)
	}

	outbounds := []obj{}
	final := "direct"
	if len(proxies) > 0 {
		// Ручной выбор сервера + «Авто»: клиент сам берёт самый быстрый из живых
		final = title
		outbounds = append(outbounds,
			obj{"type": "selector", "tag": title, "outbounds": append([]string{"auto"}, tags...), "default": "auto"},
			obj{"type": "urltest", "tag": "auto", "outbounds": tags, "url": xray.ProbeURL, "interval": "3m", "tolerance": 50},
		)
		for _, p := range proxies {
			outbounds = append(outbounds, p)
		}
	}
	outbounds = append(outbounds, obj{"type": "direct", "tag": "direct"})

	return json.MarshalIndent(obj{
		"outbounds": outbounds,
		"route": obj{
			"rules":                 []obj{{"ip_is_private": true, "outbound": "direct"}},
			"final":                 final,
			"auto_detect_interface": true,
		},
	}, "", "  ")
}
