// контроллеры прошивок и протокол применения
package router

import (
	"errors"
	"fmt"
)

// абстракция соединения
type Runner interface {
	Run(cmd string) (string, error)
}

type Op string

const OpUDPProxy Op = "udp_proxy"

const (
	FirmwareOpenWrt  = "openwrt"
	FirmwareKeenetic = "keenetic"
)

var (
	ErrUnknownFormat = errors.New("формат конфигурации роутера не распознан") // e-11: ничего не изменено

	ErrRolledBack = errors.New("изменение не применилось, выполнен откат") // e-12: вернули как было

	ErrRollbackFailed = errors.New("откат не удался, роутер в неопределённом состоянии") // e-13: откат не удался

	ErrUnsupportedOp = errors.New("операция не поддерживается")

	ErrInconsistent = errors.New("конфигурация роутера противоречива") // конфиги роутера противоречат друг другу
)

// изменение одного файла
type FileChange struct {
	Path    string
	Before  string
	Content string

	Validate func(content string) error

	RemoteCheck string
}

type Plan struct {
	Op      Op
	Value   bool
	Changes []FileChange
}

func (p *Plan) Empty() bool { return p == nil || len(p.Changes) == 0 }

type Controller interface {
	Firmware() string
	SupportedOps() []Op
	ReadState(r Runner, op Op) (bool, error)
	Plan(r Runner, op Op, value bool) (*Plan, error)
	Restart(r Runner) error
}

func For(firmware string) (Controller, error) {
	switch firmware {
	case FirmwareOpenWrt:
		return OpenWrt{}, nil
	case FirmwareKeenetic:
		return Keenetic{}, nil
	default:
		return nil, fmt.Errorf("неизвестная прошивка: %q", firmware)
	}
}

func supports(c Controller, op Op) bool {
	for _, o := range c.SupportedOps() {
		if o == op {
			return true
		}
	}
	return false
}
