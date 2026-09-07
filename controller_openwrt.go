package main

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

type OpenWrtController struct{}

var openwrtUDPLineRe = regexp.MustCompile(
	`(?s)(nft 'add rule ip xray prerouting ip saddr \S+ udp dport \{)([^}]*)(\} tproxy to :1083 meta mark set 1')`,
)

func (OpenWrtController) ToggleUDPProxy(client *ssh.Client, enable bool) error {
	current, err := Exec(client, "cat /etc/rc.local")
	if err != nil {
		return fmt.Errorf("не смог прочитать rc.local: %w", err)
	}

	updated, err := toggleDportRange(current, UDPRange, enable)
	if err != nil {
		return err
	}

	if err := writeRemoteFile(client, "/etc/rc.local", updated); err != nil {
		return fmt.Errorf("не смог записать rc.local: %w", err)
	}

	if _, err := Exec(client, "nft flush table xray; sh /etc/rc.local"); err != nil {
		return fmt.Errorf("не смог применить nft flush + rc.local: %w", err)
	}
	return nil
}

func toggleDportRange(content, portRange string, enable bool) (string, error) {
	loc := openwrtUDPLineRe.FindStringSubmatchIndex(content)
	if loc == nil {
		return "", fmt.Errorf("не нашёл строку с udp dport в rc.local — формат файла отличается от ожидаемого")
	}

	prefix := content[loc[2]:loc[3]]
	portsRaw := content[loc[4]:loc[5]]
	suffix := content[loc[6]:loc[7]]

	ports := splitAndTrim(portsRaw)
	ports = toggleInList(ports, portRange, enable)

	newLine := prefix + " " + strings.Join(ports, ", ") + " " + suffix
	return content[:loc[0]] + newLine + content[loc[1]:], nil
}
