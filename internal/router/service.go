package router

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// служба не переключилась
var ErrServiceFailed = errors.New("служба не переключилась")

// служба останавливается не мгновенно
var vpnPoll = 2 * time.Second

func ApplyVPN(r Runner, c Controller, on bool) (bool, error) {
	if err := c.SetVPN(r, on); err != nil {
		return false, fmt.Errorf("%w: %v", ErrServiceFailed, err)
	}
	var state bool
	var err error
	for i := 0; i < 5; i++ {
		if state, err = c.VPNState(r); err != nil {
			return false, err
		}
		if state == on {
			return state, nil
		}
		time.Sleep(vpnPoll)
	}
	return state, fmt.Errorf("%w: после переключения vpn=%v, ожидалось %v", ErrServiceFailed, state, on)
}

type Health struct {
	Internet bool
	VPNOn    bool
	Proxy    bool
	VPS      bool
	VPSKnown bool
}

// проверка соединения с vps пропускается
func CheckHealth(r Runner, c Controller, vpsIP string) (Health, error) {
	var h Health
	var err error

	if h.Internet, err = yesNo(r, "ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1 && echo yes || echo no"); err != nil {
		return h, err
	}
	if h.VPNOn, err = c.VPNState(r); err != nil {
		return h, err
	}
	if h.Proxy, err = c.ProxyRunning(r); err != nil {
		return h, err
	}
	if vpsIP != "" {
		h.VPSKnown = true
		cmd := fmt.Sprintf("netstat -tn 2>/dev/null | grep ESTABLISHED | grep -q %s && echo yes || echo no",
			shq(vpsIP+":443"))
		if h.VPS, err = yesNo(r, cmd); err != nil {
			return h, err
		}
	}
	return h, nil
}

// команда обязана печатать yes или no
func yesNo(r Runner, cmd string) (bool, error) {
	out, err := r.Run(cmd)
	if err != nil {
		return false, fmt.Errorf("%s: %w (%s)", cmd, err, strings.TrimSpace(out))
	}
	switch strings.TrimSpace(out) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, fmt.Errorf("%s: неожиданный ответ %q", cmd, strings.TrimSpace(out))
}
