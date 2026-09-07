package main

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

type KeeneticController struct{}

const (
	portProxyingPath = "/opt/etc/xkeen/port_proxying.lst"
	routingJSONPath  = "/opt/etc/xray/configs/05_routing.json"

	udpRangeColon = "27000:27100" // формат port_proxying.lst
	udpRangeDash  = "27000-27100" // формат 05_routing.json
)

var keeneticUDPPortRe = regexp.MustCompile(
	`(?s)("outboundTag":\s*"vless-reality",\s*"network":\s*"udp",\s*"port":\s*")([^"]*)(")`,
)

func (KeeneticController) ToggleUDPProxy(client *ssh.Client, enable bool) error {
	lst, err := Exec(client, "cat "+portProxyingPath)
	if err != nil {
		return fmt.Errorf("не смог прочитать %s: %w", portProxyingPath, err)
	}
	newLst := toggleLineList(lst, udpRangeColon, enable)
	if err := writeRemoteFile(client, portProxyingPath, newLst); err != nil {
		return fmt.Errorf("не смог записать %s: %w", portProxyingPath, err)
	}

	routingJSON, err := Exec(client, "cat "+routingJSONPath)
	if err != nil {
		return fmt.Errorf("не смог прочитать %s: %w", routingJSONPath, err)
	}
	newRouting, err := toggleJSONPortList(routingJSON, udpRangeDash, enable)
	if err != nil {
		return err
	}
	if err := writeRemoteFile(client, routingJSONPath, newRouting); err != nil {
		return fmt.Errorf("не смог записать %s: %w", routingJSONPath, err)
	}

	if _, err := Exec(client, "xkeen -restart"); err != nil {
		return fmt.Errorf("xkeen -restart завершился с ошибкой: %w", err)
	}
	return nil
}

func toggleJSONPortList(content, value string, enable bool) (string, error) {
	loc := keeneticUDPPortRe.FindStringSubmatchIndex(content)
	if loc == nil {
		return "", fmt.Errorf("не нашёл правило vless-reality/udp в 05_routing.json — формат файла отличается от ожидаемого")
	}
	prefix := content[loc[2]:loc[3]]
	portsRaw := content[loc[4]:loc[5]]
	suffix := content[loc[6]:loc[7]]

	ports := splitAndTrim(portsRaw)
	ports = toggleInList(ports, value, enable)

	newValue := prefix + strings.Join(ports, ",") + suffix
	return content[:loc[0]] + newValue + content[loc[1]:], nil
}
