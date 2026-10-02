package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Соседи в 127.0.0.0/24 (на Linux весь 127/8 — локальный): настоящие TLS-серверы
// с сертификатами от тестового CA
func TestScanSubnet(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf := func(names ...string) tls.Certificate {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, _ := x509.CreateCertificate(rand.Reader, tpl, ca, &k.PublicKey, caKey)
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	serve := func(ip string, cfg *tls.Config) {
		l, err := tls.Listen("tcp", net.JoinHostPort(ip, port), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() { c.(*tls.Conn).Handshake(); c.Close() }()
			}
		}()
	}
	h2 := []string{"h2", "http/1.1"}
	serve("127.0.0.5", &tls.Config{Certificates: []tls.Certificate{leaf("good.example", "www.good.example")}, NextProtos: h2})
	serve("127.0.0.6", &tls.Config{Certificates: []tls.Certificate{leaf("elsewhere.example")}, NextProtos: h2})                           // DNS не сюда
	serve("127.0.0.7", &tls.Config{Certificates: []tls.Certificate{leaf("old.example")}, NextProtos: []string{"http/1.1"}})               // без HTTP/2
	serve("127.0.0.8", &tls.Config{Certificates: []tls.Certificate{leaf("tls12.example")}, NextProtos: h2, MaxVersion: tls.VersionTLS12}) // без TLS 1.3
	serve("127.0.0.9", &tls.Config{Certificates: []tls.Certificate{leaf("*.wild.example")}, NextProtos: h2})                              // только маска
	serve("127.0.0.1", &tls.Config{Certificates: []tls.Certificate{leaf("self.example")}, NextProtos: h2})                                // сама нода

	defer func(a func(string) string, r func(context.Context, string) ([]string, error), p *x509.CertPool) {
		scanAddr, scanResolve, scanRoots = a, r, p
	}(scanAddr, scanResolve, scanRoots)
	scanAddr = func(ip string) string { return net.JoinHostPort(ip, port) }
	scanRoots = roots
	dns := map[string][]string{"good.example": {"127.0.0.5"}, "www.good.example": {"10.0.0.1", "127.0.0.5"},
		"elsewhere.example": {"10.9.9.9"}, "old.example": {"127.0.0.7"}, "tls12.example": {"127.0.0.8"}, "self.example": {"127.0.0.1"}}
	scanResolve = func(_ context.Context, host string) ([]string, error) { return dns[host], nil }

	res := scanSubnet(context.Background(), "127.0.0.1")
	if res.Error != "" || res.Subnet != "127.0.0.0/24" {
		t.Fatalf("%+v", res)
	}
	var names []string
	for _, c := range res.Found {
		names = append(names, c.SNI)
		if c.IP != "127.0.0.5" || c.MS < 1 {
			t.Fatalf("кандидат %+v", c)
		}
	}
	if strings.Join(names, ",") != "good.example,www.good.example" && strings.Join(names, ",") != "www.good.example,good.example" {
		t.Fatalf("кандидаты: %v", names)
	}
	// Ответили TLS 1.3: .5, .6, .7, .9 (сама нода и TLS 1.2 не в счёт)
	if res.Scanned != 4 {
		t.Fatalf("ответили %d", res.Scanned)
	}

	if r := scanSubnet(context.Background(), "2001:db8::1"); !strings.Contains(r.Error, "IPv4") {
		t.Fatal("IPv6 не сканируем")
	}

	// Итог уходит мастеру в заголовке
	var s sniScanner
	if s.header() != "" {
		t.Fatal("до сканирования заголовка нет")
	}
	if err := s.scan("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	raw, _ := url.QueryUnescape(s.header())
	var got protocol.SNIScan
	if json.Unmarshal([]byte(raw), &got) != nil || len(got.Found) != 2 {
		t.Fatalf("заголовок: %s", raw)
	}
}
