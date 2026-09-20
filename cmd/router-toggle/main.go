// консольный клиент
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
	reset := flag.Bool("reset", false, "забыть сохранённый код и выйти")
	flag.Parse()

	if *reset {
		if err := resetConfig(); err != nil {
			fail(err)
		}
		fmt.Println("Сохранённый код удалён.")
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

	c, err := authorize(&cfg)
	if err != nil {
		fail(err)
	}

	routers, err := c.Routers()
	if err == nil {
		adminLoop(c, routers)
		return
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && (apiErr.Code == api.ErrBadCode) {
		routerLoop(c, 0, true)
		return
	}
	fail(err)
}

func authorize(cfg *Config) (*client.Client, error) {
	for {
		if cfg.Code == "" {
			var input string
			if err := ask("Введите код доступа:", &input); err != nil {
				return nil, err
			}
			cfg.Code = strings.TrimSpace(input)
		}

		c := client.New(cfg.APIURL, cfg.Code)
		_, err := c.Status(0)
		if err == nil {
			return c, saveConfig(*cfg)
		}

		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Code {
			case api.ErrBadCode:
				// проверка списком
				if _, rerr := c.Routers(); rerr == nil {
					return c, saveConfig(*cfg)
				}
				fmt.Println(apiErr.Message)
				cfg.Code = ""
				continue
			default:
				return c, saveConfig(*cfg)
			}
		}
		return nil, err
	}
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
