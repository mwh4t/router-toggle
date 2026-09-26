package router

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type Keenetic struct{}

const (
	keeneticPortListPath = "/opt/etc/xkeen/port_proxying.lst"
	keeneticRoutingPath  = "/opt/etc/xray/configs/05_routing.json"

	keeneticUDPRangeColon = "27000:27100" // формат port_proxying.lst
	keeneticUDPRangeDash  = "27000-27100" // формат 05_routing.json
)

// список портов в правиле vless-reality/udp
var keeneticUDPPortRe = regexp.MustCompile(
	`(?s)("outboundTag":\s*"vless-reality",\s*"network":\s*"udp",\s*"port":\s*")([^"]*)(")`,
)

func (Keenetic) Firmware() string   { return FirmwareKeenetic }
func (Keenetic) SupportedOps() []Op { return []Op{OpUDPProxy} }

func (k Keenetic) ReadState(r Runner, op Op) (bool, error) {
	if op != OpUDPProxy {
		return false, ErrUnsupportedOp
	}

	lst, err := readFile(r, keeneticPortListPath)
	if err != nil {
		return false, err
	}
	routing, err := readFile(r, keeneticRoutingPath)
	if err != nil {
		return false, err
	}

	ports, err := keeneticPorts(routing)
	if err != nil {
		return false, err
	}

	inList := lineListHas(lst, keeneticUDPRangeColon)
	inRouting := contains(ports, keeneticUDPRangeDash)

	if inList != inRouting {
		return false, fmt.Errorf("%w: диапазон %s есть в %s=%v, в %s=%v",
			ErrInconsistent, keeneticUDPRangeDash,
			keeneticPortListPath, inList, keeneticRoutingPath, inRouting)
	}
	return inList, nil
}

func (k Keenetic) Plan(r Runner, op Op, value bool) (*Plan, error) {
	if op != OpUDPProxy {
		return nil, ErrUnsupportedOp
	}

	lst, err := readFile(r, keeneticPortListPath)
	if err != nil {
		return nil, err
	}
	routing, err := readFile(r, keeneticRoutingPath)
	if err != nil {
		return nil, err
	}

	plan := &Plan{Op: op, Value: value}

	newLst := toggleLineList(lst, keeneticUDPRangeColon, value)
	if newLst != lst {
		plan.Changes = append(plan.Changes, FileChange{
			Path:    keeneticPortListPath,
			Before:  lst,
			Content: newLst,
			Validate: func(content string) error {
				if lineListHas(content, keeneticUDPRangeColon) != value {
					return fmt.Errorf("строка %s не в ожидаемом состоянии", keeneticUDPRangeColon)
				}
				return nil
			},
		})
	}

	newRouting, err := keeneticSetPort(routing, keeneticUDPRangeDash, value)
	if err != nil {
		return nil, err
	}
	if newRouting != routing {
		plan.Changes = append(plan.Changes, FileChange{
			Path:    keeneticRoutingPath,
			Before:  routing,
			Content: newRouting,
			Validate: func(content string) error {
				if !json.Valid([]byte(content)) {
					return fmt.Errorf("результат не является корректным JSON")
				}
				ports, err := keeneticPorts(content)
				if err != nil {
					return err
				}
				if contains(ports, keeneticUDPRangeDash) != value {
					return fmt.Errorf("диапазон %s не в ожидаемом состоянии", keeneticUDPRangeDash)
				}
				return nil
			},
		})
	}

	return plan, nil
}

func (Keenetic) Restart(r Runner) error {
	out, err := r.Run("if iptables -t mangle -S 2>/dev/null | grep -q xkeen_rule; then xkeen -restart && sleep 2 && pidof xray >/dev/null; fi")
	if err != nil {
		return fmt.Errorf("xkeen -restart завершился с ошибкой: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// правила перенаправления
func (Keenetic) VPNState(r Runner) (bool, error) {
	return yesNo(r, "iptables -t mangle -S 2>/dev/null | grep -q xkeen_rule && echo yes || echo no")
}

func (Keenetic) SetVPN(r Runner, on bool) error {
	cmd := "xkeen -stop"
	if on {
		cmd = "xkeen -start"
	}
	if out, err := r.Run(cmd); err != nil {
		return fmt.Errorf("%s: %w (%s)", cmd, err, strings.TrimSpace(out))
	}
	return nil
}

func (Keenetic) ProxyRunning(r Runner) (bool, error) {
	return yesNo(r, "pidof xray >/dev/null && echo yes || echo no")
}

func (Keenetic) Reboot(r Runner) error {
	_, err := r.Run("(sleep 2; ndmc -c 'system reboot' || reboot) >/dev/null 2>&1 &")
	return err
}

func keeneticPorts(routing string) ([]string, error) {
	m := keeneticUDPPortRe.FindStringSubmatch(routing)
	if m == nil {
		return nil, fmt.Errorf("%w: не нашёл правило vless-reality/udp в %s",
			ErrUnknownFormat, keeneticRoutingPath)
	}
	return splitAndTrim(m[2]), nil
}

func keeneticSetPort(routing, value string, enable bool) (string, error) {
	loc := keeneticUDPPortRe.FindStringSubmatchIndex(routing)
	if loc == nil {
		return "", fmt.Errorf("%w: не нашёл правило vless-reality/udp в %s",
			ErrUnknownFormat, keeneticRoutingPath)
	}
	prefix := routing[loc[2]:loc[3]]
	portsRaw := routing[loc[4]:loc[5]]
	suffix := routing[loc[6]:loc[7]]

	ports := toggleInList(splitAndTrim(portsRaw), value, enable)
	replacement := prefix + strings.Join(ports, ",") + suffix
	return routing[:loc[0]] + replacement + routing[loc[1]:], nil
}
