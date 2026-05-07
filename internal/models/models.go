package models

import (
	"time"
)

// User - профиль клиента
type User struct {
	ID           string `gorm:"primaryKey;type:uuid"`
	Name         string `gorm:"uniqueIndex;not null"`
	TrafficUp    int64  `gorm:"default:0"`
	TrafficDown  int64  `gorm:"default:0"`
	TrafficQuota int64  `gorm:"default:0"`
	IPLimit      int    `gorm:"default:5"`
	ExpiresAt    *time.Time
	Status       string `gorm:"default:'active'"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Node - удаленный сервер (мост или экзит)
type Node struct {
	IP        string `gorm:"primaryKey"`
	Type      string `gorm:"not null"` // "ru_bridge" или "eu_exit"
	Domain    string
	SNI       string
	PubKey    string
	SID       string
	SSPass    string
	XHTTPPath string
	Mode      string
	IsOnline  bool `gorm:"default:false"`
	LastSeen  time.Time
	CreatedAt time.Time
}

// Setting - глобальные настройки кластера
type Setting struct {
	Key   string `gorm:"primaryKey"`
	Value string
}
