package router

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// эталонные файлы маршрутизации
const (
	TemplateServers = "dnsmasq.servers" // openwrt
	TemplateRouting = "05_routing.json" // keenetic
)

// строка с несколькими доменами через /
var templateServerRe = regexp.MustCompile(`^server=/[^/\s]+(/[^/\s]+)*/\S+$`)

type TemplateController interface {
	DomainController
	PlanTemplate(r Runner, template string, expand Expander) (*Plan, error)
}

func AsTemplate(c Controller) (TemplateController, error) {
	tc, ok := c.(TemplateController)
	if !ok {
		return nil, ErrNotSupported
	}
	return tc, nil
}

// имя эталона для прошивки
func TemplateFor(firmware string) string {
	switch firmware {
	case FirmwareOpenWrt:
		return TemplateServers
	case FirmwareKeenetic:
		return TemplateRouting
	}
	return ""
}

// проверка эталона перед сохранением
func ValidateTemplate(name, content string) error {
	switch name {
	case TemplateRouting:
		if !json.Valid([]byte(content)) {
			return fmt.Errorf("%w: не JSON", ErrUnknownFormat)
		}
		if _, err := keeneticWithUser(content, nil); err != nil {
			return err
		}
		if _, err := keeneticPorts(content); err != nil {
			return err
		}
		return nil
	case TemplateServers:
		head, _ := splitSection(content)
		servers := 0
		for _, l := range head {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if !templateServerRe.MatchString(l) {
				return fmt.Errorf("%w: строка %q", ErrUnknownFormat, l)
			}
			servers++
		}
		if servers == 0 {
			return fmt.Errorf("%w: нет ни одной строки server=", ErrUnknownFormat)
		}
		return nil
	}
	return fmt.Errorf("%w: неизвестный эталон %q", ErrUnknownFormat, name)
}

// игровые порты и правило пользователя остаются как были
func (Keenetic) PlanTemplate(r Runner, template string, _ Expander) (*Plan, error) {
	text, err := readFile(r, keeneticRoutingPath)
	if err != nil {
		return nil, err
	}
	entries, err := keeneticDomains(text)
	if err != nil {
		return nil, err
	}
	ports, err := keeneticPorts(text)
	if err != nil {
		return nil, err
	}
	games := contains(ports, keeneticUDPRangeDash)

	updated, err := keeneticSetPort(template, keeneticUDPRangeDash, games)
	if err != nil {
		return nil, err
	}
	if updated, err = keeneticWithUser(updated, entries); err != nil {
		return nil, err
	}

	plan := &Plan{}
	if sameText(updated, text) {
		return plan, nil
	}
	plan.Changes = append(plan.Changes, FileChange{
		Path:    keeneticRoutingPath,
		Before:  text,
		Content: updated,
		Validate: func(content string) error {
			if err := keeneticCheckUser(content, entries); err != nil {
				return err
			}
			ports, err := keeneticPorts(content)
			if err != nil {
				return err
			}
			if contains(ports, keeneticUDPRangeDash) != games {
				return fmt.Errorf("диапазон %s не в прежнем состоянии", keeneticUDPRangeDash)
			}
			return nil
		},
	})
	return plan, nil
}

// секция пользователя остаётся как была
func (OpenWrt) PlanTemplate(r Runner, template string, expand Expander) (*Plan, error) {
	text, err := readFile(r, openwrtServersPath)
	if err != nil {
		return nil, err
	}
	entries := openwrtDomains(text)
	head, _ := splitSection(template)
	updated, err := openwrtWithUser(head, entries, expand)
	if err != nil {
		return nil, err
	}

	plan := &Plan{}
	if sameText(updated, text) {
		return plan, nil
	}
	plan.Changes = append(plan.Changes, FileChange{
		Path:    openwrtServersPath,
		Before:  text,
		Content: updated,
		Validate: func(content string) error {
			return openwrtCheckUser(content, entries)
		},
	})
	return plan, nil
}

type TemplateResult struct {
	Changed bool
	Added   int
	Removed int
}

func ApplyTemplate(r Runner, c TemplateController, template string, expand Expander, dry bool) (TemplateResult, error) {
	var res TemplateResult
	plan, err := c.PlanTemplate(r, template, expand)
	if err != nil {
		return res, err
	}
	if plan.Empty() {
		return res, nil
	}
	ch := plan.Changes[0]
	res.Changed = true
	res.Added = len(lineDiff(ch.Content, ch.Before))
	res.Removed = len(lineDiff(ch.Before, ch.Content))
	if dry {
		return res, nil
	}

	verify := func() error {
		got, err := readFile(r, ch.Path)
		if err != nil {
			return err
		}
		if !sameText(got, ch.Content) {
			return fmt.Errorf("после применения %s не совпал с ожидаемым", ch.Path)
		}
		return nil
	}
	return res, applyPlan(r, plan, c.RestartDNS, verify)
}

// writeFile дописывает перевод строки
func sameText(a, b string) bool {
	return strings.TrimRight(a, "\n") == strings.TrimRight(b, "\n")
}
