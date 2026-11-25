package runner

import (
	"bytes"
	"context"
	"encoding/base64"
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
	KeyFile         string
	PrivateKey      string
	NodeToken       string
	MasterURL       string
	SelfStealDomain string
	SelfStealCDN    string
	SelfStealHTML   string
	WarpTransit     bool
	SSH             SSHRoute
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
	// Онлайн отмечает только агент при первой синхронизации.
}

func run(p DeployParams) error {
	agentSrc, err := absPath(agentBinary())
	if err != nil {
		return err
	}

	// Секреты передаём файлом с правами 0600, а не аргументами командной строки:
	// аргументы видны любому процессу через ps / /proc
	v := map[string]string{
		"ansible_user":          "root",
		"master_url":            p.MasterURL,
		"priv_key":              p.PrivateKey,
		"node_token":            p.NodeToken,
		"node_role":             p.Role,
		"agent_src":             agentSrc,
		"self_steal_domain":     p.SelfStealDomain,
		"self_steal_cdn_domain": p.SelfStealCDN,
		"self_steal_site_b64":   base64.StdEncoding.EncodeToString([]byte(p.SelfStealHTML)),
		"node_ip":               p.IP,
		"node_ssh_port":         p.SSH.PortString(),
		"warp_transit":          fmt.Sprint(p.WarpTransit),
	}
	if p.RootPassword != "" {
		v["ansible_password"] = p.RootPassword
	} else {
		v["ansible_ssh_private_key_file"] = p.KeyFile
	}
	return runPlaybook(context.Background(), p.IP, p.SSH, v)
}

// ProvisionSelfSteal runs only the web site tasks, without restarting Xray or the agent.
func ProvisionSelfSteal(ctx context.Context, ip, domain, cdn, siteHTML, keyFile string, route SSHRoute) error {
	return runPlaybook(ctx, ip, route, map[string]string{
		"ansible_user":                 "root",
		"ansible_ssh_private_key_file": keyFile,
		"self_steal_domain":            domain,
		"self_steal_cdn_domain":        cdn,
		"self_steal_site_b64":          base64.StdEncoding.EncodeToString([]byte(siteHTML)),
		"node_ip":                      ip,
	}, "--tags", "self-steal")
}

// ProvisionWARP installs the proxy only. The existing node services keep running.
func ProvisionWARP(ctx context.Context, ip, keyFile string, route SSHRoute) error {
	return runPlaybook(ctx, ip, route, map[string]string{
		"ansible_user":                 "root",
		"ansible_ssh_private_key_file": keyFile,
	}, "--tags", "install-warp")
}

func ProvisionAWG(ctx context.Context, ip, keyFile string, route SSHRoute) error {
	dir, err := absPath(envOr("AWG_BINARIES", "build/awg_linux_amd64"))
	if err != nil {
		return err
	}
	for _, name := range []string{"awg", "amneziawg-go"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("не найден бинарник %s для AWG в образе панели", name)
		}
	}
	return runPlaybook(ctx, ip, route, map[string]string{
		"ansible_user":                 "root",
		"ansible_ssh_private_key_file": keyFile,
		"awg_binaries":                 dir,
	}, "--tags", "install-awg")
}

func runPlaybook(ctx context.Context, ip string, route SSHRoute, v map[string]string, options ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	host, port, closeRelay, err := route.ansibleTarget(ctx, ip)
	if err != nil {
		return err
	}
	defer closeRelay()
	v["ansible_host"], v["ansible_port"] = host, port
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

	// Запятая после IP нужна Ansible для работы без inventory-файла
	args := []string{"-i", ip + ",", playbookPath(), "-e", "@" + f.Name()}
	cmd := exec.CommandContext(ctx, "ansible-playbook", append(args, options...)...)
	cmd.Env = append(os.Environ(), ansibleEnv()...)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		log.Printf("[ANSIBLE] Вывод %s:\n%s", ip, out.String())
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
