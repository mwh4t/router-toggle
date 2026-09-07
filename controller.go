package main

import (
	"fmt"

	"golang.org/x/crypto/ssh"
)

const UDPRange = "27000-27100"

type RouterController interface {
	ToggleUDPProxy(client *ssh.Client, enable bool) error
}

func ControllerFor(firmware string) (RouterController, error) {
	switch firmware {
	case "openwrt":
		return OpenWrtController{}, nil
	case "keenetic":
		return KeeneticController{}, nil
	default:
		return nil, fmt.Errorf("неизвестная прошивка: %q", firmware)
	}
}
