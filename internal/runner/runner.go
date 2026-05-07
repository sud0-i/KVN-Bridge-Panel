package runner

import (
	"bytes"
	"log"
	"os/exec"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/db"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
)

// DeployNode запускает Ansible плейбук для настройки сервера
func DeployNode(nodeIP, nodeType, rootPassword string, privKey string) {
	log.Printf("🚀 [ANSIBLE] Начинаем деплой ноды %s (Тип: %s)...", nodeIP, nodeType)

	// Формируем команду запуска. Обрати внимание на запятую после IP — это нужно Ansible для работы без inventory-файла
	cmd := exec.Command("ansible-playbook", "-i", nodeIP+",", "ansible/deploy_node.yml",
		"-e", "ansible_user=root",
		"-e", "ansible_password="+rootPassword)

	// Перехватываем логи (чтобы видеть их в консоли Мастера)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	// Запускаем процесс и ждем его завершения
	err := cmd.Run()
	if err != nil {
		log.Printf("❌ [ANSIBLE] Ошибка деплоя %s:\n%s", nodeIP, out.String())
		// В случае ошибки помечаем ноду в БД как офлайн
		db.DB.Model(&models.Node{}).Where("ip = ?", nodeIP).Update("is_online", false)
		return
	}

	log.Printf("✅ [ANSIBLE] Нода %s успешно настроена!\nЛоги:\n%s", nodeIP, out.String())
	
	// Если всё прошло супер, включаем ноду в базе!
	db.DB.Model(&models.Node{}).Where("ip = ?", nodeIP).Update("is_online", true)
}