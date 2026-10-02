package api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
)

// parseHy2Cert проверяет присланный агентом сертификат: base64 от одного PEM-блока
// CERTIFICATE, который действительно разбирается как X.509. Иначе — пусто (не сохраняем).
func parseHy2Cert(header string) string {
	if header == "" || len(header) > 8192 {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return ""
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return ""
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(block))
}

// hy2Pin — SHA-256 сертификата (hex): так клиенты «пинят» самоподписанный сервер
func hy2Pin(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

// hy2ServerName — имя сервера в сертификате Hysteria2 (SAN). Агент выпускает сертификат
// на SNI ноды, клиенты указывают его же — иначе sing-box (Karing) сертификат не примет.
func hy2ServerName(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.DNSNames) == 0 {
		return ""
	}
	return cert.DNSNames[0]
}
