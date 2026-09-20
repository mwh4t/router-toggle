package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// файл с правами 600 вне репозитория
type Config struct {
	Listen    string `json:"listen"`
	DBPath    string `json:"db_path"`
	ServerKey string `json:"server_key"` // hex
	AdminCode string `json:"admin_code"`

	TelegramToken  string `json:"telegram_token"`
	TelegramChatID string `json:"telegram_chat_id"`
}

func LoadConfig(path string) (*Config, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, nil, fmt.Errorf("конфигурация %s: %w", path, err)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.DBPath == "" {
		c.DBPath = "/etc/router-toggle/router-toggle.db"
	}
	key, err := hex.DecodeString(c.ServerKey)
	if err != nil || len(key) < 32 {
		return nil, nil, fmt.Errorf("server_key: нужен hex из 32 байт, сгенерировать: openssl rand -hex 32")
	}
	if c.AdminCode == "" {
		return nil, nil, fmt.Errorf("admin_code пустой")
	}
	return &c, key, nil
}

// по одному замку на роутер
type routerLocks struct {
	mu    sync.Mutex
	locks map[int]chan struct{}
}

const lockWait = 30 * time.Second

func newRouterLocks() *routerLocks {
	return &routerLocks{locks: map[int]chan struct{}{}}
}

func (l *routerLocks) acquire(routerID int) bool {
	l.mu.Lock()
	ch, ok := l.locks[routerID]
	if !ok {
		ch = make(chan struct{}, 1)
		l.locks[routerID] = ch
	}
	l.mu.Unlock()

	select {
	case ch <- struct{}{}:
		return true
	case <-time.After(lockWait):
		return false
	}
}

func (l *routerLocks) release(routerID int) {
	l.mu.Lock()
	ch := l.locks[routerID]
	l.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
		}
	}
}
