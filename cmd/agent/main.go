package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	statscmd "github.com/xtls/xray-core/app/stats/command"
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
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ Файл .env не найден, используем системные переменные")
	}

	// Читаем переменные ТОЛЬКО ОДИН РАЗ:
	masterURL := os.Getenv("MASTER_URL")
	privKey := os.Getenv("PRIVATE_KEY")
	role := os.Getenv("NODE_ROLE")
	apiKey := os.Getenv("CLUSTER_API_KEY")

	// Проверяем, что всё на месте
	if masterURL == "" || privKey == "" || apiKey == "" {
		log.Fatal("❌ Ошибка: MASTER_URL, PRIVATE_KEY или CLUSTER_API_KEY не заданы!")
	}

	log.Printf("🤖 Агент запущен. Роль: %s. Мастер: %s", role, masterURL)

	// 2. Делаем первую синхронизацию при старте
	syncWithMaster(masterURL, privKey, role, apiKey)

	// 3. Запускаем фоновые задачи (Горутины)

	// Обновление юзеров каждые 3 минуты
	go func() {
		for {
			time.Sleep(3 * time.Minute)
			syncWithMaster(masterURL, privKey, role, apiKey)
		}
	}()

	// Отправка статистики каждую минуту
	go func() {
		for {
			time.Sleep(1 * time.Minute)
			sendStatsToMaster(masterURL, apiKey)
		}
	}()

	// Блокируем main, чтобы программа не завершилась
	select {}
}

// --- ФУНКЦИЯ СИНХРОНИЗАЦИИ И ОБНОВЛЕНИЯ КОНФИГА ---
func syncWithMaster(masterURL, privKey, role string, apiKey string) {
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/sync", masterURL), nil)
	req.Header.Set("X-API-Key", apiKey) // Подставляем ключ

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("❌ Ошибка связи с мастером: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("❌ Мастер отклонил запрос (код %d). Проверьте CLUSTER_API_KEY.", resp.StatusCode)
		return
	}

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
func sendStatsToMaster(masterURL string, apiKey string) {
	// 1. Подключаемся к gRPC API Xray
	// Используем insecure.NewCredentials(), так как это локальное соединение внутри сервера
	conn, err := grpc.Dial("127.0.0.1:10085", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("⚠️ Ошибка подключения к Xray gRPC (локальный тест).")
		sendFakeStats(masterURL, apiKey)
		return
	}
	defer conn.Close()

	client := statscmd.NewStatsServiceClient(conn)

	// 2. Запрашиваем статистику с обнулением
	resp, err := client.QueryStats(context.Background(), &statscmd.QueryStatsRequest{
		Pattern: "",   // Пустой паттерн означает "отдай всё"
		Reset_:  true, // Сбрасываем счетчик в самом Xray после чтения!
	})
	if err != nil {
		log.Printf("⚠️ Не удалось получить статистику от Xray: %v", err)
		sendFakeStats(masterURL, apiKey) // Опять же, для теста в Codespaces
		return
	}

	// 3. Агрегируем трафик
	type UserStat struct {
		Email string `json:"email"`
		Up    int64  `json:"up"`
		Down  int64  `json:"down"`
	}
	statsMap := make(map[string]*UserStat)

	for _, stat := range resp.Stat {
		// Xray отдает имена в формате: user>>>UUID>>>traffic>>>downlink
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) == 4 && parts[0] == "user" {
			email := parts[1]
			direction := parts[3]

			if _, exists := statsMap[email]; !exists {
				statsMap[email] = &UserStat{Email: email}
			}

			switch direction {
			case "downlink":
				statsMap[email].Down += stat.Value
			case "uplink":
				statsMap[email].Up += stat.Value
			}
		}
	}

	var finalStats []UserStat
	for _, stat := range statsMap {
		finalStats = append(finalStats, *stat)
	}

	if len(finalStats) == 0 {
		return // Если трафика не было, не дергаем Мастер
	}

	// 4. Отправляем на Мастер
	body, _ := json.Marshal(finalStats)
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/stats", masterURL), bytes.NewBuffer(body))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	httpResp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("❌ Ошибка связи с мастером при отправке статистики: %v", err)
		return
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		log.Printf("❌ Мастер отклонил статистику (код %d)", httpResp.StatusCode)
		return
	}

	log.Printf("📊 Статистика успешно отправлена на Мастер (юзеров: %d)", len(finalStats))
}

// Заглушка для локального тестирования
func sendFakeStats(masterURL string, apiKey string) {
	stats := []map[string]interface{}{
		{
			"email": "313c0ed4-c7ce-4d59-a162-80bd0f8aca2a", // Не забудь вернуть сюда UUID из базы
			"up":    1024 * 1024 * 5,
			"down":  1024 * 1024 * 50,
		},
	}
	body, _ := json.Marshal(stats)

	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/stats", masterURL), bytes.NewBuffer(body))
	req.Header.Set("X-API-Key", apiKey) // Добавили ключ и сюда!
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	client.Do(req)
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
