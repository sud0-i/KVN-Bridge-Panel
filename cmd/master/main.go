package main

import (
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/db"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
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

	// 4. Запускаем сервер
	log.Println("🚀 Master API запускается на порту 8080...")
	e.Logger.Fatal(e.Start(":8080"))
}
