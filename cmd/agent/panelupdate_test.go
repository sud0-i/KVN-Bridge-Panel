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

	if _, err := updatePanel(); err == nil || !strings.Contains(err.Error(), "нет панели") {
		t.Fatalf("без docker-compose.yml: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), nil, 0o644)

	// Новая версия поднялась
	set("running", "sha256:old")
	set("registry", "sha256:new")
	set("http", "404")
	if res, err := updatePanel(); err != nil || res != "готово" {
		t.Fatalf("обновление: %q %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "running")); strings.TrimSpace(string(b)) != "sha256:new" {
		t.Fatal("работает не новая версия")
	}

	// Уже последняя
	if res, err := updatePanel(); err != nil || res != "уже последняя версия" {
		t.Fatalf("без изменений: %q %v", res, err)
	}

	// Новая не отвечает — откат на прежнюю (ожидание сокращаем: curl сразу «000»)
	set("registry", "sha256:broken")
	set("http", "000")
	short := strings.Replace(panelUpdateScript, "-lt 45", "-lt 2", 1)
	short = strings.Replace(short, "sleep 2", "sleep 0", 1)
	defer func(s string) { panelUpdateScriptRun = s }(panelUpdateScriptRun)
	panelUpdateScriptRun = short
	_, err := updatePanel()
	if err == nil || !strings.Contains(err.Error(), "вернул прежнюю") {
		t.Fatalf("откат: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "running")); strings.TrimSpace(string(b)) != "sha256:new" {
		t.Fatalf("после отката работает %s", b)
	}
	calls, _ := os.ReadFile(filepath.Join(state, "calls"))
	if !strings.Contains(string(calls), "tag sha256:new ghcr.io/x/panel:latest") {
		t.Fatalf("вызовы docker:\n%s", calls)
	}
}
