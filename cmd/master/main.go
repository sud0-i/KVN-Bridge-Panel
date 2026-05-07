package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/db"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"golang.org/x/crypto/curve25519"
	"gorm.io/gorm"
)

func main() {
	// 1. Подключаемся к базе (файл core.db создастся в корне проекта)
	db.InitDB("core.db")

	// 2. Инициализируем веб-фреймворк Echo
	e := echo.New()

	// Полезные мидлвари (логирование запросов, защита от падений и CORS для фронтенда)
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	// 3. Создаем группу роутов для API
	api := e.Group("/api")

	// Эндпоинт для проверки, что сервер жив
	api.GET("/ping", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"message": "pong"})
	})

	// --- Управление Пользователями ---

	// ЧТЕНИЕ: Получить всех пользователей
	api.GET("/users", func(c echo.Context) error {
		var users []models.User
		if err := db.DB.Find(&users).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Ошибка БД"})
		}
		return c.JSON(http.StatusOK, users)
	})

	// СОЗДАНИЕ: Добавить нового пользователя
	api.POST("/users", func(c echo.Context) error {
		// Описываем, что ждем от фронтенда
		var req struct {
			Name    string `json:"name"`
			IPLimit int    `json:"ip_limit"`
		}

		// Парсим JSON из запроса
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Неверный формат данных"})
		}

		// Создаем новую запись
		newUser := models.User{
			ID:      uuid.NewString(), // Генерируем UUID
			Name:    req.Name,
			IPLimit: req.IPLimit,
			Status:  "active",
		}
		if newUser.IPLimit == 0 {
			newUser.IPLimit = 5 // Дефолтное значение
		}

		// Сохраняем в базу через GORM
		if err := db.DB.Create(&newUser).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Ошибка сохранения: " + err.Error()})
		}

		return c.JSON(http.StatusCreated, newUser)
	})
	// --- Управление Узлами (Нодами) ---

	// Получить все ноды
	api.GET("/nodes", func(c echo.Context) error {
		var nodes []models.Node
		if err := db.DB.Find(&nodes).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Ошибка БД"})
		}
		return c.JSON(http.StatusOK, nodes)
	})

	// Добавить и развернуть новую ноду
	api.POST("/nodes", func(c echo.Context) error {
		var req struct {
			IP       string `json:"ip"`
			Type     string `json:"type"`     // "ru_bridge" или "eu_exit"
			Password string `json:"password"` // Пароль не храним в БД, используем только для Ansible!
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Неверный формат"})
		}

		// Генерируем криптографию для новой ноды
		pub, priv, sid := generateRealityKeys()

		// 1. Создаем запись в базе
		newNode := models.Node{
			IP:       req.IP,
			Type:     req.Type,
			PubKey:   pub,
			SID:      sid,
			SNI:      "www.microsoft.com", // Дефолтный SNI
			IsOnline: false,               // Изначально офлайн, пока идет деплой
		}

		if err := db.DB.Create(&newNode).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Ошибка БД"})
		}

		// 2. ЗАПУСКАЕМ ANSIBLE
		// Теперь мы передаем приватный ключ в Runner, чтобы Ansible положил его на сервер!
		go runner.DeployNode(req.IP, req.Type, req.Password, priv)

		// 3. Сразу отвечаем фронтенду, что процесс пошел
		return c.JSON(http.StatusAccepted, map[string]string{
			"message": "Установка запущена! Это займет 2-3 минуты.",
			"ip":      req.IP,
		})
	})

	// ИЗМЕНЕНИЕ СТАТУСА (Блокировка/Разблокировка)
	api.PATCH("/users/:id/status", func(c echo.Context) error {
		id := c.Param("id")
		var req struct {
			Status string `json:"status"` // "active" или "blocked"
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "bad format"})
		}

		if err := db.DB.Model(&models.User{}).Where("id = ?", id).Update("status", req.Status).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "DB error"})
		}
		return c.NoContent(http.StatusOK)
	})

	// УДАЛЕНИЕ ПОЛЬЗОВАТЕЛЯ
	api.DELETE("/users/:id", func(c echo.Context) error {
		id := c.Param("id")
		if err := db.DB.Where("id = ?", id).Delete(&models.User{}).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "DB error"})
		}
		return c.NoContent(http.StatusNoContent)
	})

	// --- Технические эндпоинты для Агентов ---

	// 1. СИНХРОНИЗАЦИЯ: Агент запрашивает актуальные данные
	api.GET("/sync", func(c echo.Context) error {
		// Получаем всех активных пользователей
		var users []models.User
		db.DB.Where("status = ?", "active").Find(&users)

		// Получаем все активные EU-ноды (чтобы RU-мосты знали, куда направлять трафик)
		var exits []models.Node
		db.DB.Where("type = ?", "eu_exit").Where("is_online = ?", true).Find(&exits)

		// Формируем JSON-ответ ровно в том формате, который ждет твой Агент
		response := map[string]interface{}{
			"sni":          "www.microsoft.com",        // Глобальный SNI для маскировки
			"ru_sni":       "sber.ru",                  // Пример SNI для моста
			"warp_domains": "geosite:google,domain:ru", // Домены, которые пойдут через WARP
			"users":        users,                      // Агент сам вытащит ID (UUID) из этого массива
			"exits":        exits,                      // Агент получит IP и ключи экзитов
		}

		return c.JSON(http.StatusOK, response)
	})

	// 2. СТАТИСТИКА: Агент присылает данные о потребленном трафике
	api.POST("/stats", func(c echo.Context) error {
		// Агент присылает массив объектов: [{"email": "uuid", "up": 123, "down": 456}]
		var req []struct {
			Email string `json:"email"` // У Xray юзеры называются email
			Up    int64  `json:"up"`
			Down  int64  `json:"down"`
		}

		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "bad format"})
		}

		// Проходимся по массиву и обновляем данные каждого юзера в базе
		for _, stat := range req {
			// Используем GORM для атомарного инкремента (прибавляем новые байты к старым)
			db.DB.Model(&models.User{}).
				Where("id = ?", stat.Email).
				Updates(map[string]interface{}{
					"traffic_up":   gorm.Expr("traffic_up + ?", stat.Up),
					"traffic_down": gorm.Expr("traffic_down + ?", stat.Down),
				})
		}

		return c.String(http.StatusOK, "OK")
	})

	// --- ПУБЛИЧНЫЕ ЭНДПОИНТЫ (ПОДПИСКИ) ---

	e.GET("/sub/:id", func(c echo.Context) error {
		userID := c.Param("id")

		// Ищем юзера
		var user models.User
		if err := db.DB.Where("id = ?", userID).First(&user).Error; err != nil {
			return c.HTML(http.StatusNotFound, "<h1>❌ Пользователь не найден или удален</h1>")
		}

		if user.Status != "active" {
			return c.HTML(http.StatusForbidden, "<h1>🚫 Подписка неактивна</h1>")
		}

		// Ищем все живые RU-мосты
		var bridges []models.Node
		db.DB.Where("type = ?", "ru_bridge").Where("is_online = ?", true).Find(&bridges)

		// Генерируем VLESS-ссылки для каждого моста
		var vlessLinks []string
		for _, b := range bridges {
			// Используем домен, если есть, иначе IP
			address := b.Domain
			if address == "" {
				address = b.IP
			}

			// Формируем классическую ссылку VLESS Reality / XHTTP
			// Замени параметры на те, что реально использует твой Xray
			link := fmt.Sprintf(
				"vless://%s@%s:443?type=grpc&security=reality&pbk=%s&fp=chrome&sni=%s&sid=%s&serviceName=grpc#%s",
				user.ID, address, b.PubKey, b.SNI, b.SID, "RU-"+address,
			)
			vlessLinks = append(vlessLinks, link)
		}

		allLinks := strings.Join(vlessLinks, "\n")

		// УМНАЯ ОТДАЧА: Проверяем, кто пришел (человек или программа)
		userAgent := c.Request().Header.Get("User-Agent")
		isBrowser := strings.Contains(userAgent, "Mozilla") || strings.Contains(userAgent, "Chrome") || strings.Contains(userAgent, "Safari")

		if !isBrowser {
			// Если это VPN-клиент, отдаем Base64
			encoded := base64.StdEncoding.EncodeToString([]byte(allLinks))
			return c.String(http.StatusOK, encoded)
		}

		// Если это человек в браузере — отдаем красивую HTML
		html := `
		<!DOCTYPE html>
		<html lang="ru">
		<head>
			<meta charset="UTF-8">
			<meta name="viewport" content="width=device-width, initial-scale=1.0">
			<title>Ваш VPN</title>
			<script src="https://cdn.tailwindcss.com"></script>
		</head>
		<body class="bg-gray-900 text-white min-h-screen flex items-center justify-center p-4">
			<div class="bg-gray-800 p-8 rounded-2xl shadow-xl max-w-md w-full text-center border border-gray-700">
				<h1 class="text-2xl font-bold text-blue-400 mb-2">Привет, ` + user.Name + `! 🚀</h1>
				<p class="text-gray-400 mb-6">Это твоя персональная страница управления VPN.</p>
				
				<div class="bg-gray-900 p-4 rounded-xl mb-6 break-all text-sm text-gray-300 font-mono border border-gray-700">
					` + allLinks + `
				</div>

				<button onclick="navigator.clipboard.writeText('` + allLinks + `'); alert('Скопировано!');" 
					class="w-full bg-blue-600 hover:bg-blue-500 text-white font-bold py-3 px-4 rounded-xl transition duration-200 mb-4 shadow-lg">
					📋 Скопировать конфигурацию
				</button>
				
				<p class="text-xs text-gray-500 mt-4">Вставь этот код в приложение V2rayTun, Hiddify или V2Box.</p>
			</div>
		</body>
		</html>
		`
		return c.HTML(http.StatusOK, html)
	})

	// 4. Запускаем сервер
	log.Println("🚀 Master API запускается на порту 8080...")
	e.Logger.Fatal(e.Start(":8080"))

}

func generateRealityKeys() (pubKey, privKey, sid string) {
	// 1. Приватный ключ (случайные 32 байта)
	var priv [32]byte
	rand.Read(priv[:])

	// 2. Публичный ключ (вычисляется из приватного по кривой 25519)
	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)

	// Xray использует Base64-URL кодировку без паддинга (символов =)
	pubKey = base64.RawURLEncoding.EncodeToString(pub[:])
	privKey = base64.RawURLEncoding.EncodeToString(priv[:])

	// 3. Генерация Short ID (8 случайных hex символов = 4 байта)
	sidBytes := make([]byte, 4)
	rand.Read(sidBytes)
	sid = hex.EncodeToString(sidBytes)

	return pubKey, privKey, sid
}
