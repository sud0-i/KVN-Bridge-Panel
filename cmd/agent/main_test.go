package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/xray"
)

func TestTempConfigPath(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/etc/xray/config.json": "/usr/local/etc/xray/config.next.json",
		"/etc/xray/custom":                "/etc/xray/custom.next.json",
	} {
		got := tempConfigPath(in)
		if got != want {
			t.Errorf("tempConfigPath(%q) = %q, ожидали %q", in, got, want)
		}
		// Xray определяет формат по расширению — без .json он отвергает файл
		if !strings.HasSuffix(got, ".json") {
			t.Errorf("временный конфиг %q не заканчивается на .json", got)
		}
	}
}

// Настоящая проверка конфига через Xray. Запускается, если задан XRAY_BIN
// (и XRAY_LOCATION_ASSET с geoip.dat/geosite.dat), иначе пропускается.
func TestXrayAcceptsTempConfig(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("XRAY_BIN не задан")
	}
	// Мост с выходными нодами: балансировщик, observatory, XHTTP — всё, что Xray должен принять
	cfg, err := xray.Build(protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "www.example.com", ShortID: "abcd"},
		Clients: []protocol.Client{{ID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", Email: "u1"}},
		Exits: []protocol.Exit{
			{Address: "192.0.2.1", Port: 443, UUID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6e", PublicKey: "Y0pYqLnvQxvvmUx4XnzMn7bJuSRdpcJHdXXC9XnMKwM", SNI: "a.com", ShortID: "01"},
			{Address: "192.0.2.2", Port: 443, UUID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6e", PublicKey: "Y0pYqLnvQxvvmUx4XnzMn7bJuSRdpcJHdXXC9XnMKwM", SNI: "b.com", ShortID: "02"},
		},
		XHTTPPath: "/secret",
	}, "2KZ7s3Gn8dq3ZNUB-Knd0t7ptZ7g8kpRDFTXlvCp1mc")
	if err != nil {
		t.Fatal(err)
	}
	tmp := tempConfigPath(filepath.Join(t.TempDir(), "config.json"))
	if err := os.WriteFile(tmp, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "run", "-test", "-c", tmp).CombinedOutput(); err != nil {
		t.Fatalf("xray отверг конфиг: %v\n%s", err, out)
	}

	// Одиночный режим: мост без выходных нод, правила через WARP
	single, err := xray.Build(protocol.SyncResponse{
		Role:    protocol.RoleBridge,
		Reality: protocol.Reality{SNI: "panel.example.com", ShortID: "abcd", Dest: "127.0.0.1:8443", Xver: 1},
		Clients: []protocol.Client{{ID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", Email: "u1"}},
		Warp:    []string{"geosite:openai", "geosite:category-ru", "geoip:ru"},
	}, "2KZ7s3Gn8dq3ZNUB-Knd0t7ptZ7g8kpRDFTXlvCp1mc")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(tmp, single, 0o644)
	if out, err := exec.Command(bin, "run", "-test", "-c", tmp).CombinedOutput(); err != nil {
		t.Fatalf("xray отверг одиночный конфиг с WARP: %v\n%s", err, out)
	}
}

func TestXrayError(t *testing.T) {
	out := []byte(`Xray 26.3.27 (Xray, Penetrates Everything.) d2758a0 (go1.26.1 linux/amd64)
A unified platform for anti-censorship.
2026/09/30 04:16:01.641551 [Info] infra/conf/serial: Reading config: &{Name:/usr/local/etc/xray/config.next.json Format:json}
Failed to start: main: failed to load config files: [/usr/local/etc/xray/config.next.json] > infra/conf: failed to build routing configuration > infra/conf: invalid field rule > infra/conf: failed to parse domain rule: geosite:nope > infra/conf: failed to load geosite: NOPE`)
	got := xrayError(out)
	want := "infra/conf: failed to build routing configuration > infra/conf: invalid field rule > infra/conf: failed to parse domain rule: geosite:nope > infra/conf: failed to load geosite: NOPE"
	if got != want {
		t.Fatalf("xrayError:\n получили %q\n ожидали  %q", got, want)
	}
	if xrayError([]byte("что-то странное")) != "что-то странное" {
		t.Fatal("без строки Failed to start должна вернуться последняя строка")
	}
}

func TestCheckSNI(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	// TLS 1.3 + HTTP/2 — подходит
	h2 := httptest.NewUnstartedServer(handler)
	h2.EnableHTTP2 = true
	h2.StartTLS()
	defer h2.Close()
	if msg := checkSNI(h2.Listener.Addr().String(), "example.com"); msg != "" {
		t.Fatalf("сервер с HTTP/2 должен пройти проверку: %s", msg)
	}

	// Только HTTP/1.1 — Reality с таким SNI работает плохо
	h1 := httptest.NewUnstartedServer(handler)
	h1.StartTLS()
	defer h1.Close()
	if msg := checkSNI(h1.Listener.Addr().String(), "example.com"); !strings.Contains(msg, "HTTP/2") {
		t.Fatalf("ожидали жалобу на HTTP/2, получили %q", msg)
	}

	// Недоступный адрес
	if msg := checkSNI("127.0.0.1:1", "example.com"); !strings.Contains(msg, "недоступен") {
		t.Fatalf("ожидали «недоступен», получили %q", msg)
	}
}

func TestSNICheckerCaches(t *testing.T) {
	calls := 0
	check := func(addr, name string) string { calls++; return "" }
	var c sniChecker
	c.get("a.com", check)
	c.get("a.com", check)
	if calls != 1 {
		t.Fatalf("повторная проверка того же SNI должна браться из кэша, вызовов: %d", calls)
	}
	c.get("b.com", check)
	if calls != 2 {
		t.Fatalf("новый SNI должен проверяться сразу, вызовов: %d", calls)
	}
}

// Плохое правило маршрутизации не должно блокировать остальной конфиг.
// Нужен настоящий Xray (XRAY_BIN) с геоданными (XRAY_LOCATION_ASSET).
func TestSyncFallsBackWithoutBadRules(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("XRAY_BIN не задан")
	}
	// Поддельный systemctl, чтобы «перезапуск xray» ничего не делал
	fakeBin := t.TempDir()
	os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(protocol.SyncResponse{
			Role:    protocol.RoleBridge,
			Reality: protocol.Reality{SNI: "panel.example.com", ShortID: "abcd", Dest: "127.0.0.1:8443"},
			Clients: []protocol.Client{{ID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", Email: "u1"}},
			Direct:  []string{"geosite:no-such-category", "domain:example.com"},
		})
	}))
	defer master.Close()

	a := &agent{
		masterURL:  master.URL,
		token:      "t",
		privKey:    "2KZ7s3Gn8dq3ZNUB-Knd0t7ptZ7g8kpRDFTXlvCp1mc",
		configPath: filepath.Join(t.TempDir(), "config.json"),
		xrayBin:    bin,
		http:       master.Client(),
	}
	a.sync()

	cfg, err := os.ReadFile(a.configPath)
	if err != nil {
		t.Fatalf("конфиг не записан: %v (ошибка агента: %s)", err, a.lastErr)
	}
	if strings.Contains(string(cfg), "no-such-category") {
		t.Fatal("плохое правило попало в конфиг")
	}
	if !strings.Contains(string(cfg), "9b1deb4d") {
		t.Fatal("пользователь должен попасть в конфиг, несмотря на плохое правило")
	}
	if !strings.Contains(a.lastErr, "Правила маршрутизации не применены") || !strings.Contains(a.lastErr, "NO-SUCH-CATEGORY") {
		t.Fatalf("в панели должна быть понятная причина, а не %q", a.lastErr)
	}
}

func TestEnsureHy2Cert(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	certFile, keyFile, pem1, err := ensureHy2Cert(dir, "cv.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(keyFile); st.Mode().Perm() != 0o600 {
		t.Fatalf("ключ должен быть 0600, а не %v", st.Mode().Perm())
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatalf("сертификат и ключ не подходят друг другу: %v", err)
	}
	// Тот же SNI — тот же сертификат (иначе у клиентов менялся бы пин)
	if _, _, pem2, _ := ensureHy2Cert(dir, "cv.example.com", now); !bytes.Equal(pem1, pem2) {
		t.Fatal("сертификат перевыпущен без причины")
	}
	// Сменился SNI или сертификат скоро истекает — перевыпуск
	if _, _, pem3, _ := ensureHy2Cert(dir, "other.example.com", now); bytes.Equal(pem1, pem3) {
		t.Fatal("при смене SNI сертификат нужно перевыпустить")
	}
	if _, _, pem4, _ := ensureHy2Cert(dir, "other.example.com", now.AddDate(10, 0, 0)); len(pem4) == 0 {
		t.Fatal("истекающий сертификат должен перевыпускаться")
	}
	if _, _, _, err := ensureHy2Cert(dir, "", now); err == nil {
		t.Fatal("без имени сервера сертификат не выпускаем")
	}
}
