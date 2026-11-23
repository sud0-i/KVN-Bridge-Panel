package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

const publicHy2Prefix = "kvn-hy2-public-"

type hy2Certificate struct {
	certFile, keyFile string
	leaf              []byte
	name              string
}

func caddyCertificateRoot(inDocker bool) (string, error) {
	if !inDocker {
		return "/var/lib/kvn-self-steal/caddy/certificates", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const mount = `{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}`
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", mount, "kvn-caddy").Output()
	if err != nil {
		return "", fmt.Errorf("хранилище Caddy: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		return "", errors.New("у контейнера Caddy нет тома /data")
	}
	return filepath.Join(path, "caddy", "certificates"), nil
}

func verifyHy2Chain(cert, key []byte, name string, now time.Time, roots *x509.CertPool) (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		intermediates.AddCert(c)
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: name, CurrentTime: now, Roots: roots, Intermediates: intermediates})
	return leaf, err
}

func stagePublicHy2(root, dir, name string, now time.Time) (hy2Certificate, error) {
	if name == "" || strings.ContainsAny(name, "/\\*?[") || filepath.Base(name) != name {
		return hy2Certificate{}, errors.New("некорректный домен сертификата")
	}
	// Caddy uses one directory per issuer. Use the newest valid chain for this host.
	paths, err := filepath.Glob(filepath.Join(root, "*", name, name+".crt"))
	if err != nil {
		return hy2Certificate{}, err
	}
	var cert, key []byte
	var newest *x509.Certificate
	for _, path := range paths {
		c, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		k, err := os.ReadFile(strings.TrimSuffix(path, ".crt") + ".key")
		if err != nil {
			continue
		}
		leaf, err := verifyHy2Chain(c, k, name, now, nil)
		if err == nil && (newest == nil || leaf.NotAfter.After(newest.NotAfter)) {
			cert, key, newest = c, k, leaf
		}
	}
	if newest == nil {
		return hy2Certificate{}, fmt.Errorf("Caddy ещё не выдал действующий доверенный сертификат для %s", name)
	}
	digest := sha256.Sum256(cert)
	base := filepath.Join(dir, fmt.Sprintf("%s%x", publicHy2Prefix, digest[:16]))
	bundle := hy2Certificate{base + ".crt", base + ".key",
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newest.Raw}), name}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return hy2Certificate{}, err
	}
	// A new filename makes the Xray config change at renewal, while keeping the
	// previous pair in place until the new configuration has started.
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{bundle.certFile, cert, 0o644}, {bundle.keyFile, key, 0o600}} {
		if old, err := os.ReadFile(f.path); err != nil || !bytes.Equal(old, f.data) {
			if err := writeFileAtomic(f.path, f.data, f.mode); err != nil {
				return hy2Certificate{}, err
			}
		}
	}
	if err := prepareXrayTLSKey(bundle.keyFile); err != nil {
		return hy2Certificate{}, err
	}
	return bundle, nil
}

func activePublicHy2(configPath, name string) hy2Certificate {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return hy2Certificate{}
	}
	var cfg struct {
		Inbounds []struct {
			Tag            string
			StreamSettings struct {
				TLSSettings struct {
					Certificates []struct{ CertificateFile, KeyFile string }
				}
			}
		}
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return hy2Certificate{}
	}
	for _, in := range cfg.Inbounds {
		if in.Tag != "hy2-in" || len(in.StreamSettings.TLSSettings.Certificates) != 1 {
			continue
		}
		c := in.StreamSettings.TLSSettings.Certificates[0]
		if filepath.Dir(c.CertificateFile) != filepath.Dir(configPath) ||
			!strings.HasPrefix(filepath.Base(c.CertificateFile), publicHy2Prefix) {
			continue
		}
		chain, err := os.ReadFile(c.CertificateFile)
		if err != nil {
			continue
		}
		block, _ := pem.Decode(chain)
		if block != nil && block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err == nil && cert.VerifyHostname(name) == nil {
				return hy2Certificate{c.CertificateFile, c.KeyFile, pem.EncodeToMemory(block), name}
			}
		}
	}
	return hy2Certificate{}
}

func (a *agent) configureHy2(h *protocol.Hysteria, sni string) (hy2Certificate, error) {
	dir := filepath.Dir(a.configPath)
	if h.PublicTLSName != "" {
		root, err := caddyCertificateRoot(h.CaddyInDocker)
		if err == nil {
			var bundle hy2Certificate
			bundle, err = stagePublicHy2(root, dir, h.PublicTLSName, time.Now())
			if err == nil {
				return bundle, nil
			}
		}
		a.hy2TLSError = err.Error()
		if current := activePublicHy2(a.configPath, h.PublicTLSName); current.certFile != "" {
			return current, nil
		}
	}
	cert, key, leaf, err := ensureHy2Cert(dir, sni, time.Now())
	if err == nil {
		err = prepareXrayTLSKey(key)
	}
	return hy2Certificate{certFile: cert, keyFile: key, leaf: leaf}, err
}

func (a *agent) commitHy2(bundle hy2Certificate) {
	if bundle.name != "" && exec.Command("systemctl", "is-active", "--quiet", "xray").Run() != nil {
		a.hy2TLSError = "Xray ещё не подтвердил запуск с доверенным сертификатом Hysteria 2"
		return
	}
	a.hy2Cert, a.hy2TrustedName = bundle.leaf, bundle.name
	if bundle.certFile == "" {
		return
	}
	dir := filepath.Dir(a.configPath)
	old, _ := filepath.Glob(filepath.Join(dir, publicHy2Prefix+"*"))
	for _, path := range old {
		if path != bundle.certFile && path != bundle.keyFile &&
			(filepath.Ext(path) == ".crt" || filepath.Ext(path) == ".key") {
			_ = os.Remove(path)
		}
	}
}
