package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"router-toggle/internal/nodes"
)

// ноды из подписки
type NodesController interface {
	NodeTags(r Runner) ([]string, error)
	PlanNodes(r Runner, set nodes.Set) (*Plan, error)
	RestartProxy(r Runner) error
	NodeProbe(r Runner, tag string) (bool, error)
	NodeSpeed(r Runner, tag string) (float64, error)
}

func AsNodes(c Controller) (NodesController, error) {
	nc, ok := c.(NodesController)
	if !ok {
		return nil, ErrUnsupportedOp
	}
	return nc, nil
}

type NodesResult struct {
	Changed bool
	Before  []string // пусто до первого перехода
	After   []string
}

func ApplyNodes(r Runner, c Controller, set nodes.Set, dry bool) (NodesResult, error) {
	var res NodesResult
	nc, err := AsNodes(c)
	if err != nil {
		return res, err
	}
	if len(set.Nodes) == 0 {
		return res, nodes.ErrNoNodes
	}
	if res.Before, err = nc.NodeTags(r); err != nil {
		return res, err
	}
	res.After = set.Tags()
	plan, err := nc.PlanNodes(r, set)
	if err != nil {
		return res, err
	}
	res.Changed = !plan.Empty()
	if plan.Empty() || dry {
		return res, nil
	}

	verify := func() error {
		for _, ch := range plan.Changes {
			got, err := readFile(r, ch.Path)
			if err != nil {
				return err
			}
			if !sameText(got, ch.Content) {
				return fmt.Errorf("после применения %s не совпал с ожидаемым", ch.Path)
			}
		}
		on, err := c.VPNState(r)
		if err != nil || !on {
			return err
		}
		if running, err := c.ProxyRunning(r); err != nil || !running {
			return fmt.Errorf("xray не запустился с новыми нодами")
		}
		for _, tag := range set.Tags() {
			if ok, err := nc.NodeProbe(r, tag); err == nil && ok {
				return nil
			}
		}
		return fmt.Errorf("ни одна нода не ответила через новый конфиг")
	}
	return res, applyPlan(r, plan, nc.RestartProxy, verify)
}

type NodeState struct {
	Tag string
	OK  bool
}

var nodeTagRe = regexp.MustCompile(`^` + nodes.TagPrefix + `[a-z0-9-]+$`)

func nodeTagsOf(tags []string) []string {
	var out []string
	for _, t := range tags {
		if strings.HasPrefix(t, nodes.TagPrefix) {
			out = append(out, t)
		}
	}
	return out
}

// формат по расширению
func xrayTest(env, ext string) string {
	tmp := "/tmp/rt-test." + ext
	return "cp %s " + tmp + " && " + env + "xray run -test -c " + tmp +
		" >/dev/null 2>&1; rc=$?; rm -f " + tmp + "; exit $rc"
}

const keeneticMainTag = "vless-reality"

var (
	keeneticMainRuleRe = regexp.MustCompile(`"outboundTag"(\s*:\s*)"` + keeneticMainTag + `"`)
	keeneticBalLineRe  = regexp.MustCompile(`(?m)^[ \t]*"balancers"[ \t]*:[^\n]*\n`)
	keeneticRoutingRe  = regexp.MustCompile(`(?m)^[ \t]*"routing"[ \t]*:[ \t]*\{[ \t]*\n`)
)

func (Keenetic) NodeTags(r Runner) ([]string, error) {
	text, err := readFile(r, keeneticOutboundsPath)
	if err != nil {
		return nil, err
	}
	tags, err := keeneticOutboundTags(text)
	return nodeTagsOf(tags), err
}

func keeneticOutboundTags(text string) ([]string, error) {
	var top struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		return nil, fmt.Errorf("%w: %s не JSON: %v", ErrUnknownFormat, keeneticOutboundsPath, err)
	}
	out := make([]string, 0, len(top.Outbounds))
	for _, o := range top.Outbounds {
		out = append(out, o.Tag)
	}
	return out, nil
}

func (k Keenetic) PlanNodes(r Runner, set nodes.Set) (*Plan, error) {
	outText, err := readFile(r, keeneticOutboundsPath)
	if err != nil {
		return nil, err
	}
	routing, err := readFile(r, keeneticRoutingPath)
	if err != nil {
		return nil, err
	}
	newOut, err := keeneticWithNodes(outText, set)
	if err != nil {
		return nil, err
	}
	balancers := nodes.Compact([]nodes.Balancer{set.Balancer(keeneticMainTag)})
	newRouting, err := keeneticUseBalancer(routing, balancers)
	if err != nil {
		return nil, err
	}
	users, err := keeneticDomains(routing)
	if err != nil {
		return nil, err
	}
	ports, err := keeneticPorts(routing)
	if err != nil {
		return nil, err
	}

	plan := &Plan{}
	if !sameText(newOut, outText) {
		plan.Changes = append(plan.Changes, FileChange{
			Path:    keeneticOutboundsPath,
			Before:  outText,
			Content: newOut,
			Validate: func(content string) error {
				tags, err := keeneticOutboundTags(content)
				if err != nil {
					return err
				}
				if strings.Join(nodeTagsOf(tags), ",") != strings.Join(set.Tags(), ",") {
					return fmt.Errorf("ноды собраны неверно")
				}
				return nil
			},
			RemoteCheck: xrayTest("", "json"),
		})
	}
	if !sameText(newRouting, routing) {
		plan.Changes = append(plan.Changes, FileChange{
			Path:    keeneticRoutingPath,
			Before:  routing,
			Content: newRouting,
			Validate: func(content string) error {
				if err := keeneticCheckBalancer(content, balancers); err != nil {
					return err
				}
				if err := keeneticCheckUser(content, users); err != nil {
					return err
				}
				got, err := keeneticPorts(content)
				if err != nil {
					return err
				}
				if strings.Join(got, ",") != strings.Join(ports, ",") {
					return fmt.Errorf("порты правила udp изменились")
				}
				return nil
			},
		})
	}
	return plan, nil
}

func keeneticWithNodes(text string, set nodes.Set) (string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		return "", fmt.Errorf("%w: %s не JSON: %v", ErrUnknownFormat, keeneticOutboundsPath, err)
	}
	var outs []json.RawMessage
	if err := json.Unmarshal(top["outbounds"], &outs); err != nil || len(outs) == 0 {
		return "", fmt.Errorf("%w: в %s нет outbounds", ErrUnknownFormat, keeneticOutboundsPath)
	}

	items := make([]json.RawMessage, 0, len(outs)+len(set.Nodes))
	for _, n := range set.Nodes {
		items = append(items, json.RawMessage(nodes.Compact(n.Outbound(0))))
	}
	main := false
	for _, raw := range outs {
		var o struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return "", fmt.Errorf("%w: %s: %v", ErrUnknownFormat, keeneticOutboundsPath, err)
		}
		if o.Tag == keeneticMainTag || strings.HasPrefix(o.Tag, nodes.TagPrefix) {
			main = true
			continue
		}
		items = append(items, raw)
	}
	if !main {
		return "", fmt.Errorf("%w: в %s нет выхода %s", ErrUnknownFormat, keeneticOutboundsPath, keeneticMainTag)
	}

	top["outbounds"] = json.RawMessage(nodes.Compact(items))
	top["burstObservatory"] = json.RawMessage(nodes.Compact(set.Observatory()))
	keys := []string{"outbounds", "burstObservatory"}
	var rest []string
	for k := range top {
		if k != "outbounds" && k != "burstObservatory" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)

	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range append(keys, rest...) {
		b.WriteString(`    "` + k + `": `)
		if err := json.Indent(&b, top[k], "    ", "    "); err != nil {
			return "", fmt.Errorf("%w: %s: %v", ErrUnknownFormat, keeneticOutboundsPath, err)
		}
		if i < len(keys)+len(rest)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func keeneticUseBalancer(text, balancers string) (string, error) {
	text = keeneticMainRuleRe.ReplaceAllString(text, `"balancerTag"${1}"`+keeneticMainTag+`"`)
	line := func(indent string) string { return indent + `"balancers": ` + balancers + ",\n" }

	if m := keeneticBalLineRe.FindStringIndex(text); m != nil {
		old := text[m[0]:m[1]]
		indent := old[:len(old)-len(strings.TrimLeft(old, " \t"))]
		return text[:m[0]] + line(indent) + text[m[1]:], nil
	}
	m := keeneticRoutingRe.FindStringIndex(text)
	if m == nil {
		return "", fmt.Errorf("%w: не нашёл \"routing\": { в %s", ErrUnknownFormat, keeneticRoutingPath)
	}
	next := text[m[1]:]
	if i := strings.IndexByte(next, '\n'); i >= 0 {
		next = next[:i]
	}
	indent := next[:len(next)-len(strings.TrimLeft(next, " \t"))]
	return text[:m[1]] + line(indent) + text[m[1]:], nil
}

func keeneticBalancers(text string) (string, bool) {
	var top struct {
		Routing struct {
			Balancers json.RawMessage `json:"balancers"`
		} `json:"routing"`
	}
	if json.Unmarshal([]byte(text), &top) != nil || len(top.Routing.Balancers) == 0 {
		return "", false
	}
	var b bytes.Buffer
	if json.Compact(&b, top.Routing.Balancers) != nil {
		return "", false
	}
	return b.String(), true
}

func keeneticCheckBalancer(content, balancers string) error {
	if !json.Valid([]byte(content)) {
		return fmt.Errorf("результат не является корректным JSON")
	}
	if got, ok := keeneticBalancers(content); !ok || got != balancers {
		return fmt.Errorf("балансировщик собран неверно")
	}
	if keeneticMainRuleRe.MatchString(content) {
		return fmt.Errorf("остались правила с outboundTag %s", keeneticMainTag)
	}
	return nil
}

func (k Keenetic) RestartProxy(r Runner) error { return k.Restart(r) }

func (Keenetic) NodeSpeed(r Runner, tag string) (float64, error) {
	if !nodeTagRe.MatchString(tag) {
		return 0, fmt.Errorf("недопустимый тег %q", tag)
	}
	return speedOf(r, speedScript("", []string{keeneticOutboundsPath},
		probeConfig([]string{probeInboundTo("rt-probe", speedHost)}, "rt-probe", tag)))
}

func (Keenetic) NodeProbe(r Runner, tag string) (bool, error) {
	if !nodeTagRe.MatchString(tag) {
		return false, fmt.Errorf("недопустимый тег %q", tag)
	}
	return yesNo(r, probeScript("", []string{keeneticOutboundsPath},
		probeConfig([]string{probeInbound("rt-probe")}, "rt-probe", tag)))
}

// openwrt построчно, ноды json-строками

const (
	openwrtMainTag = "proxy"
	openwrtMark    = 2
)

var openwrtMainRuleRe = regexp.MustCompile(`^([ \t]*(?:- )?)outboundTag:[ \t]*` + openwrtMainTag + `[ \t]*$`)

type yamlItem struct {
	lines []string
	tag   string
}

// ключ верхнего уровня
func yamlSection(lines []string, key string) (int, int, bool) {
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == key+":" {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	end := len(lines)
	for j := start + 1; j < len(lines); j++ {
		l := lines[j]
		if l == "" || l[0] == ' ' || l[0] == '\t' || l[0] == '-' || l[0] == '#' {
			continue
		}
		end = j
		break
	}
	return start, end, true
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

func yamlItems(body []string) ([]yamlItem, int, error) {
	var items []yamlItem
	indent := -1
	for _, l := range body {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			if len(items) > 0 {
				items[len(items)-1].lines = append(items[len(items)-1].lines, l)
			}
			continue
		}
		if indent < 0 {
			if !strings.HasPrefix(t, "- ") {
				return nil, 0, fmt.Errorf("outbounds не список")
			}
			indent = indentOf(l)
		}
		switch {
		case indentOf(l) == indent && strings.HasPrefix(t, "- "):
			items = append(items, yamlItem{lines: []string{l}})
		case indentOf(l) > indent && len(items) > 0:
			items[len(items)-1].lines = append(items[len(items)-1].lines, l)
		default:
			return nil, 0, fmt.Errorf("строка %q вне элемента списка", t)
		}
	}
	for i := range items {
		items[i].tag = yamlItemTag(items[i].lines, indent)
	}
	return items, indent, nil
}

func yamlItemTag(lines []string, indent int) string {
	first := strings.TrimSpace(lines[0])[2:]
	if strings.HasPrefix(first, "{") {
		var o struct {
			Tag string `json:"tag"`
		}
		_ = json.Unmarshal([]byte(first), &o)
		return o.Tag
	}
	cands := []string{first}
	for _, l := range lines[1:] {
		if indentOf(l) == indent+2 {
			cands = append(cands, strings.TrimSpace(l))
		}
	}
	for _, c := range cands {
		if v, ok := strings.CutPrefix(c, "tag:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func (OpenWrt) NodeTags(r Runner) ([]string, error) {
	text, err := readFile(r, openwrtXrayConfigPath)
	if err != nil {
		return nil, err
	}
	return openwrtNodeTags(text)
}

func openwrtNodeTags(text string) ([]string, error) {
	lines := splitLines(text)
	start, end, ok := yamlSection(lines, "outbounds")
	if !ok {
		return nil, fmt.Errorf("%w: в %s нет outbounds", ErrUnknownFormat, openwrtXrayConfigPath)
	}
	items, _, err := yamlItems(lines[start+1 : end])
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnknownFormat, openwrtXrayConfigPath, err)
	}
	var tags []string
	for _, it := range items {
		tags = append(tags, it.tag)
	}
	return nodeTagsOf(tags), nil
}

func (OpenWrt) PlanNodes(r Runner, set nodes.Set) (*Plan, error) {
	text, err := readFile(r, openwrtXrayConfigPath)
	if err != nil {
		return nil, err
	}
	updated, err := openwrtWithNodes(text, set)
	if err != nil {
		return nil, err
	}
	plan := &Plan{}
	if sameText(updated, text) {
		return plan, nil
	}
	balancers := "balancers: " + nodes.Compact([]nodes.Balancer{set.Balancer(openwrtMainTag)})
	plan.Changes = append(plan.Changes, FileChange{
		Path:    openwrtXrayConfigPath,
		Before:  text,
		Content: updated,
		Validate: func(content string) error {
			tags, err := openwrtNodeTags(content)
			if err != nil {
				return err
			}
			if strings.Join(tags, ",") != strings.Join(set.Tags(), ",") {
				return fmt.Errorf("ноды собраны неверно")
			}
			lines := splitLines(content)
			start, end, _ := yamlSection(lines, "routing")
			bal, main := false, false
			for _, l := range lines[start+1 : end] {
				bal = bal || strings.TrimSpace(l) == balancers
				main = main || openwrtMainRuleRe.MatchString(l)
			}
			if !bal || main {
				return fmt.Errorf("балансировщик собран неверно")
			}
			return nil
		},
		RemoteCheck: xrayTest("XRAY_LOCATION_ASSET=/usr/share/xray ", "yaml"),
	})
	return plan, nil
}

func openwrtWithNodes(text string, set nodes.Set) (string, error) {
	lines := splitLines(text)
	fail := func(format string, a ...any) (string, error) {
		return "", fmt.Errorf("%w: %s: %s", ErrUnknownFormat, openwrtXrayConfigPath, fmt.Sprintf(format, a...))
	}

	start, end, ok := yamlSection(lines, "outbounds")
	if !ok {
		return fail("нет outbounds")
	}
	items, indent, err := yamlItems(lines[start+1 : end])
	if err != nil {
		return fail("%v", err)
	}
	var out []string
	placed := false
	for _, it := range items {
		if it.tag != openwrtMainTag && !strings.HasPrefix(it.tag, nodes.TagPrefix) {
			out = append(out, it.lines...)
			continue
		}
		if !placed {
			for _, n := range set.Nodes {
				out = append(out, strings.Repeat(" ", indent)+"- "+nodes.Compact(n.Outbound(openwrtMark)))
			}
			placed = true
		}
	}
	if !placed {
		return fail("нет выхода %s", openwrtMainTag)
	}
	lines = append(append(append([]string{}, lines[:start+1]...), out...), lines[end:]...)

	start, end, ok = yamlSection(lines, "routing")
	if !ok {
		return fail("нет routing")
	}
	child := -1
	balancer := -1
	for i := start + 1; i < end; i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if child < 0 {
			child = indentOf(l)
		}
		if indentOf(l) == child && strings.HasPrefix(strings.TrimSpace(l), "balancers:") {
			balancer = i
		}
		lines[i] = openwrtMainRuleRe.ReplaceAllString(l, "${1}balancerTag: "+openwrtMainTag)
	}
	if child <= 0 {
		return fail("пустой routing")
	}
	bal := strings.Repeat(" ", child) + "balancers: " + nodes.Compact([]nodes.Balancer{set.Balancer(openwrtMainTag)})
	switch {
	case balancer >= 0 && balancer+1 < end && indentOf(lines[balancer+1]) > child && strings.TrimSpace(lines[balancer+1]) != "":
		return fail("balancers записан не одной строкой")
	case balancer >= 0:
		lines[balancer] = bal
	default:
		lines = append(append(append([]string{}, lines[:start+1]...), bal), lines[start+1:]...)
	}

	if _, _, block := yamlSection(lines, "burstObservatory"); block {
		return fail("burstObservatory записан не одной строкой")
	}
	obs := "burstObservatory: " + nodes.Compact(set.Observatory())
	replaced := false
	for i, l := range lines {
		if strings.HasPrefix(l, "burstObservatory:") {
			lines[i], replaced = obs, true
		}
	}
	if !replaced {
		lines = append(lines, obs)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func (OpenWrt) RestartProxy(r Runner) error {
	out, err := r.Run("/etc/init.d/xray restart && sleep 2 && pidof xray >/dev/null")
	if err != nil {
		return fmt.Errorf("перезапуск xray: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

func (OpenWrt) NodeSpeed(r Runner, tag string) (float64, error) {
	if !nodeTagRe.MatchString(tag) {
		return 0, fmt.Errorf("недопустимый тег %q", tag)
	}
	return speedOf(r, speedScript("XRAY_LOCATION_ASSET=/usr/share/xray ", []string{openwrtXrayConfigPath},
		probeConfig([]string{probeInboundTo("all-in", speedHost), openwrtProbeDNS()}, "all-in", tag)))
}

func (OpenWrt) NodeProbe(r Runner, tag string) (bool, error) {
	if !nodeTagRe.MatchString(tag) {
		return false, fmt.Errorf("недопустимый тег %q", tag)
	}
	return yesNo(r, probeScript("XRAY_LOCATION_ASSET=/usr/share/xray ", []string{openwrtXrayConfigPath},
		probeConfig([]string{probeInbound("all-in"), openwrtProbeDNS()}, "all-in", tag)))
}
