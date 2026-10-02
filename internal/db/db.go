package db

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Глобальная переменная для доступа к базе из любого места программы
var DB *gorm.DB

func InitDB(dbPath string) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Fatalf("❌ Не удалось создать папку для БД: %v", err)
	}

	var err error
	// В логи пишем только предупреждения и ошибки: на уровне Info туда попадали бы все запросы с данными юзеров.
	// «record not found» — не ошибка: так выглядят несохранённые настройки (берутся значения
	// по умолчанию) и неизвестные токены подписки; иначе ими забит весь лог.
	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
	})
	if err != nil {
		log.Fatalf("❌ Ошибка подключения к БД: %v", err)
	}

	// Магия GORM: он сам проверит структуры и создаст/обновит таблицы
	err = DB.AutoMigrate(&models.User{}, &models.Node{}, &models.Setting{}, &models.NotifyMessage{}, &models.LinkSample{}, &models.NodeMetric{}, &models.TerminalSession{})
	if err != nil {
		log.Fatalf("❌ Ошибка миграции БД: %v", err)
	}

	// Старые названия ролей → нейтральные
	DB.Model(&models.Node{}).Where("type = ?", "ru_bridge").Update("type", "bridge")
	DB.Model(&models.Node{}).Where("type = ?", "eu_exit").Update("type", "exit")

	log.Println("✅ База данных успешно инициализирована")
}
