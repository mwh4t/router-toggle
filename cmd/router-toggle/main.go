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
const defaultAPI = "https://rt.mwh4t.lol"

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
		// один код
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

		op := state.Ops[0]
		fmt.Println()
		if showName {
			fmt.Printf("Роутер: %s\n", state.Router.Name)
		}
		if op.Stale {
			fmt.Printf("Роутер не отвечает. Последнее известное состояние: %s (%s)\n",
				onOff(op.Value), ago(op.ReadAt))
			fmt.Println("Проверьте, что роутер включён и подключён к интернету.")
		} else {
			fmt.Printf("%s: %s\n", opTitle, onOff(op.Value))
		}

		action := "Включить"
		if op.Value {
			action = "Выключить"
		}
		options := []string{action, "Обновить", "Выход"}

		var choice string
		if err := selectOne("", options, &choice); err != nil || choice == "Выход" {
			return
		}
		if choice == "Обновить" {
			continue
		}

		fmt.Println("Применяю...")
		res, err := c.Apply(routerID, api.OpUDPProxy, !op.Value)
		if err != nil {
			report(err)
			continue
		}
		fmt.Printf("Готово. %s: %s\n", opTitle, onOff(res.Ops[0].Value))
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
