package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/xray"
)

const testPriv = "2KZ7s3Gn8dq3ZNUB-Knd0t7ptZ7g8kpRDFTXlvCp1mc"

func buildCfg(t *testing.T, mutate func(*protocol.SyncResponse), clients ...protocol.Client) []byte {
	t.Helper()
	s := protocol.SyncResponse{
		Role:      protocol.RoleBridge,
		Reality:   protocol.Reality{SNI: "www.example.com", ShortID: "abcd", Dest: "127.0.0.1:8443"},
		Clients:   clients,
		XHTTPPath: "/x",
		Hysteria:  &protocol.Hysteria{CertFile: "/c.crt", KeyFile: "/c.key"},
	}
	if mutate != nil {
		mutate(&s)
	}
	cfg, err := xray.Build(s, testPriv)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

var (
	alice = protocol.Client{ID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d", Email: "alice"}
	bob   = protocol.Client{ID: "1b9d6bcd-bbfd-4b2d-9b5d-ab8dfbbd4bed", Email: "bob"}
)

func TestHotAdds(t *testing.T) {
	old := buildCfg(t, nil, alice)

	adds, ok := hotAdds(old, buildCfg(t, nil, alice, bob))
	if !ok {
		t.Fatal("добавление пользователя — без перезапуска")
	}
	tags := map[string]bool{}
	for _, a := range adds {
		if a.Client["email"] != "bob" {
			t.Fatalf("добавлять только нового: %+v", a)
		}
		tags[a.Tag] = true
	}
	if !tags["vless-in"] || !tags["xhttp-in"] || !tags["hy2-in"] {
		t.Fatalf("новый пользователь нужен во всех входящих: %v", tags)
	}

	if _, ok := hotAdds(buildCfg(t, nil, alice, bob), old); ok {
		t.Fatal("удаление (блокировка) — только перезапуском")
	}
	rotated := alice
	rotated.ID = "5f0c6b2a-6f0e-4b0c-9a7e-2d3c4b5a6f70"
	if _, ok := hotAdds(old, buildCfg(t, nil, rotated)); ok {
		t.Fatal("смена ключа — только перезапуском")
	}
	if _, ok := hotAdds(old, buildCfg(t, func(s *protocol.SyncResponse) { s.XHTTPPath = "/y" }, alice, bob)); ok {
		t.Fatal("изменились не только пользователи — перезапуск")
	}
	if adds, ok := hotAdds(old, old); !ok || len(adds) != 0 {
		t.Fatal("без изменений добавлять нечего")
	}
	if _, ok := hotAdds([]byte("garbage"), old); ok {
		t.Fatal("старый конфиг не читается — перезапуск")
	}
}

func TestAddUserRequest(t *testing.T) {
	req, err := hotUser{Tag: "vless-in", Protocol: "vless", Client: map[string]any{"id": alice.ID, "email": "alice", "flow": xray.Flow}}.addUserRequest()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vless-in", "xray.app.proxyman.command.AddUserOperation", "xray.proxy.vless.Account", alice.ID, xray.Flow, "alice"} {
		if !strings.Contains(string(req), want) {
			t.Fatalf("в запросе нет %q", want)
		}
	}
	if _, err := (hotUser{Protocol: "shadowsocks"}).addUserRequest(); err == nil {
		t.Fatal("неизвестный протокол — ошибка, а значит перезапуск")
	}
}

// Применение конфига: новые пользователи — без перезапуска, остальное — перезапуском
func TestApplyConfigHot(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("XRAY_BIN не задан")
	}
	calls := fakeSystemctl(t)
	restarts := func() int { b, _ := os.ReadFile(calls); return strings.Count(string(b), "restart xray") }
	dir := t.TempDir()
	cert, key, _, err := ensureHy2Cert(dir, "www.example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	withCert := func(s *protocol.SyncResponse) { s.Hysteria.CertFile, s.Hysteria.KeyFile = cert, key }
	var added []hotUser
	a := &agent{xrayBin: bin, configPath: filepath.Join(dir, "config.json"), pending: map[string]*protocol.UserTraffic{},
		hotAdd: func(u []hotUser) error { added = u; return nil }}

	if _, err := a.applyConfig(buildCfg(t, withCert, alice)); err != nil {
		t.Fatal(err)
	}
	if restarts() != 1 {
		t.Fatal("первый конфиг — перезапуск")
	}
	if _, err := a.applyConfig(buildCfg(t, withCert, alice, bob)); err != nil {
		t.Fatal(err)
	}
	if restarts() != 1 || len(added) != 3 {
		t.Fatalf("новый пользователь — без перезапуска: перезапусков %d, добавлено %d", restarts(), len(added))
	}
	if b, _ := os.ReadFile(a.configPath); !strings.Contains(string(b), bob.ID) {
		t.Fatal("конфиг на диске должен содержать нового пользователя")
	}
	if _, err := a.applyConfig(buildCfg(t, withCert, bob)); err != nil {
		t.Fatal(err)
	}
	if restarts() != 2 {
		t.Fatal("удаление — перезапуск")
	}
	// API недоступен — всё равно применяем, перезапуском
	a.hotAdd = func([]hotUser) error { return os.ErrDeadlineExceeded }
	if _, err := a.applyConfig(buildCfg(t, withCert, bob, alice)); err != nil {
		t.Fatal(err)
	}
	if restarts() != 3 {
		t.Fatal("сбой API — перезапуск")
	}
}

// Настоящий Xray: пользователь, добавленный через API, появляется во всех входящих
func TestAddUsersLive(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("XRAY_BIN не задан")
	}
	if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", xray.APIPort)); err == nil {
		c.Close()
		t.Skip("порт API Xray занят")
	}
	dir := t.TempDir()
	cert, key, _, err := ensureHy2Cert(dir, "www.example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	withCert := func(s *protocol.SyncResponse) { s.Hysteria.CertFile, s.Hysteria.KeyFile = cert, key }
	cfgOld := buildCfg(t, withCert, alice)
	// Порт 443 может быть занят или требовать root — переносим входящие на свободный
	cfgOld = []byte(strings.ReplaceAll(string(cfgOld), `"port": 443`, `"port": 24443`))
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, cfgOld, 0o644)
	cmd := exec.Command(bin, "run", "-c", path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(time.Second)

	adds, ok := hotAdds(cfgOld, []byte(strings.ReplaceAll(string(buildCfg(t, withCert, alice, bob)), `"port": 443`, `"port": 24443`)))
	if !ok || len(adds) != 3 {
		t.Fatalf("ожидали 3 добавления, %v %d", ok, len(adds))
	}
	a := &agent{}
	if err := a.addUsers(adds); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"vless-in", "xhttp-in", "hy2-in"} {
		out, err := exec.Command(bin, "api", "inbounduser", fmt.Sprintf("--server=127.0.0.1:%d", xray.APIPort), "-tag="+tag, "-email=bob").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "bob") {
			t.Fatalf("%s: bob не добавлен: %v %s", tag, err, out)
		}
		if tag == "vless-in" && !strings.Contains(string(out), bob.ID) {
			t.Fatalf("неверная учётная запись: %s", out)
		}
	}
	// Повторное добавление того же пользователя — ошибка API, агент уйдёт в перезапуск
	if err := a.addUsers(adds[:1]); err == nil {
		t.Fatal("Xray должен отказать в повторном добавлении")
	}
}
