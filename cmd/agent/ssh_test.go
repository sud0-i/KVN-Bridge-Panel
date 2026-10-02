package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncSSH(t *testing.T) {
	dir := t.TempDir()
	defer func(a, d, m string) { authorizedKeysPath, sshdDropIn, sshdMain = a, d, m }(authorizedKeysPath, sshdDropIn, sshdMain)
	authorizedKeysPath = filepath.Join(dir, ".ssh", "authorized_keys")
	sshdDropIn = filepath.Join(dir, "sshd_config.d", "00-kvn.conf")
	sshdMain = filepath.Join(dir, "sshd_config")
	os.WriteFile(sshdMain, []byte("Include /etc/ssh/sshd_config.d/*.conf\n"), 0o644)

	var calls []string
	sshdBroken := false
	defer func(r func(string, ...string) ([]byte, error)) { runCmd = r }(runCmd)
	runCmd = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "sshd" && sshdBroken {
			return []byte("bad option"), errors.New("exit 255")
		}
		if name == "systemctl" && args[1] == "ssh" {
			return []byte("Unit ssh.service not found"), errors.New("exit 5")
		}
		return nil, nil
	}

	// Свои ключи администратора вне блока не трогаем
	os.MkdirAll(filepath.Dir(authorizedKeysPath), 0o755)
	os.WriteFile(authorizedKeysPath, []byte("ssh-ed25519 AAAAmine me@pc\n"), 0o644)
	a := &agent{}
	a.syncSSH([]string{"ssh-ed25519 AAAAmaster kvn-master"}, false)
	got, _ := os.ReadFile(authorizedKeysPath)
	want := "ssh-ed25519 AAAAmine me@pc\n" + keysBegin + "\nssh-ed25519 AAAAmaster kvn-master\n" + keysEnd + "\n"
	if string(got) != want || a.sshHeader() != "keys" || len(calls) != 0 {
		t.Fatalf("authorized_keys:\n%s\nсостояние %q, команды %v", got, a.sshHeader(), calls)
	}
	if st, _ := os.Stat(authorizedKeysPath); st.Mode().Perm() != 0o600 {
		t.Fatalf("права %v", st.Mode())
	}
	if st, _ := os.Stat(filepath.Dir(authorizedKeysPath)); st.Mode().Perm() != 0o700 {
		t.Fatalf("права .ssh %v", st.Mode())
	}

	// Ключи поменялись — блок переписан, чужое на месте
	a.syncSSH([]string{"ssh-ed25519 AAAAmaster kvn-master", "ssh-ed25519 AAAAlaptop admin"}, false)
	got, _ = os.ReadFile(authorizedKeysPath)
	if !strings.HasPrefix(string(got), "ssh-ed25519 AAAAmine me@pc\n") || strings.Count(string(got), keysBegin) != 1 || !strings.Contains(string(got), "AAAAlaptop") {
		t.Fatalf("authorized_keys:\n%s", got)
	}

	// Только по ключу: файл настройки + проверка + перезагрузка sshd (служба sshd, не ssh)
	a.syncSSH([]string{"ssh-ed25519 AAAAmaster kvn-master"}, true)
	if body, err := os.ReadFile(sshdDropIn); err != nil || !strings.Contains(string(body), "PasswordAuthentication no") || a.sshHeader() != "keys-only" {
		t.Fatalf("drop-in: %v %q", err, a.sshHeader())
	}
	if strings.Join(calls, "; ") != "sshd -t; systemctl reload ssh; systemctl reload sshd" {
		t.Fatalf("команды: %v", calls)
	}
	// Повтор — ничего не перезагружаем
	calls = nil
	a.syncSSH([]string{"ssh-ed25519 AAAAmaster kvn-master"}, true)
	if len(calls) != 0 {
		t.Fatalf("лишние команды: %v", calls)
	}
	// Выключили — файл убран
	a.syncSSH([]string{"ssh-ed25519 AAAAmaster kvn-master"}, false)
	if _, err := os.Stat(sshdDropIn); !os.IsNotExist(err) || a.sshHeader() != "keys" {
		t.Fatal("drop-in должен исчезнуть")
	}

	// sshd -t не прошёл — откат и ошибка в панель
	sshdBroken = true
	a.syncSSH([]string{"ssh-ed25519 AAAAmaster kvn-master"}, true)
	if _, err := os.Stat(sshdDropIn); !os.IsNotExist(err) || !strings.Contains(a.sshHeader(), "bad option") {
		t.Fatalf("откат: %q", a.sshHeader())
	}
	// Нет Include — не включаем (иначе настройка молча не подействует)
	sshdBroken = false
	os.WriteFile(sshdMain, []byte("PermitRootLogin yes\n"), 0o644)
	a.syncSSH([]string{"ssh-ed25519 AAAAmaster kvn-master"}, true)
	if !strings.Contains(a.sshHeader(), "Include") {
		t.Fatalf("без Include: %q", a.sshHeader())
	}
	// Старый мастер без ключей — ничего не трогаем
	b := &agent{}
	b.syncSSH(nil, true)
	if b.sshHeader() != "" {
		t.Fatal("без ключей — без состояния")
	}
}
