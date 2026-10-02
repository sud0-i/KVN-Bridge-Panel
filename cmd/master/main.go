package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/api"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/backup"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/db"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func main() {
	// 1. Загружаем переменные окружения (в Docker они прокинутся из .env файла)
	if err := godotenv.Load(); err != nil {
		log.Println("ℹ️ Файл .env не найден, используем переменные окружения системы")
	}

	cfg := api.Config{
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		JWTSecret:     []byte(os.Getenv("JWT_SECRET")),
		MasterURL:     strings.TrimRight(os.Getenv("MASTER_URL"), "/"),
		DefaultSNI:    envOr("DEFAULT_SNI", "www.microsoft.com"),
		BridgeDirect:  splitList(os.Getenv("BRIDGE_DIRECT")),
		FrontendDir:   "frontend/dist",
		SiteDir:       envOr("SITE_DIR", "data/site"),
	}
	adminPath, err := api.NormalizeAdminPath(os.Getenv("ADMIN_PATH"))
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	cfg.AdminPath = adminPath
	if adminPath == "" {
		log.Println("⚠️ ADMIN_PATH не задан — панель открыта в корне сайта. Задайте его в .env, чтобы спрятать панель.")
	}

	// Проверяем, что критически важные переменные заданы
	if cfg.AdminPassword == "" || len(cfg.JWTSecret) < 32 {
		log.Fatal("❌ Ошибка: ADMIN_PASSWORD не задан или JWT_SECRET короче 32 символов! Проверьте конфигурацию.")
	}
	if cfg.MasterURL == "" {
		if domain := os.Getenv("DOMAIN"); domain != "" && domain != "localhost" {
			cfg.MasterURL = "https://" + domain
		} else {
			log.Println("⚠️ MASTER_URL и DOMAIN не заданы — развёрнутые ноды не смогут связаться с Мастером")
		}
	}

	// 2. Инициализация БД
	db.InitDB(envOr("DB_PATH", "data/db.sqlite"))
	if err := api.EnsureXHTTPPaths(db.DB); err != nil {
		log.Fatalf("❌ Не удалось выдать нодам пути XHTTP: %v", err)
	}
	if err := api.EnsureSubTokens(db.DB); err != nil {
		log.Fatalf("❌ Не удалось выдать пользователям токены подписки: %v", err)
	}

	// Аварийный сброс двухфакторного входа (потерян телефон и резервные коды)
	if len(os.Args) > 1 && os.Args[1] == "reset-2fa" {
		if err := api.ResetTOTP(db.DB); err != nil {
			log.Fatalf("❌ %v", err)
		}
		fmt.Println("✅ Двухфакторный вход выключен — войдите по паролю и включите его заново")
		return
	}

	// Служебная команда установщика: мост на этом же сервере (см. install.sh)
	if len(os.Args) > 1 && os.Args[1] == "local-bridge" {
		if err := localBridge(os.Args[2:]); err != nil {
			log.Fatalf("❌ %v", err)
		}
		return
	}

	// Ежедневные копии базы рядом с ней (в Docker это ./data/backups на хосте)
	dbPath := envOr("DB_PATH", "data/db.sqlite")
	backup.Schedule(db.DB, filepath.Join(filepath.Dir(dbPath), "backups"), 7)

	// 3. Веб-сервер
	e := echo.New()
	e.HideBanner = true
	// Реальный IP клиента берём из X-Forwarded-For, который выставляет Caddy
	e.IPExtractor = echo.ExtractIPFromXFFHeader()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	// CORS не нужен: панель и API отдаются с одного домена

	// API, панель (на секретном пути, если задан ADMIN_PATH) и заглушка в корне
	srv := api.New(cfg, db.DB)
	srv.Register(e)
	// Уведомления в Telegram (если включены в панели)
	go srv.RunNotifier(context.Background())

	log.Println("🚀 Мастер-сервер запускается на порту 8080...")
	e.Logger.Fatal(e.Start(":8080"))
}

// localBridge регистрирует мост на сервере Мастера и печатает в stdout
// переменные для /etc/vpn-agent/.env. Посторонние подключения к :443 Reality
// отправляет в локальный Caddy (127.0.0.1:8443), поэтому снаружи сервер выглядит
// как обычный сайт с настоящим сертификатом, а панель открывается по тому же домену.
func localBridge(args []string) error {
	fs := flag.NewFlagSet("local-bridge", flag.ContinueOnError)
	ip := fs.String("ip", "", "публичный IP сервера")
	domain := fs.String("domain", os.Getenv("DOMAIN"), "домен панели")
	dest := fs.String("dest", "127.0.0.1:8443", "адрес локального Caddy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	parsed := net.ParseIP(*ip)
	if parsed == nil || *domain == "" {
		return fmt.Errorf("нужны корректные -ip и -domain")
	}

	// Повторная установка: пересоздаём ноду с новыми ключами
	db.DB.Where("ip = ?", parsed.String()).Delete(&models.Node{})

	_, secrets, err := api.CreateNode(db.DB, api.NodeSpec{
		IP:     parsed.String(),
		Role:   protocol.RoleBridge,
		SNI:    *domain,
		Domain: *domain,
		Dest:   *dest,
		Xver:   1, // Caddy получит настоящий IP клиента через PROXY protocol
	})
	if err != nil {
		return err
	}
	fmt.Printf("NODE_TOKEN=%s\nPRIVATE_KEY=%s\n", secrets.Token, secrets.PrivateKey)
	return nil
}

// splitList разбирает список через запятую: "a, b,c" → [a b c]
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
