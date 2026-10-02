package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
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
	// Xray после Xray-install работает от nobody — ключ должен быть ему доступен
	chownToXrayUser(keyFile)
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

// chownToXrayUser отдаёт файл пользователю, от которого работает Xray (nobody у Xray-install).
// Если такого пользователя нет — оставляем как есть: значит, Xray работает от root.
func chownToXrayUser(path string) {
	u, err := user.Lookup(envOr("XRAY_USER", "nobody"))
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	_ = os.Chown(path, uid, gid)
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
