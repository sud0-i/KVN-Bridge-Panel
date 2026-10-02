package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"google.golang.org/protobuf/encoding/protowire"
)

// fakeMita — скрипт вместо mita: пишет вызовы в файл, версия и статус задаются файлами
func fakeMita(t *testing.T, version string) (dir string, calls func() string) {
	dir = t.TempDir()
	log := filepath.Join(dir, "calls")
	os.WriteFile(filepath.Join(dir, "version"), []byte(version), 0o644)
	os.WriteFile(filepath.Join(dir, "status"), []byte("IDLE"), 0o644)
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]s/calls
case "$1" in
  version) cat %[1]s/version ;;
  status) echo "mita server status is \"$(cat %[1]s/status)\"" ;;
  start) echo RUNNING > %[1]s/status ;;
  stop) echo IDLE > %[1]s/status ;;
  replace) cp "$3" %[1]s/applied.json ;;
esac
exit 0
`, dir)
	os.WriteFile(filepath.Join(dir, "mita"), []byte(script), 0o755)
	t.Cleanup(func(b string) func() { return func() { mitaBin = b } }(mitaBin))
	mitaBin = filepath.Join(dir, "mita")
	return dir, func() string { b, _ := os.ReadFile(log); return string(b) }
}

func mieruAgent(t *testing.T) *agent {
	return &agent{configPath: filepath.Join(t.TempDir(), "config.json"), privKey: "priv", maint: testMaintainer(t),
		mieru: &mieruState{}, pending: map[string]*protocol.UserTraffic{}}
}

func TestMitaConfig(t *testing.T) {
	m := &protocol.Mieru{Ports: "40100-40109", Users: []protocol.MieruUser{{Name: "u1", Password: "p1"}}}
	raw, err := mitaConfig(m, "priv")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	b := cfg["portBindings"].([]any)[0].(map[string]any)
	if b["portRange"] != "40100-40109" || b["protocol"] != "TCP" {
		t.Fatalf("порты: %v", b)
	}
	proxy := cfg["egress"].(map[string]any)["proxies"].([]any)[0].(map[string]any)
	if proxy["port"] != float64(protocol.MieruSocksPort) || proxy["socks5Authentication"].(map[string]any)["password"] != mieruSocksPass("priv") {
		t.Fatalf("выход — в SOCKS Xray с паролем: %v", proxy)
	}
	single, _ := mitaConfig(&protocol.Mieru{Ports: "40100"}, "priv")
	if !strings.Contains(string(single), `"port": 40100`) {
		t.Fatalf("один порт: %s", single)
	}
	if mieruSocksPass("a") == mieruSocksPass("b") || len(mieruSocksPass("a")) != 24 {
		t.Fatal("пароль SOCKS — свой у каждой ноды")
	}
	other, _ := mitaConfig(&protocol.Mieru{Ports: "40100-40109", Users: []protocol.MieruUser{{Name: "u2", Password: "p2"}}}, "priv")
	if !sameExceptUsers(string(raw), string(other)) || sameExceptUsers(string(raw), string(single)) {
		t.Fatal("сравнение без пользователей")
	}
}

func TestSyncMieru(t *testing.T) {
	dir, calls := fakeMita(t, "3.38.0")
	a := mieruAgent(t)
	m := &protocol.Mieru{Version: "3.38.0", Ports: "40100-40109", Users: []protocol.MieruUser{{Name: "u1", Password: "p1"}}}

	a.syncMieru(m)
	if a.mieru.running != "3.38.0" || a.mieru.err != "" || !strings.Contains(calls(), "replace config") || !strings.Contains(calls(), "start") {
		t.Fatalf("первый запуск: %+v, вызовы %q", a.mieru, calls())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "applied.json")); !strings.Contains(string(b), `"u1"`) {
		t.Fatal("mita получил пользователей")
	}

	// Без изменений — mita не трогаем
	before := calls()
	a.syncMieru(m)
	if strings.TrimPrefix(calls(), before) != "version\nstatus\n" {
		t.Fatalf("лишние вызовы: %q", strings.TrimPrefix(calls(), before))
	}

	// Новый пользователь — reload, без перезапуска
	before = calls()
	m.Users = append(m.Users, protocol.MieruUser{Name: "u2", Password: "p2"})
	a.syncMieru(m)
	if d := strings.TrimPrefix(calls(), before); !strings.Contains(d, "reload") || strings.Contains(d, "stop") {
		t.Fatalf("пользователи — через reload: %q", d)
	}

	// Пользователя заблокировали — перезапуск, иначе его соединения останутся
	before = calls()
	m.Users = m.Users[:1]
	a.syncMieru(m)
	if d := strings.TrimPrefix(calls(), before); !strings.Contains(d, "stop") || !strings.Contains(d, "start") {
		t.Fatalf("удаление — перезапуск: %q", d)
	}

	// Порты поменялись — перезапуск
	before = calls()
	m.Ports = "40200-40209"
	a.syncMieru(m)
	if d := strings.TrimPrefix(calls(), before); !strings.Contains(d, "stop") || !strings.Contains(d, "start") {
		t.Fatalf("порты — перезапуск: %q", d)
	}

	// mita упал — поднимаем
	os.WriteFile(filepath.Join(dir, "status"), []byte("IDLE"), 0o644)
	a.syncMieru(m)
	if a.mieru.running == "" || !strings.HasSuffix(calls(), "start\nstatus\n") {
		t.Fatalf("упавший mita — запуск: %q", calls())
	}

	// Выключили — останавливаем
	a.syncMieru(nil)
	if a.mieru.running != "" || !strings.HasSuffix(calls(), "stop\n") {
		t.Fatal("выключение")
	}
}

func TestEnsureMitaInstall(t *testing.T) {
	dir, _ := fakeMita(t, "3.37.0")
	deb := []byte("fake deb")
	debSum := sha(deb) // сумма опубликована для настоящего пакета
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v3.38.0/mita_3.38.0_amd64.deb"):
			w.Write(deb)
		case strings.HasSuffix(r.URL.Path, "/v3.38.0/mita_3.38.0_amd64.deb.sha256.txt"):
			fmt.Fprintf(w, "%s  mita_3.38.0_amd64.deb\n", debSum)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer func(u string) { mieruReleaseURL = u }(mieruReleaseURL)
	mieruReleaseURL = srv.URL
	// Поддельный dpkg «ставит» новую версию
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "dpkg"), []byte(fmt.Sprintf("#!/bin/sh\necho 3.38.0 > %s/version\n", dir)), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	a := mieruAgent(t)
	if err := a.ensureMita("3.38.0"); err != nil {
		t.Fatal(err)
	}
	if mitaVersion() != "3.38.0" {
		t.Fatal("должна стоять нужная версия")
	}
	// Неверная сумма — не ставим
	os.WriteFile(filepath.Join(dir, "version"), []byte("3.37.0"), 0o644)
	deb = []byte("tampered")
	a.mieru.err = ""
	if err := a.ensureMita("3.38.0"); err == nil || !strings.Contains(err.Error(), "контрольная сумма") {
		t.Fatalf("подмена пакета: %v", err)
	}
}

func mitaUsersPB(users map[string][2]int64) []byte {
	var out []byte
	for name, v := range users {
		user := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), name)
		var item []byte
		item = protowire.AppendBytes(protowire.AppendTag(item, 1, protowire.BytesType), user)
		for i, mname := range []string{"DownloadBytes", "UploadBytes"} {
			var metric []byte
			metric = protowire.AppendString(protowire.AppendTag(metric, 1, protowire.BytesType), mname)
			metric = protowire.AppendVarint(protowire.AppendTag(metric, 2, protowire.VarintType), 2)
			metric = protowire.AppendVarint(protowire.AppendTag(metric, 3, protowire.VarintType), uint64(v[i]))
			item = protowire.AppendBytes(protowire.AppendTag(item, 2, protowire.BytesType), metric)
		}
		out = protowire.AppendBytes(protowire.AppendTag(out, 1, protowire.BytesType), item)
	}
	return out
}

func TestParseMitaUsers(t *testing.T) {
	got, err := parseMitaUsers(mitaUsersPB(map[string][2]int64{"u1": {1000, 20}, "u2": {0, 0}}))
	if err != nil || got["u1"] != [2]int64{1000, 20} || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestCollectMieru(t *testing.T) {
	a := mieruAgent(t)
	a.mieru.running = "3.38.0"
	totals := map[string][2]int64{"u1": {1000, 10}}
	a.mitaTraffic = func(string) (map[string][2]int64, error) { return totals, nil }

	a.collectMieru() // точка отсчёта — накопленное до агента не считаем
	if len(a.pending) != 0 {
		t.Fatal("первое чтение — только точка отсчёта")
	}
	totals = map[string][2]int64{"u1": {1500, 30}}
	a.collectMieru()
	if p := a.pending["u1"]; p == nil || p.Down != 500 || p.Up != 20 {
		t.Fatalf("прирост: %+v", p)
	}
	// Перезапуск агента: точка отсчёта с диска — ни потерь, ни удвоения
	b := mieruAgent(t)
	b.configPath, b.mieru.running = a.configPath, "3.38.0"
	totals = map[string][2]int64{"u1": {1700, 30}}
	b.mitaTraffic = a.mitaTraffic
	b.collectMieru()
	if p := b.pending["u1"]; p == nil || p.Down != 200 {
		t.Fatalf("после перезапуска агента: %+v", p)
	}
	// mita перезапустился — счётчик с нуля
	totals = map[string][2]int64{"u1": {50, 5}}
	b.pending = map[string]*protocol.UserTraffic{}
	b.collectMieru()
	if p := b.pending["u1"]; p == nil || p.Down != 50 || p.Up != 5 {
		t.Fatalf("сброс счётчика mita: %+v", p)
	}
}
