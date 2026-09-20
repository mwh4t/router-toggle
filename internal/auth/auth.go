// коды доступа и лимит попыток
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"
)

// без похожих символов
const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const codeLen = 12

// код выводится из порта туннеля
func Code(serverKey []byte, tunnelPort int) string {
	mac := hmac.New(sha256.New, serverKey)
	fmt.Fprintf(mac, "router:%d", tunnelPort)
	sum := mac.Sum(nil)

	var b strings.Builder
	for i := 0; i < codeLen; i++ {
		b.WriteByte(alphabet[int(sum[i])%len(alphabet)])
	}
	return b.String()
}

// вид для выдачи человеку
func Format(code string) string {
	var parts []string
	for i := 0; i < len(code); i += 4 {
		end := i + 4
		if end > len(code) {
			end = len(code)
		}
		parts = append(parts, code[i:end])
	}
	return strings.Join(parts, "-")
}

func Normalize(input string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(input) {
		if strings.ContainsRune(alphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func Equal(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

const (
	maxFails    = 5
	blockFor    = time.Minute
	failsExpire = time.Minute
)

// лимит попыток по ip
type Limiter struct {
	mu    sync.Mutex
	fails map[string]*counter
}

type counter struct {
	n       int
	firstAt time.Time
	until   time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{fails: map[string]*counter{}}
}

func (l *Limiter) Blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.fails[ip]
	if !ok {
		return false
	}
	if time.Now().Before(c.until) {
		return true
	}
	if time.Since(c.firstAt) > failsExpire {
		delete(l.fails, ip)
	}
	return false
}

func (l *Limiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.fails[ip]
	if !ok || time.Since(c.firstAt) > failsExpire {
		l.fails[ip] = &counter{n: 1, firstAt: time.Now()}
		return
	}
	c.n++
	if c.n >= maxFails {
		c.until = time.Now().Add(blockFor)
		c.n = 0
		c.firstAt = time.Now()
	}
}

func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
