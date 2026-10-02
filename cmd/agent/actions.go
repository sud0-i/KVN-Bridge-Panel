package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Действия из панели. Мастер выдаёт действие один раз, агент выполняет его после
// синхронизации и сообщает итог в следующей. Перед перезапуском или перезагрузкой
// отправляет накопленный трафик, чтобы статистика не потерялась.

var (
	runCmd  = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).CombinedOutput() }
	exitNow = func() { os.Exit(0) } // systemd (Restart=always) поднимет агент заново
)

func (a *agent) runAction(action string) {
	log.Printf("🛠 Действие из панели: %s", action)
	result := "ok"
	var err error
	switch action {
	case protocol.ActionRestartXray:
		_ = a.collectStats()
		_, err = runCmd("systemctl", "restart", "xray")
	case protocol.ActionRestartMieru:
		a.collectMieru()
		_, err = runCmd("systemctl", "restart", "mita")
		if a.mieru != nil {
			a.mieru.applied = "" // конфиг и запуск — при следующей синхронизации
		}
	case protocol.ActionRestartAgent:
		a.collectAndSendStats()
		exitNow()
		return
	case protocol.ActionUpdatePanel:
		var res string
		if res, err = updatePanel(); err == nil {
			result = res
		}
	case protocol.ActionScanSNI:
		err = a.sniScan.scan(a.nodeIP)
	case protocol.ActionReboot:
		a.collectAndSendStats()
		var out []byte
		if out, err = runCmd("systemctl", "reboot"); err != nil {
			err = fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
		}
	default:
		err = fmt.Errorf("неизвестное действие")
	}
	if err != nil {
		result = "ошибка: " + err.Error()
		log.Printf("⚠️ %s: %v", action, err)
	}
	a.actionResult = action + ": " + result
}

func (a *agent) actionHeader() string { return url.QueryEscape(a.actionResult) }

// scheduleAction — выполнить после ответа мастеру (не держим синхронизацию)
func (a *agent) scheduleAction(action string) {
	if action == "" {
		return
	}
	go func() {
		time.Sleep(time.Second)
		a.runAction(action)
	}()
}
