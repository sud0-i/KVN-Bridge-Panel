package db

import (
	"log"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Глобальная переменная для доступа к базе из любого места программы
var DB *gorm.DB

func InitDB(dbPath string) {
	var err error
	// Открываем подключение с включенным логгером, чтобы видеть SQL запросы
	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		log.Fatalf("❌ Ошибка подключения к БД: %v", err)
	}

	// Магия GORM: он сам проверит структуры и создаст/обновит таблицы
	err = DB.AutoMigrate(&models.User{}, &models.Node{}, &models.Setting{})
	if err != nil {
		log.Fatalf("❌ Ошибка миграции БД: %v", err)
	}

	log.Println("✅ База данных успешно инициализирована")
}
