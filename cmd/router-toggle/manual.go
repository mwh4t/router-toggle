package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2"

	"router-toggle/internal/router"
	"router-toggle/internal/sshconn"
)

type manualTarget struct {
	addr     string
	user     string
	firmware string
	authType string
	secret   string
}

func manualMode() {
	t, err := askTarget()
	if err != nil {
		return
	}

	ctrl, err := router.For(t.firmware)
	if err != nil {
		report(err)
		return
	}

	hostKey, err := trustHostKey(t.addr)
	if err != nil {
		report(err)
		return
	}

	client, err := sshconn.Dial(sshconn.Target{
		Addr: t.addr, User: t.user, AuthType: t.authType, Secret: t.secret, HostKey: hostKey,
	})
	if err != nil {
		report(err)
		return
	}
	defer client.Close()

	manualLoop(client, ctrl, t)
}

func askTarget() (manualTarget, error) {
	var t manualTarget
	var host, port string

	if err := survey.AskOne(&survey.Input{Message: "Адрес роутера:", Default: "192.168.1.1"}, &host); err != nil {
		return t, err
	}
	if err := selectOne("Прошивка:", []string{"openwrt", "keenetic"}, &t.firmware); err != nil {
		return t, err
	}
	defaultPort := "22"
	if t.firmware == "keenetic" {
		defaultPort = "222"
	}
	if err := survey.AskOne(&survey.Input{Message: "Порт SSH:", Default: defaultPort}, &port); err != nil {
		return t, err
	}
	if err := survey.AskOne(&survey.Input{Message: "Пользователь:", Default: "root"}, &t.user); err != nil {
		return t, err
	}

	var method string
	if err := selectOne("Вход:", []string{"пароль", "приватный ключ"}, &method); err != nil {
		return t, err
	}
	if method == "пароль" {
		t.authType = "password"
		if err := survey.AskOne(&survey.Password{Message: "Пароль:"}, &t.secret); err != nil {
			return t, err
		}
	} else {
		t.authType = "key"
		var path string
		if err := survey.AskOne(&survey.Input{Message: "Путь к ключу:", Default: "~/.ssh/id_ed25519"}, &path); err != nil {
			return t, err
		}
		data, err := os.ReadFile(expandHome(strings.TrimSpace(path)))
		if err != nil {
			return t, err
		}
		t.secret = string(data)
	}

	t.addr = strings.TrimSpace(host) + ":" + strings.TrimSpace(port)
	return t, nil
}

func trustHostKey(addr string) (string, error) {
	known, err := loadKnownHosts()
	if err != nil {
		return "", err
	}
	if key, ok := known[addr]; ok {
		return key, nil
	}

	key, fingerprint, err := sshconn.ScanHostKey(addr)
	if err != nil {
		return "", err
	}
	fmt.Printf("\nОтпечаток ключа %s:\n  %s\n\n", addr, fingerprint)
	if !confirm("Раньше вы к нему не подключались. Продолжить?") {
		return "", fmt.Errorf("подключение отменено")
	}

	known[addr] = key
	return key, saveKnownHosts(known)
}

func knownHostsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "router-toggle", "known_hosts.json"), nil
}

func loadKnownHosts() (map[string]string, error) {
	out := map[string]string{}
	path, err := knownHostsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(data, &out)
}

func saveKnownHosts(known map[string]string) error {
	path, err := knownHostsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(known, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

func manualLoop(client *sshconn.Client, ctrl router.Controller, t manualTarget) {
	for {
		udp, err := ctrl.ReadState(client, router.OpUDPProxy)
		if err != nil {
			report(err)
			return
		}
		vpn, err := ctrl.VPNState(client)
		if err != nil {
			report(err)
			return
		}

		fmt.Printf("\n%s (%s)\n", t.addr, t.firmware)
		fmt.Printf("  %s %s — %s\n", dot(udp), opTitle, onOff(udp))
		fmt.Printf("  %s VPN — %s\n", dot(vpn), vpnText(vpn))

		const (
			optCheck  = "Проверка"
			optReboot = "Перезагрузить роутер"
			optExit   = "Выход"
		)
		optUDP := "Включить игровые порты"
		if udp {
			optUDP = "Выключить игровые порты"
		}
		optVPN := "Выключить VPN до перезагрузки"
		if !vpn {
			optVPN = "Включить VPN"
		}

		var choice string
		if err := selectOne("", []string{optUDP, optVPN, optCheck, optReboot, optExit}, &choice); err != nil || choice == optExit {
			return
		}

		switch choice {
		case optCheck:
			manualCheck(client, ctrl)
		case optReboot:
			if confirm("Перезагрузить роутер? Интернет пропадёт на 1-2 минуты.") {
				if err := ctrl.Reboot(client); err != nil {
					report(err)
					return
				}
				fmt.Println("Роутер перезагружается.")
				return
			}
		case optVPN:
			if _, err := router.ApplyVPN(client, ctrl, !vpn); err != nil {
				report(err)
				continue
			}
			fmt.Println("Готово.")
		case optUDP:
			if !showDiff(client, ctrl, !udp) {
				continue
			}
			if _, err := router.Apply(client, ctrl, router.OpUDPProxy, !udp); err != nil {
				report(err)
				continue
			}
			fmt.Println("Готово.")
		}
	}
}

func showDiff(client *sshconn.Client, ctrl router.Controller, value bool) bool {
	plan, err := ctrl.Plan(client, router.OpUDPProxy, value)
	if err != nil {
		report(err)
		return false
	}
	if plan.Empty() {
		fmt.Println("Менять нечего.")
		return false
	}

	fmt.Println()
	for _, d := range router.Diff(plan) {
		fmt.Printf("%s\n", d.Path)
		for _, line := range d.Removed {
			fmt.Printf("  - %s\n", strings.TrimSpace(line))
		}
		for _, line := range d.Added {
			fmt.Printf("  + %s\n", strings.TrimSpace(line))
		}
	}
	fmt.Println()
	return confirm("Применить?")
}

func manualCheck(client *sshconn.Client, ctrl router.Controller) {
	fmt.Println("Проверяю...")
	h, err := router.CheckHealth(client, ctrl, "")
	if err != nil {
		report(err)
		return
	}

	fmt.Println()
	fmt.Printf("  %s Интернет\n", mark(h.Internet))
	if !h.VPNOn {
		fmt.Println("  — Служба прокси — vpn выключен до перезагрузки")
		return
	}
	fmt.Printf("  %s Служба прокси\n", mark(h.Proxy))
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
