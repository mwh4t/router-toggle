package auth

import (
	"strings"
	"testing"
)

func TestCodeStableAndDistinct(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	a := Code(key, 22001)
	if a != Code(key, 22001) {
		t.Fatal("код не стабилен для одного порта")
	}
	if a == Code(key, 22002) {
		t.Fatal("соседние порты дали одинаковый код")
	}
	if len(a) != codeLen {
		t.Fatalf("длина %d, ожидалась %d", len(a), codeLen)
	}
	if Code([]byte("другой ключ"), 22001) == a {
		t.Fatal("код не зависит от ключа")
	}
}

func TestNormalize(t *testing.T) {
	code := Code([]byte("ключ"), 22001)
	if Normalize(Format(code)) != code {
		t.Fatal("дефисы ломают разбор")
	}
	if Normalize(" "+strings.ToLower(Format(code))+" ") != code {
		t.Fatal("регистр или пробелы ломают разбор")
	}
}

func TestLimiterBlocksAfterFails(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < maxFails-1; i++ {
		l.Fail("1.2.3.4")
	}
	if l.Blocked("1.2.3.4") {
		t.Fatal("заблокировал раньше лимита")
	}
	l.Fail("1.2.3.4")
	if !l.Blocked("1.2.3.4") {
		t.Fatal("не заблокировал после лимита")
	}
	if l.Blocked("5.6.7.8") {
		t.Fatal("заблокировал чужой ip")
	}
}
