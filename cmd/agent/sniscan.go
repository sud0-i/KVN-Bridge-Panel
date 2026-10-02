package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Подбор SNI для Reality: лучше всего маскироваться под сайт, который живёт рядом —
// в той же /24 у того же хостера. Тогда IP, сертификат и задержка выглядят правдоподобно.
// Агент по команде из панели один раз обходит соседние адреса на порту 443 (не больше
// 10 подключений одновременно) и отбирает сайты, которые годятся Reality:
// TLS 1.3 с X25519, HTTP/2, настоящий сертификат на имя, и имя указывает на этот же адрес.

var (
	// Подменяются в тестах: куда подключаться к соседу, как резолвить имя, каким корням верить
	scanAddr    = func(ip string) string { return net.JoinHostPort(ip, "443") }
	scanResolve = func(ctx context.Context, host string) ([]string, error) {
		return net.DefaultResolver.LookupHost(ctx, host)
	}
	scanRoots *x509.CertPool // nil — системные корни
)

const (
	scanWorkers  = 10
	scanTimeout  = 2 * time.Second
	scanKeep     = 10               // сколько кандидатов показать
	scanFreshFor = 30 * time.Minute // столько агент повторяет итог мастеру
)

var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

type sniScanner struct {
	mu   sync.Mutex
	last *protocol.SNIScan
}

func (s *sniScanner) header() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil || time.Since(s.last.At) > scanFreshFor {
		return ""
	}
	raw, _ := json.Marshal(s.last)
	return url.QueryEscape(string(raw))
}

// scanSNI обходит /24 вокруг nodeIP и запоминает лучших кандидатов
func (s *sniScanner) scan(nodeIP string) error {
	res := scanSubnet(context.Background(), nodeIP)
	s.mu.Lock()
	s.last = &res
	s.mu.Unlock()
	if res.Error != "" {
		return fmt.Errorf("%s", res.Error)
	}
	log.Printf("🔎 Подбор SNI: %s — ответили %d, подходят %d", res.Subnet, res.Scanned, len(res.Found))
	return nil
}

func scanSubnet(ctx context.Context, nodeIP string) protocol.SNIScan {
	res := protocol.SNIScan{At: time.Now().UTC(), Found: []protocol.SNICandidate{}}
	ip := net.ParseIP(nodeIP).To4()
	if ip == nil {
		res.Error = "подбор работает только для IPv4-адреса ноды"
		return res
	}
	res.Subnet = fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		scanned int
		found   = map[string]protocol.SNICandidate{}
	)
	jobs := make(chan string)
	for w := 0; w < scanWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for addr := range jobs {
				answered, cands := probeNeighbour(ctx, addr)
				mu.Lock()
				if answered {
					scanned++
				}
				for _, c := range cands {
					if old, ok := found[c.SNI]; !ok || c.MS < old.MS {
						found[c.SNI] = c
					}
				}
				mu.Unlock()
			}
		}()
	}
	for last := 1; last <= 254; last++ {
		if byte(last) == ip[3] {
			continue // себя не проверяем
		}
		select {
		case jobs <- fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], last):
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()

	res.Scanned = scanned
	for _, c := range found {
		res.Found = append(res.Found, c)
	}
	sort.Slice(res.Found, func(i, j int) bool {
		if res.Found[i].MS != res.Found[j].MS {
			return res.Found[i].MS < res.Found[j].MS
		}
		return res.Found[i].SNI < res.Found[j].SNI
	})
	if len(res.Found) > scanKeep {
		res.Found = res.Found[:scanKeep]
	}
	return res
}

// realityTLS — требования Reality к сайту маскировки
func realityTLS(serverName string, verify bool) *tls.Config {
	return &tls.Config{
		ServerName:         serverName,
		MinVersion:         tls.VersionTLS13,
		CurvePreferences:   []tls.CurveID{tls.X25519},
		NextProtos:         []string{"h2", "http/1.1"},
		RootCAs:            scanRoots,
		InsecureSkipVerify: !verify, //nolint:gosec // первое рукопожатие — только узнать имена из сертификата
	}
}

func handshake(ctx context.Context, ip string, cfg *tls.Config) (*tls.ConnectionState, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	start := time.Now()
	d := tls.Dialer{Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", scanAddr(ip))
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	st := conn.(*tls.Conn).ConnectionState()
	return &st, time.Since(start), nil
}

// probeNeighbour: answered — на 443 отвечает TLS 1.3; cands — подходящие имена с этого адреса
func probeNeighbour(ctx context.Context, ip string) (answered bool, cands []protocol.SNICandidate) {
	st, _, err := handshake(ctx, ip, realityTLS("", false))
	if err != nil || len(st.PeerCertificates) == 0 {
		return false, nil
	}
	tried := 0
	for _, name := range certNames(st.PeerCertificates[0]) {
		if tried == 3 {
			break
		}
		tried++
		// Имя должно указывать на этот же адрес: Reality ходит к сайту по DNS
		rctx, cancel := context.WithTimeout(ctx, scanTimeout)
		addrs, err := scanResolve(rctx, name)
		cancel()
		if err != nil || !contains(addrs, ip) {
			continue
		}
		st, took, err := handshake(ctx, ip, realityTLS(name, true))
		if err != nil || st.NegotiatedProtocol != "h2" {
			continue
		}
		ms := int(took / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		cands = append(cands, protocol.SNICandidate{SNI: name, IP: ip, MS: ms})
	}
	return true, cands
}

// certNames — имена из сертификата, годные в SNI: без масок, только домены
func certNames(cert *x509.Certificate) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append([]string{cert.Subject.CommonName}, cert.DNSNames...) {
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		if n == "" || strings.Contains(n, "*") || seen[n] || !hostRe.MatchString(n) {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
