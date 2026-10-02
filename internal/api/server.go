// Package api — HTTP API Мастера: панель администратора, агенты и подписки.
package api

import (
	"net/http"
	"path/filepath"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

// Config — настройки Мастера (читаются из окружения в main)
type Config struct {
	AdminPassword string
	JWTSecret     []byte
	MasterURL     string // адрес, по которому агенты ходят к Мастеру
	DefaultSNI    string
	BridgeDirect  []string // начальное значение правил «напрямую с моста» (BRIDGE_DIRECT)
	// AdminPath — секретный путь панели («/abc…», см. NormalizeAdminPath). Пусто — в корне.
	AdminPath   string
	FrontendDir string // собранный Vue; пусто — интерфейс не раздаём (тесты)
	AgentBinary string // бинарник агента для самообновления нод; пусто — build/agent_linux_amd64
	SiteDir     string // свой сайт вместо заглушки, если там есть index.html
	// SSHKeyPath — SSH-ключ мастера для входа на ноды; пусто — data/ssh/id_ed25519
	SSHKeyPath string
}

// NodeAliveTimeout — нода без синхронизации дольше этого срока считается офлайн
const NodeAliveTimeout = 10 * time.Minute

type Server struct {
	agentBin *agentBinary
	sshKey   *masterKey
	latest   latestCache
	cfg      Config
	db       *gorm.DB
	// deploy запускает деплой в фоне; вынесен в поле, чтобы в тестах не запускать настоящий Ansible
	deploy func(runner.DeployParams)
	now    func() time.Time
	// sshAddr — адрес SSH ноды; nil — IP:22 (в тестах — локальный сервер)
	sshAddr func(ip string) string
	term    terminals
}

func New(cfg Config, db *gorm.DB) *Server {
	bin := cfg.AgentBinary
	if bin == "" {
		bin = envOr("AGENT_BINARY", "build/agent_linux_amd64") // тот же, что ставит Ansible
	}
	keyPath := cfg.SSHKeyPath
	if keyPath == "" {
		keyPath = filepath.Join(filepath.Dir(envOr("DB_PATH", "data/db.sqlite")), "ssh", "id_ed25519")
	}
	return &Server{cfg: cfg, db: db, deploy: func(p runner.DeployParams) { go runner.DeployNode(p) }, now: time.Now,
		agentBin: &agentBinary{path: bin}, sshKey: &masterKey{path: keyPath}}
}

// Register вешает все роуты на Echo
func (s *Server) Register(e *echo.Echo) {
	s.registerSite(e)
	api := e.Group(s.adminAPI())

	// Не больше 5 попыток входа в минуту с одного IP
	loginLimiter := middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(5.0 / 60.0),
			Burst:     5,
			ExpiresIn: 10 * time.Minute,
		}),
	})
	api.POST("/login", s.login, loginLimiter)

	admin := s.requireAdmin
	api.GET("/ping", s.ping, admin)

	api.GET("/users", s.listUsers, admin)
	api.POST("/users", s.createUser, admin)
	api.POST("/users/import", s.importUsers, admin)
	api.PATCH("/users/:id", s.updateUser, admin)
	api.POST("/users/:id/reset-traffic", s.resetUserTraffic, admin)
	api.POST("/users/:id/rotate", s.rotateUser, admin)
	api.PATCH("/users/:id/status", s.setUserStatus, admin)
	api.DELETE("/users/:id", s.deleteUser, admin)

	api.GET("/nodes", s.listNodes, admin)
	api.POST("/nodes", s.createNode, admin)
	api.PATCH("/nodes/:ip", s.updateNode, admin)
	api.POST("/nodes/:ip/redeploy", s.redeployNode, admin)
	api.DELETE("/nodes/:ip", s.deleteNode, admin)

	api.GET("/backup", s.downloadBackup, admin)

	api.GET("/settings", s.getSettings, admin)
	api.PUT("/settings", s.saveSettings, admin)
	api.GET("/xray/latest", s.latestXray, admin)
	api.GET("/sub-preview", s.subPreview, admin)
	api.GET("/links", s.getLinks, admin)
	api.GET("/ssh-keys", s.getSSHKeys, admin)
	api.PUT("/ssh-keys", s.saveSSHKeys, admin)
	api.POST("/nodes/:ip/ssh", s.setNodeSSH, admin)
	api.POST("/nodes/:ip/terminal", s.terminalTicket, admin, loginLimiter)
	api.GET("/terminal", s.terminalWS) // авторизация — одноразовым пропуском
	api.GET("/terminal/log", s.terminalLog, admin)
	api.GET("/metrics", s.getMetrics, admin)
	api.POST("/nodes/:ip/action", s.nodeAction, admin)
	api.GET("/2fa", s.getTOTP, admin)
	api.POST("/2fa/setup", s.setupTOTP, admin)
	api.POST("/2fa/enable", s.enableTOTP, admin)
	api.POST("/2fa/disable", s.disableTOTP, admin, loginLimiter)
	api.GET("/notify", s.getNotify, admin)
	api.PUT("/notify", s.saveNotify, admin)
	api.POST("/notify/test", s.testNotify, admin)
	api.GET("/page-settings", s.getPageSettings, admin)
	api.PUT("/page-settings", s.savePageSettings, admin)

	// Эндпоинты для агентов: авторизация по персональному токену ноды.
	// Путь не секретный — он записан у агентов, — но без токена это обычная 404.
	agents := e.Group("/api")
	agents.GET("/sync", s.sync, s.requireNode)
	agents.POST("/stats", s.stats, s.requireNode)
	agents.GET("/agent/binary", s.agentBinaryHandler, s.requireNode)

	// Публичная страница подписки
	// Приложение обновляет подписку раз в несколько часов; 20 запросов в минуту с IP
	// хватает с запасом, а перебор токенов упирается в лимит
	subLimiter := middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(20.0 / 60.0),
			Burst:     20,
			ExpiresIn: 10 * time.Minute,
		}),
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return c.HTML(http.StatusTooManyRequests, "<h1>Too Many Requests</h1>")
		},
		ErrorHandler: func(c echo.Context, _ error) error { return notFound(c) },
	})
	e.GET("/sub/:token", s.subscription, subLimiter)
}

func jsonError(c echo.Context, code int, msg string) error {
	return c.JSON(code, map[string]string{"error": msg})
}

func (s *Server) ping(c echo.Context) error {
	return c.JSON(200, map[string]string{"message": "pong"})
}
