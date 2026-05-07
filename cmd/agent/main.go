package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Структуры, которые мы ожидаем получить от Мастера
type User struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	IPLimit int    `json:"IPLimit"`
}

type Node struct {
	IP     string `json:"IP"`
	PubKey string `json:"PubKey"`
}

type SyncResponse struct {
	SNI         string `json:"sni"`
	WarpDomains string `json:"warp_domains"`
	Users       []User `json:"users"`
	Exits       []Node `json:"exits"`
}

func main() {
	// 1. Загружаем переменные из .env файла
	// (Ansible создаст этот файл при деплое сервера)
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ Файл .env не найден, используем системные переменные")
	}

	masterURL := os.Getenv("MASTER_URL") // Например: http://192.168.1.100:8080
	privKey := os.Getenv("PRIVATE_KEY")
	role := os.Getenv("NODE_ROLE") // ru_bridge или eu_exit

	if masterURL == "" || privKey == "" {
		log.Fatal("❌ Ошибка: MASTER_URL или PRIVATE_KEY не заданы!")
	}

	log.Printf("🤖 Агент запущен. Роль: %s. Мастер: %s", role, masterURL)

	// 2. Делаем первую синхронизацию при старте
	syncWithMaster(masterURL, privKey, role)

	// 3. Запускаем фоновые задачи (Горутины)

	// Обновление юзеров каждые 3 минуты
	go func() {
		for {
			time.Sleep(3 * time.Minute)
			syncWithMaster(masterURL, privKey, role)
		}
	}()

	// Отправка статистики каждую минуту
	go func() {
		for {
			time.Sleep(1 * time.Minute)
			sendStatsToMaster(masterURL)
		}
	}()

	// Блокируем main, чтобы программа не завершилась
	select {}
}

// --- ФУНКЦИЯ СИНХРОНИЗАЦИИ И ОБНОВЛЕНИЯ КОНФИГА ---
func syncWithMaster(masterURL, privKey, role string) {
	resp, err := http.Get(fmt.Sprintf("%s/api/sync", masterURL))
	if err != nil {
		log.Printf("❌ Ошибка связи с мастером: %v", err)
		return
	}
	defer resp.Body.Close()

	var data SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("❌ Ошибка парсинга JSON от мастера: %v", err)
		return
	}

	log.Printf("✅ Получено %d юзеров и %d экзитов. Роль: %s. Генерируем конфиг...",
		len(data.Users), len(data.Exits), role)

	// ВЫЗЫВАЕМ НАШ ГЕНЕРАТОР:
	updateXrayConfig(data, privKey, role)

	// ЗДЕСЬ БУДЕТ ТВОЙ КОД ГЕНЕРАЦИИ config.json
	// ...
	// Ты берешь data.Users, формируешь список клиентов для Xray
	// Вставляешь privKey в секцию inbounds (если это ru_bridge)
	// Сохраняешь файл в /usr/local/etc/xray/config.json

	// И перезапускаешь Xray
	// exec.Command("systemctl", "restart", "xray").Run()
}

// --- ФУНКЦИЯ СБОРА И ОТПРАВКИ СТАТИСТИКИ ---
// sendStatsToMaster опрашивает локальный Xray и отправляет данные на Мастер
func sendStatsToMaster(masterURL string) {
	// 1. Вызываем CLI команду Xray
	// В Codespaces у нас нет установленного Xray, поэтому мы обрабатываем ошибку
	cmd := exec.Command("xray", "api", "statsquery", "-server=127.0.0.1:10085")
	out, err := cmd.Output()

	if err != nil {
		log.Printf("⚠️ Xray не найден или недоступен (локальный тест). Пропуск реального сбора.")
		// Для локального теста оставляем отправку фейковых данных (чтобы интерфейс работал)
		sendFakeStats(masterURL)
		return
	}

	// 2. Парсим ответ от Xray
	// Ответ выглядит так: {"stat": [{"name": "user>>>UUID>>>traffic>>>downlink", "value": "12345"}]}
	type XrayResponse struct {
		Stat []struct {
			Name  string `json:"name"`
			Value int64  `json:"value,string"` // Xray иногда отдает числа как строки, эта магия Go всё исправит
		} `json:"stat"`
	}

	var xrayData XrayResponse
	if err := json.Unmarshal(out, &xrayData); err != nil {
		log.Printf("❌ Ошибка парсинга статистики Xray: %v", err)
		return
	}

	// 3. Агрегируем трафик (т.к. up и down приходят отдельными строками)
	// Ключ - email (UUID), Значение - структура с up/down
	type UserStat struct {
		Email string `json:"email"`
		Up    int64  `json:"up"`
		Down  int64  `json:"down"`
	}
	statsMap := make(map[string]*UserStat)

	for _, s := range xrayData.Stat {
		parts := strings.Split(s.Name, ">>>")
		// Нас интересует только пользовательский трафик: user>>>[email]>>>traffic>>>[downlink/uplink]
		if len(parts) == 4 && parts[0] == "user" {
			email := parts[1]
			direction := parts[3]

			if _, exists := statsMap[email]; !exists {
				statsMap[email] = &UserStat{Email: email}
			}

			switch direction {
			case "downlink":
				statsMap[email].Down += s.Value
			case "uplink":
				statsMap[email].Up += s.Value
			}
		}
	}

	// 4. Преобразуем мапу в плоский массив для отправки на Мастер
	var finalStats []UserStat
	for _, stat := range statsMap {
		finalStats = append(finalStats, *stat)
	}

	// Если никто ничего не скачал, не дергаем Мастер
	if len(finalStats) == 0 {
		return
	}

	// 5. Отправляем на Мастер
	body, _ := json.Marshal(finalStats)
	resp, err := http.Post(fmt.Sprintf("%s/api/stats", masterURL), "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("❌ Ошибка отправки статистики: %v", err)
		return
	}
	defer resp.Body.Close()

	log.Printf("📊 Статистика успешно отправлена на Мастер (юзеров: %d)", len(finalStats))
}

// Заглушка для локального тестирования в Codespaces
func sendFakeStats(masterURL string) {
	// Берем фейковый UUID (замени на реальный из своей базы, если хочешь видеть анимацию в админке)
	stats := []map[string]interface{}{
		{
			"email": "СЮДА_ВСТАВИТЬ_UUID_ЮЗЕРА_ДЛЯ_ТЕСТА",
			"up":    1024 * 1024 * 5,
			"down":  1024 * 1024 * 50,
		},
	}
	body, _ := json.Marshal(stats)
	http.Post(fmt.Sprintf("%s/api/stats", masterURL), "application/json", bytes.NewBuffer(body))
}

// updateXrayConfig собирает правильный config.json для Xray
func updateXrayConfig(data SyncResponse, privKey, role string) {
	// 1. Собираем массив клиентов из наших юзеров
	var clients []map[string]interface{}
	for _, u := range data.Users {
		clients = append(clients, map[string]interface{}{
			"id":    u.ID,
			"flow":  "xtls-rprx-vision", // Включаем ускорение XTLS
			"email": u.ID,               // Xray будет считать трафик по этому полю
		})
	}

	// 2. Формируем "скелет" конфига
	var config map[string]interface{}

	if role == "ru_bridge" {
		config = map[string]interface{}{
			"log": map[string]interface{}{
				"loglevel": "warning",
				"access":   "/var/log/xray/access.log",
				"error":    "/var/log/xray/error.log",
			},
			// ВКЛЮЧАЕМ СТАТИСТИКУ И API
			"stats": map[string]interface{}{},
			"api": map[string]interface{}{
				"tag":      "api",
				"services": []string{"StatsService"},
			},
			"policy": map[string]interface{}{
				"levels": map[string]interface{}{
					"0": map[string]interface{}{
						"statsUserUplink":   true,
						"statsUserDownlink": true,
					},
				},
				"system": map[string]interface{}{
					"statsInboundUplink":   true,
					"statsInboundDownlink": true,
				},
			},
			"inbounds": []map[string]interface{}{
				{
					"port":     443,
					"protocol": "vless",
					"settings": map[string]interface{}{
						"clients":    clients,
						"decryption": "none",
					},
					"streamSettings": map[string]interface{}{
						"network":  "tcp",
						"security": "reality",
						"realitySettings": map[string]interface{}{
							"show":        false,
							"dest":        data.SNI + ":443",
							"xver":        0,
							"serverNames": []string{data.SNI},
							"privateKey":  privKey,
							"shortIds":    []string{""},
						},
					},
				},
				// ТЕХНИЧЕСКИЙ INBOUND ДЛЯ API (Только локалхост)
				{
					"listen":   "127.0.0.1",
					"port":     10085,
					"protocol": "dokodemo-door",
					"settings": map[string]interface{}{
						"address": "127.0.0.1",
					},
					"tag": "api",
				},
			},
			"outbounds": []map[string]interface{}{
				{
					"protocol": "freedom",
					"tag":      "direct",
				},
			},
			// ПРАВИЛО МАРШРУТИЗАЦИИ ДЛЯ API
			"routing": map[string]interface{}{
				"rules": []map[string]interface{}{
					{
						"inboundTag":  []string{"api"},
						"outboundTag": "api",
						"type":        "field",
					},
				},
			},
		}
	} else {
		// Конфиг для EU-экзита мы допишем позже
		log.Println("⚠️ Конфиг для eu_exit пока не реализован")
		return
	}

	// 3. Превращаем структуру Go обратно в JSON с красивыми отступами
	fileBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		log.Printf("❌ Ошибка сборки JSON: %v", err)
		return
	}

	// 4. Записываем файл (создаем папку tmp_xray для локального теста)
	os.MkdirAll("tmp_xray", 0755)
	err = os.WriteFile("tmp_xray/config.json", fileBytes, 0644)
	if err != nil {
		log.Printf("❌ Ошибка записи файла: %v", err)
		return
	}

	log.Println("💾 Конфиг Xray успешно сгенерирован и сохранен!")

	// В боевых условиях (на реальном сервере) здесь будет рестарт службы:
	// exec.Command("systemctl", "restart", "xray").Run()
}
