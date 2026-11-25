package models

import "time"

type AWGDevice struct {
	ID           string `gorm:"primaryKey"`
	UserID       string `gorm:"index"`
	NodeIP       string `gorm:"index;uniqueIndex:awg_node_ip"`
	Address      string `gorm:"uniqueIndex:awg_node_ip"`
	Label        string
	PublicKey    string    `gorm:"index" json:"-"`
	InviteHash   string    `json:"-"`
	InviteUntil  time.Time `json:"-"`
	Revoked      bool
	Handshake    int64
	TrafficUp    int64
	TrafficDown  int64
	CounterEpoch string `json:"-"`
	CounterRx    int64  `json:"-"`
	CounterTx    int64  `json:"-"`
}
