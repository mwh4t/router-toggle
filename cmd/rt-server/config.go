package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// файл с правами 600 вне репозитория
type Config struct {
	Listen  string         `json:"listen"`
	Routers []RouterConfig `json:"routers"`
}

type RouterConfig struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Firmware string `json:"firmware"`

	TunnelPort int `json:"tunnel_port"`

	SSHUser    string `json:"ssh_user"`
	AuthType   string `json:"auth_type"`
	AuthSecret string `json:"auth_secret"`

	HostKey string `json:"host_key"`

	AllowUnknownHostKey bool `json:"allow_unknown_host_key,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("конфигурация %s: %w", path, err)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	seen := map[int]bool{}
	for _, r := range c.Routers {
		if seen[r.ID] {
			return nil, fmt.Errorf("дублирующийся router id %d", r.ID)
		}
		seen[r.ID] = true
		if r.TunnelPort == 0 || r.SSHUser == "" || r.Firmware == "" {
			return nil, fmt.Errorf("роутер %d: не заполнены обязательные поля", r.ID)
		}
	}
	return &c, nil
}

func (c *Config) router(id int) (RouterConfig, bool) {
	for _, r := range c.Routers {
		if r.ID == id {
			return r, true
		}
	}
	return RouterConfig{}, false
}

// кэш показывать только когда роутер молчит
type stateCache struct {
	mu   sync.Mutex
	data map[cacheKey]cacheEntry
}

type cacheKey struct {
	routerID int
	op       string
}

type cacheEntry struct {
	value  bool
	readAt time.Time
}

func newStateCache() *stateCache {
	return &stateCache{data: map[cacheKey]cacheEntry{}}
}

func (c *stateCache) put(routerID int, op string, value bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[cacheKey{routerID, op}] = cacheEntry{value: value, readAt: time.Now().UTC()}
}

func (c *stateCache) get(routerID int, op string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[cacheKey{routerID, op}]
	return e, ok
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
