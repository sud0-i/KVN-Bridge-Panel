package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Настоящий скрипт обновления с поддельными docker и curl: состояние «контейнера» —
// в файлах, systemd-run заменён прямым запуском sh с теми же переменными
func TestUpdatePanel(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	state := filepath.Join(dir, "state")
	os.MkdirAll(state, 0o755)
	// docker: inspect отдаёт текущий образ, pull кладёт «новый» в registry, up -d ставит его, tag — откат
	os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/bin/sh
S=`+state+`
echo "$@" >> $S/calls
case "$1" in
inspect) case "$3" in *Config.Image*) echo ghcr.io/x/panel:latest;; *) cat $S/running;; esac;;
compose) case "$2" in pull) cp $S/registry $S/pulled;; up) cp $S/pulled $S/running 2>/dev/null || true;; esac;;
tag) echo "$2" > $S/pulled;;
esac
`), 0o755)
	os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\ncat "+state+"/http\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	defer func(d, l string) { panelDir, panelUpdateLog = d, l }(panelDir, panelUpdateLog)
	panelDir = dir
	panelUpdateLog = filepath.Join(dir, "update.log")
	defer func(r func(string, ...string) ([]byte, error)) { runCmd = r }(runCmd)
	runCmd = func(name string, args ...string) ([]byte, error) {
		if name != "systemd-run" {
			t.Fatalf("неожиданная команда %s", name)
		}
		cmd := exec.Command("/bin/sh", "-c", args[len(args)-1])
		cmd.Env = os.Environ()
		for _, a := range args {
			if strings.HasPrefix(a, "--setenv=") {
				cmd.Env = append(cmd.Env, strings.TrimPrefix(a, "--setenv="))
			}
		}
		return cmd.CombinedOutput()
	}
	set := func(name, v string) { os.WriteFile(filepath.Join(state, name), []byte(v+"\n"), 0o644) }

	if _, err := updatePanel(""); err == nil || !strings.Contains(err.Error(), "нет панели") {
		t.Fatalf("без docker-compose.yml: %v", err)
	}
	compose := filepath.Join(dir, "docker-compose.yml")
	os.WriteFile(compose, []byte("services:\n  master:\n    image: ghcr.io/sud0-i/kvn-bridge-panel:pr-2\n  caddy:\n    image: caddy:2-alpine\n"), 0o644)
	readCompose := func() string { b, _ := os.ReadFile(compose); return string(b) }

	// Новая версия поднялась
	set("running", "sha256:old")
	set("registry", "sha256:new")
	set("http", "404")
	if res, err := updatePanel(""); err != nil || res != "готово" {
		t.Fatalf("обновление: %q %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "running")); strings.TrimSpace(string(b)) != "sha256:new" {
		t.Fatal("работает не новая версия")
	}

	// Уже последняя
	if res, err := updatePanel(""); err != nil || res != "уже последняя версия" {
		t.Fatalf("без изменений: %q %v", res, err)
	}

	// Тестовый канал: тег в docker-compose.yml меняется на beta, чужие образы не трогаем
	set("registry", "sha256:beta1")
	if res, err := updatePanel("beta"); err != nil || res != "готово (тестовая версия)" {
		t.Fatalf("beta: %q %v", res, err)
	}
	if c := readCompose(); !strings.Contains(c, "image: ghcr.io/sud0-i/kvn-bridge-panel:beta\n") || !strings.Contains(c, "image: caddy:2-alpine") {
		t.Fatalf("compose после beta:\n%s", c)
	}
	// Обратно на стабильный
	set("registry", "sha256:new")
	if res, err := updatePanel("stable"); err != nil || res != "готово" || !strings.Contains(readCompose(), "kvn-bridge-panel:latest\n") {
		t.Fatalf("stable: %q %v\n%s", res, err, readCompose())
	}

	// Новая не отвечает — откат на прежнюю (ожидание сокращаем: curl сразу «000»)
	set("registry", "sha256:broken")
	set("http", "000")
	short := strings.Replace(panelUpdateScript, "-lt 45", "-lt 2", 1)
	short = strings.Replace(short, "sleep 2", "sleep 0", 1)
	defer func(s string) { panelUpdateScriptRun = s }(panelUpdateScriptRun)
	panelUpdateScriptRun = short
	_, err := updatePanel("beta")
	if err == nil || !strings.Contains(err.Error(), "вернул прежнюю") {
		t.Fatalf("откат: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "running")); strings.TrimSpace(string(b)) != "sha256:new" {
		t.Fatalf("после отката работает %s", b)
	}
	if !strings.Contains(readCompose(), "kvn-bridge-panel:latest\n") {
		t.Fatalf("после отката compose не восстановлен:\n%s", readCompose())
	}
	calls, _ := os.ReadFile(filepath.Join(state, "calls"))
	if !strings.Contains(string(calls), "tag sha256:new ghcr.io/x/panel:latest") {
		t.Fatalf("вызовы docker:\n%s", calls)
	}
}
