package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/db"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
)

// DeployParams — всё, что нужно Ansible, чтобы поднять ноду
type DeployParams struct {
	IP           string
	Role         string
	RootPassword string
	// KeyFile — SSH-ключ мастера; вместо пароля, если RootPassword пуст
	KeyFile    string
	PrivateKey string
	NodeToken  string
	MasterURL  string
}

// Пути можно переопределить переменными окружения (удобно для локального запуска)
func playbookPath() string { return envOr("ANSIBLE_PLAYBOOK", "ansible/deploy_node.yml") }
func agentBinary() string  { return envOr("AGENT_BINARY", "build/agent_linux_amd64") }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// DeployNode запускает Ansible плейбук для настройки сервера
func DeployNode(p DeployParams) {
	log.Printf("🚀 [ANSIBLE] Начинаем деплой ноды %s...", p.IP)

	if err := run(p); err != nil {
		log.Printf("❌ [ANSIBLE] Ошибка деплоя %s: %v", p.IP, err)
		// Причину показываем в панели, чтобы не лезть в логи Мастера
		db.DB.Model(&models.Node{}).Where("ip = ?", p.IP).Updates(map[string]any{
			"is_online":    false,
			"config_error": "Деплой не удался: " + err.Error(),
		})
		return
	}

	log.Printf("✅ [ANSIBLE] Нода %s успешно настроена!", p.IP)
	// Окончательно «онлайн» нода станет, когда её агент впервые сходит на /api/sync
	db.DB.Model(&models.Node{}).Where("ip = ?", p.IP).Update("is_online", true)
}

func run(p DeployParams) error {
	agentSrc, err := absPath(agentBinary())
	if err != nil {
		return err
	}

	// Секреты передаём файлом с правами 0600, а не аргументами командной строки:
	// аргументы видны любому процессу через ps / /proc
	v := map[string]string{
		"ansible_user": "root",
		"master_url":   p.MasterURL,
		"priv_key":     p.PrivateKey,
		"node_token":   p.NodeToken,
		"node_role":    p.Role,
		"agent_src":    agentSrc,
	}
	if p.RootPassword != "" {
		v["ansible_password"] = p.RootPassword
	} else {
		v["ansible_ssh_private_key_file"] = p.KeyFile
	}
	vars, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "kvn-deploy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(vars); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// Запятая после IP нужна Ansible для работы без inventory-файла
	cmd := exec.CommandContext(ctx, "ansible-playbook", "-i", p.IP+",", playbookPath(), "-e", "@"+f.Name())
	cmd.Env = append(os.Environ(), ansibleEnv()...)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		log.Printf("[ANSIBLE] Вывод %s:\n%s", p.IP, out.String())
		if reason := ansibleFailure(out.String()); reason != "" {
			return fmt.Errorf("%s", reason)
		}
		return err
	}
	return nil
}

// ansibleEnv — настройки SSH для деплоя. Ключ хоста новой машины заранее неизвестен,
// поэтому его не проверяем — и не запоминаем: иначе после переустановки ОС на сервере
// ключ меняется, OpenSSH видит несовпадение с known_hosts и отключает вход по паролю
// («Password authentication is disabled to avoid man-in-the-middle attacks»).
func ansibleEnv() []string {
	return []string{
		"ANSIBLE_HOST_KEY_CHECKING=False",
		"ANSIBLE_SSH_COMMON_ARGS=-o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no",
	}
}

// ansibleFailure достаёт из вывода Ansible строки с причиной провала
// (fatal:/UNREACHABLE!) — их и покажем в панели
func ansibleFailure(out string) string {
	var reasons []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "fatal:") || strings.Contains(line, "UNREACHABLE!") {
			reasons = append(reasons, line)
		}
	}
	msg := strings.Join(reasons, " | ")
	if r := []rune(msg); len(r) > 900 {
		msg = string(r[:900])
	}
	return msg
}

func absPath(p string) (string, error) {
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return filepath.Abs(p)
}
