// уведомления администратору
package notify

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// одно и то же не чаще раза в полчаса
const repeatAfter = 30 * time.Minute

type Telegram struct {
	token  string
	chatID string
	http   *http.Client

	mu   sync.Mutex
	sent map[string]time.Time
}

// пустой токен отключает уведомления
func New(token, chatID string) *Telegram {
	if token == "" || chatID == "" {
		return nil
	}
	return &Telegram{
		token:  token,
		chatID: chatID,
		http:   &http.Client{Timeout: 10 * time.Second},
		sent:   map[string]time.Time{},
	}
}

func (t *Telegram) Send(key, text string) {
	if t == nil {
		return
	}
	if key != "" && t.throttled(key) {
		return
	}
	go t.send(text)
}

func (t *Telegram) throttled(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if at, ok := t.sent[key]; ok && time.Since(at) < repeatAfter {
		return true
	}
	t.sent[key] = time.Now()
	return false
}

func (t *Telegram) send(text string) {
	form := url.Values{}
	form.Set("chat_id", t.chatID)
	form.Set("text", text)
	form.Set("disable_web_page_preview", "true")

	endpoint := "https://api.telegram.org/bot" + t.token + "/sendMessage"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		log.Printf("telegram: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.http.Do(req)
	if err != nil {
		log.Printf("telegram: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		log.Printf("telegram: %s %s", resp.Status, e.Description)
	}
}
