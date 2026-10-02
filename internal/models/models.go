package models

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// User - профиль клиента
type User struct {
	ID   string `gorm:"primaryKey;type:uuid"`
	Name string `gorm:"uniqueIndex;not null"`
	// SubToken — адрес подписки /sub/<SubToken>. Отдельно от ID (он же ключ VLESS),
	// чтобы переносить старые ссылки из других панелей и менять ссылку, не меняя ключ.
	SubToken     string `gorm:"uniqueIndex"`
	TrafficUp    int64  `gorm:"default:0"`
	TrafficDown  int64  `gorm:"default:0"`
	TrafficQuota int64  `gorm:"default:0"` // байты, 0 = без лимита
	IPLimit      int    `gorm:"default:5"`
	ExpiresAt    *time.Time
	Status       string `gorm:"default:'active'"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewSubToken — случайный токен подписки (32 hex-символа)
func NewSubToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand не отказывает на нормальной системе
	}
	return hex.EncodeToString(b)
}

// BeforeCreate гарантирует токен подписки: пустые строки нарушили бы уникальный индекс
func (u *User) BeforeCreate(*gorm.DB) error {
	if u.SubToken == "" {
		u.SubToken = NewSubToken()
	}
	return nil
}

// CanConnect — активен, не истёк и не выбрал квоту
func (u User) CanConnect(now time.Time) bool {
	if u.Status != "active" {
		return false
	}
	if u.ExpiresAt != nil && now.After(*u.ExpiresAt) {
		return false
	}
	if u.TrafficQuota > 0 && u.TrafficUp+u.TrafficDown >= u.TrafficQuota {
		return false
	}
	return true
}

// Node - удаленный сервер (мост или экзит)
type Node struct {
	IP     string `gorm:"primaryKey"`
	Type   string `gorm:"not null"` // "bridge" или "exit"
	Domain string
	SNI    string
	// RealityDest — куда Reality отправляет посторонних (пусто = SNI:443).
	// Для режима «мастер + мост» это локальный Caddy с панелью.
	RealityDest string
	RealityXver int
	PubKey      string
	SID         string
	LinkUUID    string // мост авторизуется на экзитах под этим UUID
	TokenHash   string `gorm:"index" json:"-"` // sha256 от токена агента, сам токен не храним
	SSPass      string `json:"-"`
	XHTTPPath   string
	// Label — подпись в приложениях пользователей, например «🇳🇱 Амстердам». Пусто — «KVN <адрес>».
	Label string
	// CDNDomain — домен ноды за CDN (Cloudflare и т.п.): «cdn.example.com» → IP ноды через CDN.
	// Пусто — через CDN к ноде не подключаются.
	CDNDomain string
	// Hy2Cert — самоподписанный сертификат Hysteria2 (PEM), который прислал агент.
	// Публичный: клиенты проверяют по нему сервер. Пусто — нода ещё не прислала.
	Hy2Cert string `json:"-"`
	// Что агент сообщает о себе: версии, свежесть геобаз, ошибка последнего обновления
	AgentVersion string
	XrayVersion  string
	GeoUpdated   string
	UpdateError  string
	// Mieru: версия работающего mita (пусто — не запущен) и почему не запустился
	MieruVersion string
	MieruError   string
	// Действие из панели: ждёт выдачи агенту (PendingAction) и итог последнего
	PendingAction string
	PendingAt     time.Time
	LastAction    string
	LastActionAt  time.Time
	ActionResult  string
	// SSH: SSHKeysOnly — администратор выключил вход по паролю; SSHState — что сообщает
	// агент; SSHHostKey — ключ сервера, запомненный при первом входе (защита от подмены)
	SSHKeysOnly bool
	SSHState    string
	SSHHostKey  string `json:"-"`
	Mode        string
	IsOnline    bool   `gorm:"default:false"`
	WarpOK      bool   // на ноде работает локальный прокси Cloudflare WARP
	ConfigError string // последняя ошибка применения конфига (пусто — всё хорошо)
	SNIError    string // сайт для маскировки недоступен с ноды (пусто — всё хорошо)
	WarpError   string // почему WARP не работает (пусто — работает или не нужен)
	LastSeen    time.Time
	CreatedAt   time.Time
}

// Setting - глобальные настройки кластера
type Setting struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

// NotifyMessage — уведомление в Telegram в очереди на отправку.
// Отправляет его выходная нода (AssignedTo — её IP) или сам мастер ("master").
type NotifyMessage struct {
	ID         uint `gorm:"primaryKey"`
	Text       string
	CreatedAt  time.Time
	AssignedTo string
	AssignedAt time.Time
	Attempts   int
	SentAt     *time.Time `gorm:"index"`
	Failed     bool       `gorm:"index"`
	Error      string
}

// LinkSample — замер канала мост → выходная нода за минуту (хранится 8 дней)
type LinkSample struct {
	ID    uint      `gorm:"primaryKey"`
	From  string    `gorm:"index:idx_link"` // IP моста
	To    string    `gorm:"index:idx_link"` // IP выходной ноды
	At    time.Time `gorm:"index"`
	Sent  int
	Lost  int
	AvgMs float64
	MaxMs float64
}

// NodeMetric — показатели сервера за минуту (хранятся 8 дней). nil — не измерено
// (первый замер после запуска агента)
type NodeMetric struct {
	ID          uint      `gorm:"primaryKey"`
	Node        string    `gorm:"index:idx_metric_node"`
	At          time.Time `gorm:"index"`
	CPU         *float64
	Steal       *float64
	Mem         float64
	MemTotalMB  int
	Disk        float64
	DiskTotalGB float64
	Load1       float64
	Cores       int
	RxBps       *float64
	TxBps       *float64
	Retrans     *float64
	Conns       int
	UptimeSec   int64
}

// TerminalSession — журнал входов в терминал из панели
type TerminalSession struct {
	ID        uint `gorm:"primaryKey"`
	Node      string
	RemoteIP  string
	StartedAt time.Time `gorm:"index"`
	EndedAt   *time.Time
	Reason    string // чем закончилась: вышли, простой, ошибка
}
