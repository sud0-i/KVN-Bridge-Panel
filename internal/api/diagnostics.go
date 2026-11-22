package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
)

var diagnosticSecrets = regexp.MustCompile(`(?i)https?://[^\s"'<>]+/sub/[^\s"'<>]+|(?:vless|vmess|trojan|hysteria2|hy2|ss|mierus?|xs)://[^\s"'<>]+|\b(?:authorization|password|(?:node[_-]?)?token|private[_-]?key|secret)\b["']?\s*[:=]\s*[^\r\n]+|\b[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}\b`)

func diagnosticText(s string) string {
	s = diagnosticSecrets.ReplaceAllString(s, "[redacted]")
	if strings.Contains(s, "PRIVATE KEY-----") {
		return "[redacted]"
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) > 800 {
		return string(r[:800]) + "…"
	}
	return string(r)
}

type diagnosticEvent struct {
	At      time.Time `json:"at"`
	Service string    `json:"service"`
	Message string    `json:"message"`
}

const diagnosticJournal = "journalctl --no-pager --quiet --since '24 hours ago' -n 100 --output=json --output-fields=MESSAGE,_SYSTEMD_UNIT,PRIORITY -u vpn-agent.service -u xray.service -u kvn-self-steal.service -u caddy.service -u warp-svc.service"

var journalProblem = regexp.MustCompile(`(?i)fail|error|ошиб|не удал|не удалось|отклонил|denied|timeout|timed out|refused`)

func (s *Server) diagnosticEvents(ctx context.Context, node models.Node) ([]diagnosticEvent, error) {
	client, err := s.sshDial(node)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	out, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := session.Start(diagnosticJournal); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(io.LimitReader(out, 256<<10))
	events := make([]diagnosticEvent, 0)
	for scanner.Scan() {
		var row struct {
			At       int64  `json:"__REALTIME_TIMESTAMP,string"`
			Service  string `json:"_SYSTEMD_UNIT"`
			Message  string `json:"MESSAGE"`
			Priority int    `json:"PRIORITY,string"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row.Service != "" && row.Message != "" &&
			(row.Priority <= 4 || journalProblem.MatchString(row.Message)) {
			events = append(events, diagnosticEvent{time.UnixMicro(row.At).UTC(), row.Service, diagnosticText(row.Message)})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := session.Wait(); err != nil {
		return nil, err
	}
	slices.Reverse(events)
	return events, nil
}

type diagnosticCheck struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	OK     bool   `json:"ok"`
}

func checkNodePort(ctx context.Context, name, address string) diagnosticCheck {
	dialer := net.Dialer{Timeout: 4 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return diagnosticCheck{Name: name, Result: diagnosticText(err.Error())}
	}
	conn.Close()
	return diagnosticCheck{Name: name, Result: address, OK: true}
}

func checkNodeSite(ctx context.Context, name, host, origin string) diagnosticCheck {
	dialer := net.Dialer{Timeout: 4 * time.Second}
	tr := &http.Transport{DisableKeepAlives: true}
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, origin)
	}
	client := &http.Client{
		Transport: tr, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+host+"/", nil)
	if err != nil {
		return diagnosticCheck{Name: name, Result: diagnosticText(err.Error())}
	}
	res, err := client.Do(req)
	if err != nil {
		return diagnosticCheck{Name: name, Result: diagnosticText(err.Error())}
	}
	defer res.Body.Close()
	result := res.Status
	if res.TLS != nil && len(res.TLS.PeerCertificates) > 0 {
		result += " · TLS " + res.TLS.PeerCertificates[0].NotAfter.UTC().Format(time.DateOnly)
	}
	return diagnosticCheck{Name: name, Result: result, OK: res.StatusCode >= 200 && res.StatusCode < 300}
}

func (s *Server) nodeDiagnostics(c echo.Context) error {
	var node models.Node
	if err := s.db.First(&node, "ip = ?", c.Param("ip")).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "Нода не найдена")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 15*time.Second)
	defer cancel()
	checks := make(chan diagnosticCheck, 4)
	count := 1
	go func() { checks <- checkNodePort(ctx, "tcp", net.JoinHostPort(node.IP, "443")) }()
	if validSelfStealDomain(node.SelfStealDomain) {
		count++
		go func() {
			checks <- checkNodeSite(ctx, "direct", node.SelfStealDomain, net.JoinHostPort(node.IP, "443"))
		}()
	}
	if validSelfStealDomain(node.CDNDomain) {
		count += 2
		go func() {
			checks <- checkNodePort(ctx, "cdn_tcp", net.JoinHostPort(node.CDNDomain, "8443"))
		}()
		go func() {
			checks <- checkNodeSite(ctx, "cdn", node.CDNDomain, net.JoinHostPort(node.CDNDomain, "443"))
		}()
	}
	result := make([]diagnosticCheck, 0, count)
	for range count {
		result = append(result, <-checks)
	}
	slices.SortFunc(result, func(a, b diagnosticCheck) int { return strings.Compare(a.Name, b.Name) })
	events := []diagnosticEvent{}
	issue := "ssh_key"
	if hasPanelKey(node) {
		logs, err := s.diagnosticEvents(ctx, node)
		if err == nil {
			events, issue = logs, ""
		} else {
			issue = diagnosticText(fmt.Sprintf("SSH: %v", err))
		}
	}
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"at": s.now(), "checks": result, "events": events, "journal_error": issue,
		"config_error": diagnosticText(node.ConfigError), "update_error": diagnosticText(node.UpdateError),
		"warp_error": diagnosticText(node.WarpError), "sni_error": diagnosticText(node.SNIError),
	})
}
