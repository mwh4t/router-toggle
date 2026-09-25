package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	KindCategory = "category"
	KindDomain   = "domain"
	KindRaw      = "raw"
)

type DomainEntry struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// категория в список доменов
type Expander func(category string) []string

type DomainController interface {
	ListDomains(r Runner) ([]DomainEntry, error)
	HasElsewhere(r Runner, e DomainEntry) (bool, error)
	PlanDomains(r Runner, entries []DomainEntry, expand Expander) (*Plan, error)
	RestartDNS(r Runner) error
}

var ErrNotSupported = errors.New("прошивка не поддерживает домены")

var (
	domainRe   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+([a-z]{2,63}|xn--[a-z0-9-]{1,59})$`)
	categoryRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

func ValidDomain(s string) bool   { return len(s) <= 253 && domainRe.MatchString(s) }
func ValidCategory(s string) bool { return categoryRe.MatchString(s) }

func AsDomains(c Controller) (DomainController, error) {
	dc, ok := c.(DomainController)
	if !ok {
		return nil, ErrNotSupported
	}
	return dc, nil
}

// весь список целиком
func ApplyDomains(r Runner, c DomainController, entries []DomainEntry, expand Expander) error {
	for _, e := range entries {
		if e.Kind == KindDomain && !ValidDomain(e.Name) {
			return fmt.Errorf("%w: недопустимый домен %q", ErrUnknownFormat, e.Name)
		}
		if e.Kind == KindCategory && !ValidCategory(e.Name) {
			return fmt.Errorf("%w: недопустимая категория %q", ErrUnknownFormat, e.Name)
		}
	}

	plan, err := c.PlanDomains(r, entries, expand)
	if err != nil {
		return err
	}
	if plan.Empty() {
		return nil
	}
	verify := func() error {
		got, err := c.ListDomains(r)
		if err != nil {
			return err
		}
		if !sameEntries(got, entries) {
			return fmt.Errorf("после применения список доменов не совпал")
		}
		return nil
	}
	return applyPlan(r, plan, c.RestartDNS, verify)
}

func sameEntries(a, b []DomainEntry) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(s []DomainEntry) []string {
		out := make([]string, len(s))
		for i, e := range s {
			out[i] = e.Kind + ":" + e.Name
		}
		sort.Strings(out)
		return out
	}
	ka, kb := key(a), key(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// отдельное правило с ruleTag

const (
	userRuleTag           = "added-by-user"
	keeneticGeositePrefix = "ext:geosite_v2fly.dat:"
)

type ruleSpan struct {
	start, end int
	rule       struct {
		RuleTag     string   `json:"ruleTag"`
		OutboundTag string   `json:"outboundTag"`
		Domain      []string `json:"domain"`
	}
}

// границы элементов routing.rules в тексте
func routingRules(text string) ([]ruleSpan, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if key != "routing" {
			if err := skipValue(dec); err != nil {
				return nil, err
			}
			continue
		}
		if err := expectDelim(dec, '{'); err != nil {
			return nil, err
		}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if k != "rules" {
				if err := skipValue(dec); err != nil {
					return nil, err
				}
				continue
			}
			if err := expectDelim(dec, '['); err != nil {
				return nil, err
			}
			var spans []ruleSpan
			for dec.More() {
				off := int(dec.InputOffset())
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return nil, err
				}
				end := int(dec.InputOffset())
				start := off + strings.IndexByte(text[off:end], '{')
				var s ruleSpan
				s.start, s.end = start, end
				_ = json.Unmarshal(raw, &s.rule)
				spans = append(spans, s)
			}
			return spans, nil
		}
	}
	return nil, fmt.Errorf("%w: нет routing.rules", ErrUnknownFormat)
}

func expectDelim(dec *json.Decoder, d json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != d {
		return fmt.Errorf("%w: ожидался %q", ErrUnknownFormat, d)
	}
	return nil
}

func skipValue(dec *json.Decoder) error {
	var raw json.RawMessage
	return dec.Decode(&raw)
}

func keeneticEntry(s string) DomainEntry {
	switch {
	case strings.HasPrefix(s, keeneticGeositePrefix):
		return DomainEntry{Kind: KindCategory, Name: strings.TrimPrefix(s, keeneticGeositePrefix)}
	case strings.HasPrefix(s, "domain:"):
		return DomainEntry{Kind: KindDomain, Name: strings.TrimPrefix(s, "domain:")}
	}
	return DomainEntry{Kind: KindRaw, Name: s}
}

func keeneticString(e DomainEntry) string {
	switch e.Kind {
	case KindCategory:
		return keeneticGeositePrefix + e.Name
	case KindDomain:
		return "domain:" + e.Name
	}
	return e.Name
}

func keeneticDomains(text string) ([]DomainEntry, error) {
	spans, err := routingRules(text)
	if err != nil {
		return nil, err
	}
	for _, s := range spans {
		if s.rule.RuleTag == userRuleTag {
			out := make([]DomainEntry, 0, len(s.rule.Domain))
			for _, d := range s.rule.Domain {
				out = append(out, keeneticEntry(d))
			}
			return out, nil
		}
	}
	return nil, nil
}

func (Keenetic) ListDomains(r Runner) ([]DomainEntry, error) {
	text, err := readFile(r, keeneticRoutingPath)
	if err != nil {
		return nil, err
	}
	return keeneticDomains(text)
}

func (Keenetic) HasElsewhere(r Runner, e DomainEntry) (bool, error) {
	text, err := readFile(r, keeneticRoutingPath)
	if err != nil {
		return false, err
	}
	spans, err := routingRules(text)
	if err != nil {
		return false, err
	}
	for _, s := range spans {
		if s.rule.RuleTag == userRuleTag {
			text = text[:s.start] + text[s.end:]
			break
		}
	}
	return strings.Contains(text, `"`+keeneticString(e)+`"`), nil
}

func (Keenetic) PlanDomains(r Runner, entries []DomainEntry, _ Expander) (*Plan, error) {
	text, err := readFile(r, keeneticRoutingPath)
	if err != nil {
		return nil, err
	}
	spans, err := routingRules(text)
	if err != nil {
		return nil, err
	}

	main, user := -1, -1
	for i, s := range spans {
		switch {
		case s.rule.RuleTag == userRuleTag:
			user = i
		case main == -1 && s.rule.RuleTag == "" && s.rule.OutboundTag == "vless-reality" && len(s.rule.Domain) > 0:
			main = i
		}
	}
	if main == -1 {
		return nil, fmt.Errorf("%w: нет правила vless-reality с доменами", ErrUnknownFormat)
	}

	lineStart := strings.LastIndexByte(text[:spans[main].start], '\n') + 1
	indent := text[lineStart:spans[main].start]
	block := keeneticRuleText(indent, entries)

	var updated string
	switch {
	case user >= 0 && len(entries) > 0:
		updated = text[:spans[user].start] + block + text[spans[user].end:]
	case user >= 0:
		updated = text[:spans[user-1].end] + text[spans[user].end:]
	case len(entries) > 0:
		at := spans[main].end
		updated = text[:at] + ",\n" + indent + block + text[at:]
	default:
		updated = text
	}

	plan := &Plan{}
	if updated == text {
		return plan, nil
	}
	plan.Changes = append(plan.Changes, FileChange{
		Path:    keeneticRoutingPath,
		Before:  text,
		Content: updated,
		Validate: func(content string) error {
			if !json.Valid([]byte(content)) {
				return fmt.Errorf("результат не является корректным JSON")
			}
			got, err := keeneticDomains(content)
			if err != nil {
				return err
			}
			if !sameEntries(got, entries) {
				return fmt.Errorf("правило %s собрано неверно", userRuleTag)
			}
			return nil
		},
	})
	return plan, nil
}

func keeneticRuleText(indent string, entries []DomainEntry) string {
	quoted := make([]string, len(entries))
	for i, e := range entries {
		b, _ := json.Marshal(keeneticString(e))
		quoted[i] = indent + "    " + string(b)
	}
	in := indent + "  "
	return "{\n" +
		in + `"type": "field",` + "\n" +
		in + `"ruleTag": "` + userRuleTag + `",` + "\n" +
		in + `"inboundTag": ["redirect", "tproxy"],` + "\n" +
		in + `"outboundTag": "vless-reality",` + "\n" +
		in + `"domain": [` + "\n" +
		strings.Join(quoted, ",\n") + "\n" +
		in + "]\n" +
		indent + "}"
}

func (k Keenetic) RestartDNS(r Runner) error { return k.Restart(r) }

// секция в dnsmasq.servers

const (
	openwrtServersPath = "/etc/dnsmasq.servers"
	userSection        = "# added by user"
	defaultTarget      = "127.0.0.1#5353"
)

var serverLineRe = regexp.MustCompile(`^server=/([^/]+)/(\S+)$`)

func splitSection(text string) (head, section []string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == userSection {
			return lines[:i], lines[i+1:]
		}
	}
	return lines, nil
}

func openwrtDomains(text string) []DomainEntry {
	_, section := splitSection(text)
	var out []DomainEntry
	for _, l := range section {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "# ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(l, "# "))
		kind := KindCategory
		if strings.Contains(name, ".") {
			kind = KindDomain
		}
		out = append(out, DomainEntry{Kind: kind, Name: name})
	}
	return out
}

func serversTarget(head []string) string {
	count := map[string]int{}
	best, max := defaultTarget, 0
	for _, l := range head {
		m := serverLineRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		count[m[2]]++
		if count[m[2]] > max {
			best, max = m[2], count[m[2]]
		}
	}
	return best
}

func (OpenWrt) ListDomains(r Runner) ([]DomainEntry, error) {
	text, err := readFile(r, openwrtServersPath)
	if err != nil {
		return nil, err
	}
	return openwrtDomains(text), nil
}

func (OpenWrt) HasElsewhere(r Runner, e DomainEntry) (bool, error) {
	text, err := readFile(r, openwrtServersPath)
	if err != nil {
		return false, err
	}
	head, _ := splitSection(text)
	for _, l := range head {
		l = strings.TrimSpace(l)
		if e.Kind == KindCategory && l == "# "+e.Name {
			return true, nil
		}
		if e.Kind == KindDomain && strings.HasPrefix(l, "server=/"+e.Name+"/") {
			return true, nil
		}
	}
	return false, nil
}

func (OpenWrt) PlanDomains(r Runner, entries []DomainEntry, expand Expander) (*Plan, error) {
	text, err := readFile(r, openwrtServersPath)
	if err != nil {
		return nil, err
	}
	head, section := splitSection(text)
	if len(entries) == 0 && section == nil {
		return &Plan{}, nil
	}
	target := serversTarget(head)

	var b strings.Builder
	b.WriteString(strings.TrimRight(strings.Join(head, "\n"), "\n"))
	b.WriteString("\n")
	if len(entries) > 0 {
		b.WriteString("\n\n" + userSection + "\n")
		for _, e := range entries {
			var domains []string
			switch e.Kind {
			case KindDomain:
				domains = []string{e.Name}
			case KindCategory:
				if expand == nil {
					return nil, fmt.Errorf("%w: категории без сервера недоступны", ErrNotSupported)
				}
				domains = expand(e.Name)
			default:
				continue
			}
			b.WriteString("\n# " + e.Name + "\n")
			for _, d := range domains {
				b.WriteString("server=/" + d + "/" + target + "\n")
			}
		}
	}
	updated := b.String()

	plan := &Plan{}
	if updated == text {
		return plan, nil
	}
	plan.Changes = append(plan.Changes, FileChange{
		Path:    openwrtServersPath,
		Before:  text,
		Content: updated,
		Validate: func(content string) error {
			_, section := splitSection(content)
			for _, l := range section {
				l = strings.TrimSpace(l)
				if l == "" || strings.HasPrefix(l, "# ") {
					continue
				}
				m := serverLineRe.FindStringSubmatch(l)
				if m == nil || !ValidDomain(m[1]) {
					return fmt.Errorf("строка %q не прошла проверку", l)
				}
			}
			if !sameEntries(openwrtDomains(content), entries) {
				return fmt.Errorf("секция %q собрана неверно", userSection)
			}
			return nil
		},
	})
	return plan, nil
}

func (OpenWrt) RestartDNS(r Runner) error {
	if out, err := r.Run("/etc/init.d/dnsmasq restart"); err != nil {
		return fmt.Errorf("dnsmasq restart: %w (%s)", err, strings.TrimSpace(out))
	}
	up, err := yesNo(r, "sleep 1; pidof dnsmasq >/dev/null && echo yes || echo no")
	if err != nil {
		return err
	}
	if !up {
		return fmt.Errorf("dnsmasq не поднялся")
	}
	return nil
}
