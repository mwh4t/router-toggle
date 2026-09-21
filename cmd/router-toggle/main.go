package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"

	"router-toggle/internal/api"
	"router-toggle/internal/client"
)

const opTitle = "Проксирование портов Steam / FACEIT EU"

var defaultAPI = "" // задаётся при сборке

func main() {
	apiURL := flag.String("api", "", "адрес сервера (переопределяет сохранённый)")
	add := flag.Bool("add", false, "добавить ещё один код")
	reset := flag.Bool("reset", false, "забыть все сохранённые коды и выйти")
	flag.Parse()

	if *reset {
		if err := resetConfig(); err != nil {
			fail(err)
		}
		fmt.Println("Сохранённые коды удалены.")
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fail(err)
	}
	if *apiURL != "" {
		cfg.APIURL = *apiURL
	}
	if cfg.APIURL == "" {
		cfg.APIURL = defaultAPI
	}
	if cfg.APIURL == "" {
		if err := ask("Адрес сервера:", &cfg.APIURL); err != nil {
			fail(err)
		}
	}
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")

	if err := resolveEntries(&cfg); err != nil {
		fail(err)
	}

	if *add || len(cfg.Entries) == 0 {
		if err := addEntry(&cfg); err != nil {
			fail(err)
		}
	}

	for {
		e, ok := pickEntry(&cfg)
		if !ok {
			return
		}
		c := client.New(cfg.APIURL, e.Code)
		if e.Admin {
			routers, err := c.Routers()
			if err != nil {
				report(err)
			} else {
				adminLoop(c, routers)
			}
		} else {
			routerLoop(c, 0, len(cfg.Entries) == 1)
		}
		if len(cfg.Entries) == 1 {
			return
		}
	}
}

func resolveEntries(cfg *Config) error {
	changed := false
	kept := cfg.Entries[:0]
	for _, e := range cfg.Entries {
		if e.Name != "" {
			kept = append(kept, e)
			continue
		}
		resolved, err := check(cfg.APIURL, e.Code)
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code == api.ErrBadCode {
			fmt.Println("Сохранённый код больше не действует.")
			changed = true
			continue
		}
		if err != nil {
			// сервер недоступен
			kept = append(kept, e)
			continue
		}
		kept = append(kept, resolved)
		changed = true
	}
	cfg.Entries = kept
	if changed {
		return saveConfig(*cfg)
	}
	return nil
}

func addEntry(cfg *Config) error {
	for {
		var input string
		if err := ask("Введите код доступа:", &input); err != nil {
			return err
		}
		code := strings.TrimSpace(input)
		if cfg.has(code) {
			fmt.Println("Этот код уже добавлен.")
			return nil
		}

		e, err := check(cfg.APIURL, code)
		if err != nil {
			var apiErr *client.APIError
			if errors.As(err, &apiErr) && apiErr.Code == api.ErrBadCode {
				fmt.Println(apiErr.Message)
				continue
			}
			return err
		}
		cfg.Entries = append(cfg.Entries, e)
		return saveConfig(*cfg)
	}
}

// какому роутеру принадлежит код
func check(apiURL, code string) (Entry, error) {
	c := client.New(apiURL, code)

	state, err := c.Status(0)
	if err == nil {
		return Entry{Code: code, Name: state.Router.Name}, nil
	}

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return Entry{}, err
	}
	if apiErr.Code == api.ErrBadCode {
		if _, rerr := c.Routers(); rerr == nil {
			return Entry{Code: code, Name: "Все роутеры", Admin: true}, nil
		}
		return Entry{}, err
	}
	return Entry{Code: code, Name: "Роутер"}, nil
}

// меню выбора
func pickEntry(cfg *Config) (Entry, bool) {
	if len(cfg.Entries) == 1 {
		return cfg.Entries[0], true
	}

	const addOption = "Добавить роутер"
	options := make([]string, 0, len(cfg.Entries)+2)
	for _, e := range cfg.Entries {
		options = append(options, e.Name)
	}
	options = append(options, addOption, "Выход")

	var choice string
	if err := selectOne("Роутер:", options, &choice); err != nil || choice == "Выход" {
		return Entry{}, false
	}
	if choice == addOption {
		if err := addEntry(cfg); err != nil {
			report(err)
		}
		return pickEntry(cfg)
	}
	for i, o := range options {
		if o == choice && i < len(cfg.Entries) {
			return cfg.Entries[i], true
		}
	}
	return Entry{}, false
}

func adminLoop(c *client.Client, routers api.RoutersResponse) {
	for {
		options := make([]string, 0, len(routers.Routers)+1)
		for _, r := range routers.Routers {
			options = append(options, fmt.Sprintf("%s (%s)", r.Name, r.Firmware))
		}
		options = append(options, "Выход")

		var choice string
		if err := selectOne("Роутер:", options, &choice); err != nil {
			return
		}
		if choice == "Выход" {
			return
		}
		for i, o := range options {
			if o == choice && i < len(routers.Routers) {
				routerLoop(c, routers.Routers[i].ID, false)
			}
		}
	}
}

func routerLoop(c *client.Client, routerID int, showName bool) {
	for {
		state, err := c.Status(routerID)
		if err != nil {
			report(err)
			if !retry() {
				return
			}
			continue
		}

		udp, vpn := findOp(state, api.OpUDPProxy), findOp(state, api.OpVPN)
		fmt.Println()
		if showName {
			fmt.Printf("Роутер: %s\n", state.Router.Name)
		}
		if udp.Stale {
			fmt.Printf("Роутер не отвечает. Последнее известное состояние (%s):\n", ago(udp.ReadAt))
		}
		fmt.Printf("%s: %s\n", opTitle, onOff(udp.Value))
		if vpn.Op != "" {
			fmt.Printf("VPN: %s\n", vpnText(vpn.Value))
		}

		const (
			optCheck  = "Проверить, всё ли в порядке"
			optReboot = "Перезагрузить роутер"
			optReload = "Обновить"
			optExit   = "Выход"
		)
		optUDP := "Включить проксирование портов"
		if udp.Value {
			optUDP = "Выключить проксирование портов"
		}
		optVPN := "Выключить VPN до перезагрузки роутера"
		if !vpn.Value {
			optVPN = "Включить VPN"
		}

		options := []string{optUDP}
		if vpn.Op != "" {
			options = append(options, optVPN)
		}
		options = append(options, optCheck, optReboot, optReload, optExit)

		var choice string
		if err := selectOne("", options, &choice); err != nil || choice == optExit {
			return
		}

		switch choice {
		case optReload:
			continue
		case optCheck:
			runCheck(c, routerID)
		case optReboot:
			if confirm("Перезагрузить роутер? Интернет пропадёт на 1-2 минуты.") {
				if err := c.Reboot(routerID); err != nil {
					report(err)
				} else {
					fmt.Println("Роутер перезагружается. Проверьте состояние через пару минут.")
				}
			}
		case optVPN:
			fmt.Println("Применяю...")
			res, err := c.Apply(routerID, api.OpVPN, !vpn.Value)
			if err != nil {
				report(err)
				continue
			}
			fmt.Printf("Готово. VPN: %s\n", vpnText(findOp(res, api.OpVPN).Value))
		case optUDP:
			fmt.Println("Применяю...")
			res, err := c.Apply(routerID, api.OpUDPProxy, !udp.Value)
			if err != nil {
				report(err)
				continue
			}
			fmt.Printf("Готово. %s: %s\n", opTitle, onOff(findOp(res, api.OpUDPProxy).Value))
		}
	}
}

func runCheck(c *client.Client, routerID int) {
	fmt.Println("Проверяю...")
	res, err := c.Check(routerID)
	if err != nil {
		report(err)
		return
	}
	fmt.Println()
	for _, ch := range res.Checks {
		mark := map[string]string{"ok": "✓", "fail": "✗", "off": "—", "skip": "·"}[ch.State]
		line := fmt.Sprintf("%-28s %s", ch.Name, mark)
		if ch.Hint != "" {
			line += "  " + ch.Hint
		}
		fmt.Println(line)
	}
}

func findOp(s api.StateResponse, op string) api.OpState {
	for _, o := range s.Ops {
		if o.Op == op {
			return o
		}
	}
	return api.OpState{}
}

func vpnText(on bool) string {
	if on {
		return "включён"
	}
	return "выключен до перезагрузки роутера"
}

func confirm(message string) bool {
	var yes bool
	if err := survey.AskOne(&survey.Confirm{Message: message}, &yes); err != nil {
		return false
	}
	return yes
}

func report(err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.Message)
		return
	}
	fmt.Println("Не удалось связаться с сервером.")
	fmt.Println("Возможно, ваш провайдер блокирует соединение — попробуйте позже.")
}

func retry() bool {
	var again bool
	prompt := &survey.Confirm{Message: "Повторить?", Default: true}
	if err := survey.AskOne(prompt, &again); err != nil {
		return false
	}
	return again
}

func onOff(v bool) string {
	if v {
		return "включено"
	}
	return "выключено"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "только что"
	case d < time.Hour:
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	default:
		return fmt.Sprintf("%d дн назад", int(d.Hours()/24))
	}
}

func ask(message string, dst *string) error {
	return survey.AskOne(&survey.Input{Message: message}, dst, survey.WithValidator(survey.Required))
}

func selectOne(message string, options []string, dst *string) error {
	return survey.AskOne(&survey.Select{Message: message, Options: options}, dst)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "Ошибка:", err)
	os.Exit(1)
}
