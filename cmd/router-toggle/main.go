package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"

	"router-toggle/internal/api"
	"router-toggle/internal/client"
)

const opTitle = "Игровые порты (Steam / FACEIT EU)"

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

	if *add {
		if err := addEntry(&cfg); err != nil {
			fail(err)
		}
	}
	if len(cfg.Entries) == 0 {
		if !firstRun(&cfg) {
			return
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
			entry := e
			routerLoop(c, 0, len(cfg.Entries) == 1, func(name string) {
				rename(&cfg, entry.Code, name)
			})
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

// первый запуск
func firstRun(cfg *Config) bool {
	const withCode = "У меня есть код доступа"
	const manual = "Ввести данные своего роутера"

	var choice string
	if err := selectOne("", []string{withCode, manual, "Выход"}, &choice); err != nil {
		return false
	}
	switch choice {
	case withCode:
		if err := addEntry(cfg); err != nil {
			report(err)
			return false
		}
		return true
	case manual:
		manualMode()
		return false
	}
	return false
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
	const manualOption = "Ввести данные роутера вручную"
	options := make([]string, 0, len(cfg.Entries)+3)
	for _, e := range cfg.Entries {
		options = append(options, e.Name)
	}
	options = append(options, addOption, manualOption, "Выход")

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
	if choice == manualOption {
		manualMode()
		return Entry{}, false
	}
	for i, o := range options {
		if o == choice && i < len(cfg.Entries) {
			return cfg.Entries[i], true
		}
	}
	return Entry{}, false
}

func adminLoop(c *client.Client, routers api.RoutersResponse) {
	const addOption = "Завести новый роутер"
	for {
		options := make([]string, 0, len(routers.Routers)+2)
		for _, r := range routers.Routers {
			options = append(options, fmt.Sprintf("%s (%s)", r.Name, r.Firmware))
		}
		options = append(options, addOption, "Выход")

		var choice string
		if err := selectOne("Роутер:", options, &choice); err != nil {
			return
		}
		if choice == "Выход" {
			return
		}
		if choice == addOption {
			if updated, err := addRouter(c); err != nil {
				report(err)
			} else {
				routers = updated
			}
			continue
		}
		for i, o := range options {
			if o == choice && i < len(routers.Routers) {
				routerLoop(c, routers.Routers[i].ID, false, nil)
			}
		}
	}
}

func routerLoop(c *client.Client, routerID int, showName bool, onName func(string)) {
	for {
		state, err := c.Status(routerID)
		if err != nil {
			report(err)
			if !retry() {
				return
			}
			continue
		}
		if onName != nil {
			onName(state.Router.Name)
		}

		udp, vpn := findOp(state, api.OpUDPProxy), findOp(state, api.OpVPN)
		fmt.Println()
		if showName {
			fmt.Printf("%s\n", state.Router.Name)
		}
		if routerID != 0 {
			shown := state.Router.DisplayName
			if shown == "" {
				shown = "не задано"
			}
			fmt.Printf("  для клиента: %s\n", shown)
		}
		if udp.Stale {
			fmt.Printf("  роутер не отвечает, данные %s\n", ago(udp.ReadAt))
		}
		fmt.Printf("  %s %s — %s\n", dot(udp.Value), opTitle, onOff(udp.Value))
		if vpn.Op != "" {
			fmt.Printf("  %s VPN — %s\n", dot(vpn.Value), vpnText(vpn.Value))
		}

		const (
			optDomains = "Сайты через VPN"
			optRename  = "Название для клиента"
			optCheck   = "Проверка"
			optReboot  = "Перезагрузить роутер"
			optReload  = "Обновить"
			optExit    = "Выход"
		)
		optUDP := "Включить игровые порты"
		if udp.Value {
			optUDP = "Выключить игровые порты"
		}
		optVPN := "Выключить VPN до перезагрузки"
		if !vpn.Value {
			optVPN = "Включить VPN"
		}

		options := []string{optUDP}
		if vpn.Op != "" {
			options = append(options, optVPN)
		}
		options = append(options, optDomains, optCheck, optReboot)
		if routerID != 0 {
			options = append(options, optRename)
		}
		options = append(options, optReload, optExit)

		var choice string
		if err := selectOne("", options, &choice); err != nil || choice == optExit {
			return
		}

		switch choice {
		case optReload:
			continue
		case optDomains:
			domainsLoop(c, routerID)
		case optRename:
			var name string
			prompt := &survey.Input{Message: "Название для клиента:", Default: state.Router.DisplayName}
			if err := survey.AskOne(prompt, &name); err != nil {
				continue
			}
			if _, err := c.RenameRouter(routerID, name); err != nil {
				report(err)
			}
		case optCheck:
			runCheck(c, routerID)
		case optReboot:
			if confirm("Перезагрузить роутер? Интернет пропадёт на 1-2 минуты.") {
				if err := c.Reboot(routerID); err != nil {
					report(err)
				} else {
					fmt.Println("Роутер перезагружается, проверьте через пару минут.")
				}
			}
		case optVPN:
			fmt.Println("Применяю...")
			res, err := c.Apply(routerID, api.OpVPN, !vpn.Value)
			if err != nil {
				report(err)
				continue
			}
			fmt.Printf("Готово, VPN %s\n", vpnText(findOp(res, api.OpVPN).Value))
		case optUDP:
			fmt.Println("Применяю...")
			res, err := c.Apply(routerID, api.OpUDPProxy, !udp.Value)
			if err != nil {
				report(err)
				continue
			}
			fmt.Printf("Готово, игровые порты %s\n", onOff(findOp(res, api.OpUDPProxy).Value))
		}
	}
}

func dot(on bool) string {
	if on {
		return "●"
	}
	return "○"
}

func domainsLoop(c *client.Client, routerID int) {
	const (
		optAdd  = "Добавить сайт или сервис"
		optBack = "Назад"
	)
	for {
		res, err := c.Domains(routerID)
		if err != nil {
			report(err)
			return
		}

		fmt.Println()
		if len(res.Entries) == 0 {
			fmt.Println("  Своих сайтов пока нет")
		}
		options := []string{optAdd}
		for _, e := range res.Entries {
			fmt.Printf("  • %s\n", entryTitle(e))
			options = append(options, "Удалить "+e.Name)
		}
		options = append(options, optBack)

		var choice string
		if err := selectOne("", options, &choice); err != nil || choice == optBack {
			return
		}
		if choice == optAdd {
			addDomain(c, routerID)
			continue
		}
		for _, e := range res.Entries {
			if choice == "Удалить "+e.Name && confirm("Убрать "+e.Name+" из VPN?") {
				fmt.Println("Применяю...")
				if _, err := c.RemoveDomain(routerID, e.Kind, e.Name); err != nil {
					report(err)
				}
			}
		}
	}
}

func entryTitle(e api.DomainEntry) string {
	if e.Kind == "category" {
		return e.Name + " (сервис)"
	}
	return e.Name
}

// сервис из v2fly, иначе один домен
func addDomain(c *client.Client, routerID int) {
	var query string
	if err := ask("Название сервиса или адрес сайта:", &query); err != nil {
		return
	}
	res, err := c.SearchDomains(routerID, query)
	if err != nil {
		report(err)
		return
	}

	type option struct{ kind, name string }
	var labels []string
	picks := map[string]option{}
	for _, m := range res.Matches {
		label := fmt.Sprintf("%s — сервис, доменов: %d", m.Name, m.Size)
		labels = append(labels, label)
		picks[label] = option{"category", m.Name}
	}
	if res.Domain != "" {
		label := "Только сайт " + res.Domain
		labels = append(labels, label)
		picks[label] = option{"domain", res.Domain}
	}
	if len(labels) == 0 {
		fmt.Println("Ничего не нашёл. Проверьте название или введите адрес сайта, например example.com")
		return
	}
	labels = append(labels, "Отмена")

	var choice string
	if err := selectOne("Что добавить?", labels, &choice); err != nil || choice == "Отмена" {
		return
	}
	p := picks[choice]
	fmt.Println("Применяю...")
	if _, err := c.AddDomain(routerID, p.kind, p.name); err != nil {
		report(err)
		return
	}
	fmt.Printf("Готово, %s идёт через VPN\n", p.name)
}

func runCheck(c *client.Client, routerID int) {
	fmt.Println("Проверяю...")
	res, err := c.Check(routerID)
	if err != nil {
		report(err)
		return
	}
	marks := map[string]string{"ok": "✓", "fail": "✗", "off": "—", "skip": "·"}
	fmt.Println()
	for _, ch := range res.Checks {
		line := fmt.Sprintf("  %s %s", marks[ch.State], ch.Name)
		if ch.Hint != "" {
			line += " — " + ch.Hint
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
	return "выключен до перезагрузки"
}

func confirm(message string) bool {
	var yes bool
	if err := survey.AskOne(&survey.Confirm{Message: message}, &yes); err != nil {
		return false
	}
	return yes
}

// заводит роутер и печатает код для клиента
func addRouter(c *client.Client) (api.RoutersResponse, error) {
	var req api.AddRouterRequest
	var port, user string

	if err := ask("Название роутера:", &req.Name); err != nil {
		return api.RoutersResponse{}, err
	}
	if err := survey.AskOne(&survey.Input{Message: "Название для клиента (Дом, Дача…):"}, &req.DisplayName); err != nil {
		return api.RoutersResponse{}, err
	}
	if err := selectOne("Прошивка:", []string{"keenetic", "openwrt"}, &req.Firmware); err != nil {
		return api.RoutersResponse{}, err
	}
	if err := ask("Порт туннеля на VPS:", &port); err != nil {
		return api.RoutersResponse{}, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil {
		return api.RoutersResponse{}, fmt.Errorf("порт должен быть числом")
	}
	req.TunnelPort = n

	if err := survey.AskOne(&survey.Input{Message: "Пользователь SSH:", Default: "root"}, &user); err != nil {
		return api.RoutersResponse{}, err
	}
	req.SSHUser = strings.TrimSpace(user)
	req.AuthType = "password"
	if err := survey.AskOne(&survey.Password{Message: "Пароль:"}, &req.AuthSecret); err != nil {
		return api.RoutersResponse{}, err
	}

	fmt.Println("Подключаюсь...")
	res, err := c.AddRouter(req)
	if err != nil {
		return api.RoutersResponse{}, err
	}
	fmt.Printf("\nГотово: %s (%s), id=%d\n", res.Router.Name, res.Router.Firmware, res.Router.ID)
	fmt.Printf("Код доступа для клиента: %s\n\n", res.AccessCode)

	return c.Routers()
}

func rename(cfg *Config, code, name string) {
	for i := range cfg.Entries {
		if cfg.Entries[i].Code == code && cfg.Entries[i].Name != name {
			cfg.Entries[i].Name = name
			_ = saveConfig(*cfg)
			return
		}
	}
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
