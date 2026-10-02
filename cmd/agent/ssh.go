package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// SSH на ноде: агент держит в authorized_keys ключ мастера (терминал и переустановка
// из панели) и личные ключи администратора, а по команде из панели выключает вход по
// паролю. Чужие строки в authorized_keys не трогает — правит только свой блок.

var (
	authorizedKeysPath = "/root/.ssh/authorized_keys"
	sshdDropIn         = "/etc/ssh/sshd_config.d/00-kvn.conf"
	sshdMain           = "/etc/ssh/sshd_config"
)

const (
	keysBegin = "# kvn-panel: ключи панели — правит агент, свои ключи пишите вне блока"
	keysEnd   = "# kvn-panel: конец"
	// 00- — раньше 50-cloud-init.conf: в sshd побеждает первое значение
	dropInBody = "# kvn-panel: вход только по ключу (включается и выключается в панели)\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin prohibit-password\n"
)

type sshState struct {
	lastState string
}

// syncSSH приводит authorized_keys и настройку входа к тому, что прислал мастер
func (a *agent) syncSSH(keys []string, keysOnly bool) {
	if a.ssh == nil {
		a.ssh = &sshState{}
	}
	if len(keys) == 0 {
		return // старый мастер — ничего не трогаем
	}
	if err := ensureAuthorizedKeys(keys); err != nil {
		a.ssh.fail("authorized_keys: " + err.Error())
		return
	}
	if !dropInMatches(keysOnly) {
		if err := setKeysOnly(keysOnly); err != nil {
			a.ssh.fail(err.Error())
			return
		}
		if keysOnly {
			log.Printf("🔐 SSH: вход по паролю выключен")
		} else {
			log.Printf("🔓 SSH: вход по паролю снова разрешён")
		}
	}
	a.ssh.lastState = "keys"
	if keysOnly {
		a.ssh.lastState = "keys-only"
	}
}

// fail запоминает ошибку для панели; в лог пишем только новую, а не каждую минуту
func (st *sshState) fail(msg string) {
	if st.lastState != "error: "+msg {
		log.Printf("⚠️ SSH: %s", msg)
	}
	st.lastState = "error: " + msg
}

func (a *agent) sshHeader() string {
	if a.ssh == nil {
		return ""
	}
	return a.ssh.lastState
}

// ensureAuthorizedKeys переписывает блок панели в authorized_keys, сохраняя остальное
func ensureAuthorizedKeys(keys []string) error {
	old, err := os.ReadFile(authorizedKeysPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var keep []string
	inBlock := false
	for _, l := range strings.Split(string(old), "\n") {
		switch {
		case l == keysBegin:
			inBlock = true
		case l == keysEnd:
			inBlock = false
		case !inBlock && strings.TrimSpace(l) != "":
			keep = append(keep, l)
		}
	}
	var b bytes.Buffer
	for _, l := range keep {
		b.WriteString(l + "\n")
	}
	b.WriteString(keysBegin + "\n")
	for _, k := range keys {
		if strings.ContainsAny(k, "\n\r") {
			continue
		}
		b.WriteString(k + "\n")
	}
	b.WriteString(keysEnd + "\n")
	if bytes.Equal(old, b.Bytes()) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(authorizedKeysPath), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(authorizedKeysPath), 0o700)
	return writeFileAtomic(authorizedKeysPath, b.Bytes(), 0o600)
}

func dropInMatches(keysOnly bool) bool {
	cur, err := os.ReadFile(sshdDropIn)
	if keysOnly {
		return err == nil && string(cur) == dropInBody
	}
	return os.IsNotExist(err)
}

// setKeysOnly включает или выключает вход по паролю. Новую настройку проверяет sshd -t;
// не прошла — откатываем, а не оставляем сервер с битым sshd.
func setKeysOnly(on bool) error {
	if on {
		if main, err := os.ReadFile(sshdMain); err == nil && !bytes.Contains(main, []byte("sshd_config.d")) {
			return fmt.Errorf("в %s нет Include sshd_config.d — выключите пароль вручную", sshdMain)
		}
		if err := os.MkdirAll(filepath.Dir(sshdDropIn), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(sshdDropIn, []byte(dropInBody), 0o644); err != nil {
			return err
		}
	} else if err := os.Remove(sshdDropIn); err != nil && !os.IsNotExist(err) {
		return err
	}
	if out, err := runCmd("sshd", "-t"); err != nil {
		os.Remove(sshdDropIn)
		return fmt.Errorf("sshd -t: %s", strings.TrimSpace(string(out)))
	}
	// Служба называется ssh (Debian/Ubuntu) или sshd (остальные)
	if _, err := runCmd("systemctl", "reload", "ssh"); err != nil {
		if out, err := runCmd("systemctl", "reload", "sshd"); err != nil {
			return fmt.Errorf("перезагрузка sshd: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}
