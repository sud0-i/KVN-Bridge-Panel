package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// xrayError вытаскивает из вывода `xray run -test` суть ошибки: без заставки
// Xray и без повторяющегося префикса про загрузку файла конфига.
func xrayError(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	msg := strings.TrimSpace(lines[len(lines)-1])
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "Failed to start:") {
			msg = strings.TrimSpace(l)
		}
	}
	msg = strings.TrimPrefix(msg, "Failed to start: ")
	// «main: failed to load config files: [/path/config.next.json] > …» → «…»
	if i := strings.Index(msg, "] > "); i >= 0 && strings.Contains(msg[:i], "failed to load config files") {
		msg = msg[i+len("] > "):]
	}
	return msg
}

// checkSNI проверяет, что сайт, под который маскируется Reality, открывается с этой
// ноды по TLS 1.3 и HTTP/2. Без этого подключения к ноде будут рваться.
// Возвращает пустую строку, если всё в порядке.
func checkSNI(addr, serverName string) string {
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2", "http/1.1"},
		// Важна доступность и версия протокола, а не доверие к сертификату
		InsecureSkipVerify: true, //nolint:gosec
	})
	if err != nil {
		return fmt.Sprintf("%s недоступен по TLS 1.3 с этой ноды: %v", serverName, err)
	}
	defer conn.Close()
	if conn.ConnectionState().NegotiatedProtocol != "h2" {
		return fmt.Sprintf("%s не поддерживает HTTP/2 — выберите другой SNI", serverName)
	}
	return ""
}

// sniChecker кэширует результат проверки: делать TLS-рукопожатие каждую минуту незачем
type sniChecker struct {
	mu      sync.Mutex
	sni     string
	result  string
	checked time.Time
}

func (c *sniChecker) get(sni string, check func(addr, serverName string) string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sni != c.sni || time.Since(c.checked) > 10*time.Minute {
		c.sni = sni
		c.result = check(net.JoinHostPort(sni, "443"), sni)
		c.checked = time.Now()
	}
	return c.result
}

// warpDoctor объясняет, почему WARP недоступен, и не чаще раза в 10 минут
// пытается его поднять (зарегистрировать, перевести в режим прокси, подключить).
type warpDoctor struct {
	lastFix time.Time
}

func runWarpCli(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "warp-cli", append([]string{"--accept-tos"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// diagnose возвращает причину недоступности WARP (пустая строка — WARP работает)
func (w *warpDoctor) diagnose(available bool) string {
	if available {
		return ""
	}
	if _, err := exec.LookPath("warp-cli"); err != nil {
		return "клиент WARP не установлен"
	}
	status, _ := runWarpCli("status")

	if time.Since(w.lastFix) > 10*time.Minute {
		w.lastFix = time.Now()
		if strings.Contains(status, "Registration Missing") {
			runWarpCli("registration", "new")
		}
		runWarpCli("mode", "proxy")
		runWarpCli("proxy", "port", fmt.Sprint(protocol.WarpProxyPort))
		runWarpCli("connect")
		if warpAvailable() {
			return ""
		}
		status, _ = runWarpCli("status")
	}
	return "WARP: " + oneLine(status)
}

// oneLine склеивает многострочный вывод в одну строку для панели
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
