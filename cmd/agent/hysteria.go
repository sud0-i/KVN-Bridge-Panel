package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Сертификат Hysteria2: самоподписанный, клиенты проверяют его по отпечатку (пину),
// поэтому домен ноде не нужен. Выпускается на SNI ноды — его же клиенты шлют в QUIC.
const (
	hy2CertName = "kvn-hy2.crt"
	hy2KeyName  = "kvn-hy2.key"
	// Перевыпускаем заранее: смена сертификата = новый пин, клиентам нужно обновить подписку
	hy2RenewBefore = 30 * 24 * time.Hour
)

// ensureHy2Cert возвращает пути к сертификату и ключу для serverName и сам сертификат (PEM).
// Существующий переиспользуется, если выпущен на то же имя и не истекает.
func ensureHy2Cert(dir, serverName string, now time.Time) (certFile, keyFile string, certPEM []byte, err error) {
	if serverName == "" {
		return "", "", nil, errors.New("нет имени сервера для сертификата Hysteria2")
	}
	return ensureCert(dir, hy2CertName, hy2KeyName, serverName, now)
}

// Сертификат входа для CDN: CDN в режиме «Full» принимает самоподписанный,
// снаружи клиенты видят настоящий сертификат CDN
const (
	cdnCertName = "kvn-cdn.crt"
	cdnKeyName  = "kvn-cdn.key"
)

func ensureCDNCert(dir, domain string, now time.Time) (certFile, keyFile string, err error) {
	if domain == "" {
		return "", "", errors.New("нет CDN-домена")
	}
	certFile, keyFile, _, err = ensureCert(dir, cdnCertName, cdnKeyName, domain, now)
	return certFile, keyFile, err
}

// ensureCert — самоподписанный ECDSA-сертификат на serverName в dir (переиспользует действующий)
func ensureCert(dir, certName, keyName, serverName string, now time.Time) (certFile, keyFile string, certPEM []byte, err error) {
	certFile = filepath.Join(dir, certName)
	keyFile = filepath.Join(dir, keyName)

	if pemBytes, err := os.ReadFile(certFile); err == nil {
		if _, kerr := os.Stat(keyFile); kerr == nil && hy2CertValid(pemBytes, serverName, now) {
			return certFile, keyFile, pemBytes, nil
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: serverName},
		DNSNames:     []string{serverName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", nil, err
	}
	// Ключ — сначала во временный файл с правами 0600, потом атомарно на место
	if err := writeFileAtomic(keyFile, keyPEM, 0o600); err != nil {
		return "", "", nil, err
	}
	if err := writeFileAtomic(certFile, certPEM, 0o644); err != nil {
		return "", "", nil, err
	}
	return certFile, keyFile, certPEM, nil
}

func hy2CertValid(pemBytes []byte, serverName string, now time.Time) bool {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.DNSNames) == 0 || cert.DNSNames[0] != serverName {
		return false
	}
	return now.Add(hy2RenewBefore).Before(cert.NotAfter)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// prepareXrayTLSKey also repairs keys left by a previous installation.
func prepareXrayTLSKey(path string) error {
	uid, err := xrayServiceUID()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ключ Xray %s должен быть обычным файлом", path)
	}
	if info.Mode().Perm() != 0o600 {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("права ключа Xray: %w", err)
		}
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) == uid {
		return nil
	}
	if err := os.Chown(path, uid, -1); err != nil {
		return fmt.Errorf("владелец ключа Xray (UID %d): %w", uid, err)
	}
	return nil
}

func xrayServiceUID() (int, error) {
	name := strings.TrimSpace(os.Getenv("XRAY_USER"))
	if name == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", "User", "--value", "xray.service").Output()
		if err != nil {
			return 0, fmt.Errorf("пользователь xray.service: %w", err)
		}
		name = strings.TrimSpace(string(out))
		if name == "" { // Без User= системная служба запускается от root.
			return 0, nil
		}
	}
	if uid, err := strconv.Atoi(name); err == nil && uid >= 0 {
		return uid, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, fmt.Errorf("пользователь Xray %q: %w", name, err)
	}
	return strconv.Atoi(u.Uid)
}

// openCDNPort открывает порт входа для CDN в ufw (если он включён). Ноды, развёрнутые
// до появления CDN, иначе не пустили бы CDN; новые открывают его при установке.
func (a *agent) openCDNPort() {
	if a.cdnPortOpen {
		return
	}
	if _, err := exec.LookPath("ufw"); err != nil {
		a.cdnPortOpen = true // фаервола нет — открывать нечего
		return
	}
	port := strconv.Itoa(protocol.CDNPort) + "/tcp"
	if out, err := exec.Command("ufw", "allow", port).CombinedOutput(); err != nil {
		log.Printf("⚠️ ufw allow %s: %v %s", port, err, out)
		return
	}
	a.cdnPortOpen = true
}
