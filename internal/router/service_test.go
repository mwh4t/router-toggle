package router

import (
	"errors"
	"strings"
	"testing"
)

// заглушка
type vpnRunner struct {
	running bool
	failSet bool
}

func (v *vpnRunner) Run(cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, "xkeen -start"):
		if v.failSet {
			return "boom", errors.New("exit status 1")
		}
		v.running = true
		return "", nil
	case strings.Contains(cmd, "xkeen -stop"):
		v.running = false
		return "", nil
	case strings.Contains(cmd, "xkeen_rule"), strings.Contains(cmd, "pidof xray"):
		if v.running {
			return "yes\n", nil
		}
		return "no\n", nil
	}
	return "", errors.New("unexpected: " + cmd)
}

func init() { vpnPoll = 0 }

func TestApplyVPN(t *testing.T) {
	r := &vpnRunner{running: true}
	if on, err := ApplyVPN(r, Keenetic{}, false); err != nil || on {
		t.Fatalf("выключение: on=%v err=%v", on, err)
	}
	if on, err := ApplyVPN(r, Keenetic{}, true); err != nil || !on {
		t.Fatalf("включение: on=%v err=%v", on, err)
	}
}

func TestApplyVPNFails(t *testing.T) {
	r := &vpnRunner{running: false, failSet: true}
	if _, err := ApplyVPN(r, Keenetic{}, true); !errors.Is(err, ErrServiceFailed) {
		t.Fatalf("ожидал ErrServiceFailed, получил %v", err)
	}
}

func TestYesNoRejectsGarbage(t *testing.T) {
	r := &vpnRunner{}
	if _, err := yesNo(r, "что-то непонятное"); err == nil {
		t.Fatal("ожидал ошибку на неизвестной команде")
	}
}

// заглушка для проверки состояния
type healthRunner struct {
	vpnOn  bool
	probed bool
}

func (h *healthRunner) Run(cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, "rt-probe"):
		h.probed = true
		return "yes\n", nil
	case strings.Contains(cmd, "xkeen_rule"):
		if h.vpnOn {
			return "yes\n", nil
		}
		return "no\n", nil
	case strings.Contains(cmd, "ping"), strings.Contains(cmd, "pidof xray"):
		return "yes\n", nil
	}
	return "", errors.New("unexpected: " + cmd)
}

func TestCheckHealthProbe(t *testing.T) {
	r := &healthRunner{vpnOn: true}
	h, err := CheckHealth(r, Keenetic{}, true)
	if err != nil || !r.probed || !h.VPSKnown || !h.VPS {
		t.Fatalf("канал при включённом vpn: %+v probed=%v err=%v", h, r.probed, err)
	}

	r = &healthRunner{vpnOn: false}
	if h, err = CheckHealth(r, Keenetic{}, true); err != nil || r.probed || h.VPSKnown {
		t.Fatalf("при выключенном vpn канал не проверяется: %+v probed=%v err=%v", h, r.probed, err)
	}

	r = &healthRunner{vpnOn: true}
	if h, err = CheckHealth(r, Keenetic{}, false); err != nil || r.probed {
		t.Fatalf("ручной режим без проверки канала: %+v probed=%v err=%v", h, r.probed, err)
	}
}
