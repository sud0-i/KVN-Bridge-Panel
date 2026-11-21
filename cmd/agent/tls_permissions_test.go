package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareXrayTLSKeyRepairsReusedKey(t *testing.T) {
	dir := t.TempDir()
	_, key, cert, err := ensureHy2Cert(dir, "www.example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XRAY_USER", "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	systemctl := fmt.Sprintf("#!/bin/sh\n[ \"$*\" = 'show -p User --value xray.service' ] || exit 1\necho %d\n", os.Geteuid())
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(systemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareXrayTLSKey(key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o", info.Mode().Perm())
	}
	actual, err := os.ReadFile(key)
	if err != nil || !bytes.Equal(actual, original) {
		t.Fatalf("key changed during permission repair: %v", err)
	}
	_, _, reused, err := ensureHy2Cert(dir, "www.example.com", time.Now())
	if err != nil || !bytes.Equal(reused, cert) {
		t.Fatalf("certificate changed during permission repair: %v", err)
	}
}

func TestApplyConfigStartsFailedXrayWithSameConfig(t *testing.T) {
	calls := fakeSystemctl(t)
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := []byte("{}")
	if err := os.WriteFile(path, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	a := &agent{configPath: path}
	if changed, err := a.applyConfig(cfg); err != nil || changed {
		t.Fatalf("starting failed unit: changed=%v, err=%v", changed, err)
	}
	commands, err := os.ReadFile(calls)
	if err != nil || strings.TrimSpace(string(commands)) != "is-failed --quiet xray\nstart xray" {
		t.Fatalf("unexpected systemctl calls: %q (%v)", commands, err)
	}
}
