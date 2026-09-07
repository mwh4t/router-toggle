package main

import (
	"fmt"
	"os"

	"github.com/AlecAivazis/survey/v2"
	"github.com/joho/godotenv"
)

const (
	firebaseProjectID = "router-toggle"

	actionEnable  = "Включить проксирование UDP 27000-27100"
	actionDisable = "Выключить проксирование UDP 27000-27100"
	manualEntry   = "Ввести данные вручную"
)

func main() {
	godotenv.Load()

	var presets []Preset
	if firebaseProjectID != "" {
		firebaseAPIKey := os.Getenv("FIREBASE_API_KEY")
		if firebaseAPIKey == "" {
			fmt.Println("⚠️  Переменная окружения FIREBASE_API_KEY не установлена.")
			fmt.Println("Продолжаю — можно ввести данные вручную.")
		} else {
			idToken, err := GetAnonymousIDToken(firebaseAPIKey)
			if err != nil {
				fmt.Println("Не удалось авторизоваться в Firebase:", err)
			} else {
				presets, err = FetchPresets(firebaseProjectID, idToken)
				if err != nil {
					fmt.Println("Не удалось загрузить пресеты из Firebase:", err)
					fmt.Println("Продолжаю — можно ввести данные вручную.")
				}
			}
		}
	}

	options := make([]string, 0, len(presets)+1)
	for _, p := range presets {
		options = append(options, p.Name)
	}
	options = append(options, manualEntry)

	var choice string
	if err := survey.AskOne(&survey.Select{
		Message: "Выберите роутер:",
		Options: options,
	}, &choice); err != nil {
		os.Exit(1)
	}

	var preset Preset
	if choice == manualEntry {
		preset = askManualPreset()
	} else {
		for _, p := range presets {
			if p.Name == choice {
				preset = p
				break
			}
		}
	}

	fmt.Println("Подключаюсь...")
	client, err := Connect(preset)
	if err != nil {
		fmt.Println("Ошибка подключения:", err)
		os.Exit(1)
	}
	defer client.Close()

	controller, err := ControllerFor(preset.Firmware)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	var action string
	if err := survey.AskOne(&survey.Select{
		Message: "Что сделать?",
		Options: []string{actionEnable, actionDisable},
	}, &action); err != nil {
		os.Exit(1)
	}

	if err := controller.ToggleUDPProxy(client, action == actionEnable); err != nil {
		fmt.Println("Ошибка:", err)
		os.Exit(1)
	}
	fmt.Println("Готово.")
}

func askManualPreset() Preset {
	var p Preset
	survey.AskOne(&survey.Input{Message: "Локальный IP роутера (например 192.168.1.1):"}, &p.LocalIP)
	p.LocalPort = 22

	var tunnelPortStr string
	survey.AskOne(&survey.Input{
		Message: "Порт этого роутера на VPS для подключения издалека (Enter — пропустить, если сейчас не нужно):",
	}, &tunnelPortStr)
	fmt.Sscanf(tunnelPortStr, "%d", &p.TunnelPort)

	survey.AskOne(&survey.Input{Message: "SSH-пользователь на роутере:"}, &p.SSHUser)
	survey.AskOne(&survey.Select{
		Message: "Прошивка роутера:",
		Options: []string{"openwrt", "keenetic"},
	}, &p.Firmware)
	survey.AskOne(&survey.Password{Message: "Пароль SSH роутера:"}, &p.AuthSecret)
	p.AuthType = "password"
	return p
}
