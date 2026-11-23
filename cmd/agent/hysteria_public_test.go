package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestVerifyHy2Chain(t *testing.T) {
	now := time.Now()
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1), BasicConstraintsValid: true, IsCA: true,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
	}
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), DNSNames: []string{"node.example.com"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if _, err := verifyHy2Chain(cert, key, "node.example.com", now, roots); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyHy2Chain(cert, key, "wrong.example.com", now, roots); err == nil {
		t.Fatal("accepted a different domain")
	}
	if _, err := verifyHy2Chain(cert, key, "node.example.com", now.Add(2*time.Hour), roots); err == nil {
		t.Fatal("accepted an expired certificate")
	}
	if _, err := verifyHy2Chain(cert, key, "node.example.com", now, x509.NewCertPool()); err == nil {
		t.Fatal("accepted a chain without a trusted root")
	}
}
