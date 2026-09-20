package router

import (
	"fmt"
	"regexp"
	"strings"
)

// правило nft в rc.local и применяется его выполнением
type OpenWrt struct{}

const (
	openwrtRCLocalPath = "/etc/rc.local"
	openwrtUDPRange    = "27000-27100"
)

var openwrtUDPLineRe = regexp.MustCompile(
	`(?s)(nft 'add rule ip xray prerouting ip saddr \S+ udp dport \{)([^}]*)(\} tproxy to :1083 meta mark set 1')`,
)

func (OpenWrt) Firmware() string   { return FirmwareOpenWrt }
func (OpenWrt) SupportedOps() []Op { return []Op{OpUDPProxy} }

func (OpenWrt) ReadState(r Runner, op Op) (bool, error) {
	if op != OpUDPProxy {
		return false, ErrUnsupportedOp
	}
	content, err := readFile(r, openwrtRCLocalPath)
	if err != nil {
		return false, err
	}
	ports, err := openwrtPorts(content)
	if err != nil {
		return false, err
	}
	return contains(ports, openwrtUDPRange), nil
}

func (OpenWrt) Plan(r Runner, op Op, value bool) (*Plan, error) {
	if op != OpUDPProxy {
		return nil, ErrUnsupportedOp
	}
	content, err := readFile(r, openwrtRCLocalPath)
	if err != nil {
		return nil, err
	}
	updated, err := openwrtSetPort(content, openwrtUDPRange, value)
	if err != nil {
		return nil, err
	}

	plan := &Plan{Op: op, Value: value}
	if updated == content {
		return plan, nil
	}

	plan.Changes = append(plan.Changes, FileChange{
		Path:    openwrtRCLocalPath,
		Before:  content,
		Content: updated,
		Validate: func(c string) error {
			ports, err := openwrtPorts(c)
			if err != nil {
				return err
			}
			if contains(ports, openwrtUDPRange) != value {
				return fmt.Errorf("диапазон %s не в ожидаемом состоянии", openwrtUDPRange)
			}
			return nil
		},
		// rc.local исполняется
		RemoteCheck: "sh -n %s",
	})
	return plan, nil
}

func (OpenWrt) Restart(r Runner) error {
	out, err := r.Run("nft flush table xray; sh " + shq(openwrtRCLocalPath))
	if err != nil {
		return fmt.Errorf("не смог применить nft flush + rc.local: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

func openwrtPorts(content string) ([]string, error) {
	m := openwrtUDPLineRe.FindStringSubmatch(content)
	if m == nil {
		return nil, fmt.Errorf("%w: не нашёл правило udp dport в %s",
			ErrUnknownFormat, openwrtRCLocalPath)
	}
	return splitAndTrim(m[2]), nil
}

func openwrtSetPort(content, value string, enable bool) (string, error) {
	loc := openwrtUDPLineRe.FindStringSubmatchIndex(content)
	if loc == nil {
		return "", fmt.Errorf("%w: не нашёл правило udp dport в %s",
			ErrUnknownFormat, openwrtRCLocalPath)
	}
	prefix := content[loc[2]:loc[3]]
	portsRaw := content[loc[4]:loc[5]]
	suffix := content[loc[6]:loc[7]]

	ports := toggleInList(splitAndTrim(portsRaw), value, enable)
	// пустой список в nft
	if len(ports) == 0 {
		return "", fmt.Errorf("%w: после изменения список dport оказался пустым", ErrUnknownFormat)
	}

	replacement := prefix + " " + strings.Join(ports, ", ") + " " + suffix
	return content[:loc[0]] + replacement + content[loc[1]:], nil
}
