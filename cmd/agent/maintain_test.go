package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeXray — скрипт вместо xray: сообщает версию и принимает любой конфиг
func fakeXray(version string) []byte {
	return []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray " + version + " (Xray, Penetrates Everything.)'; exit 0; fi\nexit 0\n")
}

// fakeSystemctl подменяет systemctl и возвращает файл, куда пишутся его вызовы
func fakeSystemctl(t *testing.T) string {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func testMaintainer(t *testing.T) *maintainer {
	m := newMaintainer()
	m.assetDir = t.TempDir()
	m.download = http.DefaultClient
	return m
}

func TestParseDgst(t *testing.T) {
	dgst := "MD5= 1\nSHA1= 2\nSHA2-256= ABCDEF\nSHA2-512= 3\n"
	if got := parseDgst([]byte(dgst)); got != "abcdef" {
		t.Fatalf("parseDgst = %q", got)
	}
	if parseDgst([]byte("garbage")) != "" {
		t.Fatal("без строки SHA2-256 хэша нет")
	}
}

func TestSelfUpdate(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("самообновление только для linux/amd64")
	}
	newBin := []byte("new agent")
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(protocol.NodeTokenHeader)
		w.Write(newBin)
	}))
	defer srv.Close()

	self := filepath.Join(t.TempDir(), "vpn-agent")
	os.WriteFile(self, []byte("old agent"), 0o755)
	m := testMaintainer(t)
	m.selfPath, m.selfSHA = self, sha([]byte("old agent"))
	var execed string
	m.execSelf = func(p string) error { execed = p; return nil }
	a := &agent{masterURL: srv.URL, token: "tok", maint: m, http: srv.Client(), pending: map[string]*protocol.UserTraffic{}}

	// Хэш не совпал — ничего не трогаем
	a.updateSelf(sha([]byte("something else")))
	if b, _ := os.ReadFile(self); string(b) != "old agent" || execed != "" {
		t.Fatal("бинарник с чужим хэшем не должен ставиться")
	}
	if !strings.Contains(m.lastErr(), "агент") {
		t.Fatalf("ошибка должна уйти в панель: %q", m.lastErr())
	}
	// После ошибки — пауза, а не попытка каждую минуту
	a.updateSelf(sha(newBin))
	if execed != "" {
		t.Fatal("повтор раньше паузы")
	}

	m.failedAt = map[string]time.Time{}
	a.updateSelf(sha(newBin))
	if b, _ := os.ReadFile(self); !bytes.Equal(b, newBin) {
		t.Fatal("новый бинарник не установлен")
	}
	if execed != self || gotToken != "tok" {
		t.Fatalf("перезапуск %q, токен %q", execed, gotToken)
	}

	// Тот же хэш — ничего не качаем
	execed = ""
	m.selfSHA = sha(newBin)
	a.updateSelf(sha(newBin))
	if execed != "" || m.lastErr() != "" {
		t.Fatal("при совпадении хэша обновляться нечего, ошибка должна очиститься")
	}
}

func xrayZip(t *testing.T, version string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string][]byte{"LICENSE": []byte("mpl"), "xray": fakeXray(version)} {
		w, _ := zw.Create(name)
		w.Write(body)
	}
	zw.Close()
	return buf.Bytes()
}

func TestUpdateXray(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("нужен sh")
	}
	calls := fakeSystemctl(t)
	archive := xrayZip(t, "26.9.1")
	dgst := []byte("SHA2-256= " + sha(archive) + "\n")
	asset, _ := xrayAsset()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v26.9.1/" + asset:
			w.Write(archive)
		case "/v26.9.1/" + asset + ".dgst":
			w.Write(dgst)
		case "/v26.9.2/" + asset:
			w.Write(archive) // .dgst не найдётся
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer func(u string) { xrayReleaseURL = u }(xrayReleaseURL)
	xrayReleaseURL = srv.URL

	bin := filepath.Join(t.TempDir(), "xray")
	os.WriteFile(bin, fakeXray("26.3.27"), 0o755)
	cfg := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(cfg, []byte("{}"), 0o644)
	m := testMaintainer(t)
	a := &agent{xrayBin: bin, configPath: cfg, maint: m}

	a.updateXray("26.9.2")
	if installedXray(bin) != "26.3.27" || m.lastErr() == "" {
		t.Fatal("без проверки хэша ставить нельзя, ошибка должна быть в панели")
	}

	m.failedAt = map[string]time.Time{}
	a.updateXray("26.9.1")
	if v := installedXray(bin); v != "26.9.1" {
		t.Fatalf("версия после обновления %q", v)
	}
	if m.lastErr() != "" {
		t.Fatalf("после успеха ошибки нет: %q", m.lastErr())
	}
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "restart xray") {
		t.Fatal("xray должен перезапуститься")
	}
	if _, err := os.Stat(bin + ".new"); !os.IsNotExist(err) {
		t.Fatal("временный файл остался")
	}
}

func TestUpdateXrayRejectedByConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("нужен sh")
	}
	fakeSystemctl(t)
	// Новый xray сообщает версию, но отвергает конфиг
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("xray")
	w.Write([]byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray 26.9.1 x'; exit 0; fi\necho 'Failed to start: bad config'; exit 23\n"))
	zw.Close()
	archive := buf.Bytes()
	asset, _ := xrayAsset()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".dgst") {
			fmt.Fprintf(w, "SHA2-256= %s\n", sha(archive))
			return
		}
		if strings.HasSuffix(r.URL.Path, asset) {
			w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	defer func(u string) { xrayReleaseURL = u }(xrayReleaseURL)
	xrayReleaseURL = srv.URL

	bin := filepath.Join(t.TempDir(), "xray")
	os.WriteFile(bin, fakeXray("26.3.27"), 0o755)
	a := &agent{xrayBin: bin, configPath: "/nonexistent.json", maint: testMaintainer(t)}
	a.updateXray("26.9.1")
	if installedXray(bin) != "26.3.27" {
		t.Fatal("версия, отвергшая конфиг, не должна ставиться")
	}
	if !strings.Contains(a.maint.lastErr(), "отвергла текущий конфиг") {
		t.Fatalf("ошибка: %q", a.maint.lastErr())
	}
}

func TestGeoDue(t *testing.T) {
	m := testMaintainer(t)
	night := time.Date(2026, 9, 30, 4, 0, 0, 0, time.Local)
	day := time.Date(2026, 9, 30, 14, 0, 0, 0, time.Local)
	m.now = func() time.Time { return day }
	if !m.geoDue() {
		t.Fatal("баз нет — нужно скачать сразу")
	}
	f := filepath.Join(m.assetDir, "geosite.dat")
	os.WriteFile(f, []byte("x"), 0o644)
	for _, c := range []struct {
		now time.Time
		age time.Duration
		due bool
	}{
		{day, 30 * time.Hour, false},  // днём ждём ночи
		{night, 30 * time.Hour, true}, // ночью обновляем
		{night, 5 * time.Hour, false}, // уже обновляли
		{day, 80 * time.Hour, true},   // давно не обновлялись — не ждём ночи
	} {
		os.Chtimes(f, c.now.Add(-c.age), c.now.Add(-c.age))
		m.now = func() time.Time { return c.now }
		if m.geoDue() != c.due {
			t.Fatalf("%v, возраст %v: ожидали %v", c.now, c.age, c.due)
		}
	}
}

func TestUpdateGeo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("нужен sh")
	}
	calls := fakeSystemctl(t)
	files := map[string][]byte{"geoip.dat": []byte("ip v2"), "geosite.dat": []byte("site v2")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if base, ok := strings.CutSuffix(name, ".sha256sum"); ok {
			fmt.Fprintf(w, "%s  %s\n", sha(files[base]), base)
			return
		}
		w.Write(files[name])
	}))
	defer srv.Close()
	defer func(u string) { geoBaseURL = u }(geoBaseURL)
	geoBaseURL = srv.URL

	bin := filepath.Join(t.TempDir(), "xray")
	os.WriteFile(bin, fakeXray("26.3.27"), 0o755)
	m := testMaintainer(t)
	old := time.Now().Add(-100 * time.Hour)
	for name := range files {
		p := filepath.Join(m.assetDir, name)
		os.WriteFile(p, []byte("v1"), 0o644)
		os.Chtimes(p, old, old)
	}
	a := &agent{xrayBin: bin, configPath: "/c.json", maint: m}
	a.updateGeo()
	for name, want := range files {
		if b, _ := os.ReadFile(filepath.Join(m.assetDir, name)); !bytes.Equal(b, want) {
			t.Fatalf("%s не обновлён", name)
		}
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "restart xray") != 1 {
		t.Fatalf("один перезапуск после обновления, вызовы: %q", b)
	}
	if m.geoUpdated() == "" || m.lastErr() != "" {
		t.Fatalf("дата %q, ошибка %q", m.geoUpdated(), m.lastErr())
	}
	entries, _ := os.ReadDir(m.assetDir)
	if len(entries) != 2 {
		t.Fatalf("временная папка должна удалиться: %v", entries)
	}

	// Базы те же — перезапуска нет, дата обновляется
	for name := range files {
		os.Chtimes(filepath.Join(m.assetDir, name), old, old)
	}
	a.updateGeo()
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "restart xray") != 1 {
		t.Fatal("без изменений xray не перезапускаем")
	}
	if st, _ := os.Stat(filepath.Join(m.assetDir, "geosite.dat")); time.Since(st.ModTime()) > time.Minute {
		t.Fatal("дата проверки должна обновиться")
	}

	// Подменённый файл не ставится
	files["geoip.dat"] = []byte("ip v3")
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			fmt.Fprintf(w, "%s  x\n", strings.Repeat("0", 64))
			return
		}
		w.Write(files[filepath.Base(r.URL.Path)])
	})
	for name := range files {
		os.Chtimes(filepath.Join(m.assetDir, name), old, old)
	}
	a.updateGeo()
	if b, _ := os.ReadFile(filepath.Join(m.assetDir, "geoip.dat")); string(b) != "ip v2" {
		t.Fatal("файл с неверным хэшем не должен ставиться")
	}
	if !strings.Contains(m.lastErr(), "геобазы") {
		t.Fatalf("ошибка: %q", m.lastErr())
	}
}
