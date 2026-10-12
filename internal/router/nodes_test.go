package router

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"router-toggle/internal/nodes"
)

const nodesSub = "vless://3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90@fi1.example.org:443?type=xhttp&path=%2Fmwh4t" +
	"&mode=auto&security=reality&sni=cdn-fi1.example.org&fp=firefox&pbk=PBK1&sid=4af6eba6#FI\n" +
	"vless://3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90@se1.example.org:443?type=xhttp&path=%2Fse" +
	"&mode=auto&security=reality&sni=cdn-se1.example.org&fp=firefox&pbk=PBK2&sid=50fa#SE\n"

func nodeSet(t *testing.T, sub string) nodes.Set {
	t.Helper()
	set, err := nodes.Parse([]byte(sub))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// роутер с xray
type nodeRunner struct {
	*fakeRunner
	probe  map[string]bool
	probed []string
}

func (n *nodeRunner) Run(cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, "rt-probe.json"):
		for tag, ok := range n.probe {
			if strings.Contains(cmd, `"outboundTag":"`+tag+`"`) {
				n.probed = append(n.probed, tag)
				if ok {
					return "yes\n", nil
				}
				return "no\n", nil
			}
		}
		return "yes\n", nil
	case strings.Contains(cmd, "xray run -test"),
		strings.Contains(cmd, "/etc/init.d/xray restart"):
		n.calls = append(n.calls, cmd)
		return "", nil
	case strings.Contains(cmd, "&& echo yes || echo no"):
		return "yes\n", nil
	}
	return n.fakeRunner.Run(cmd)
}

const keeneticOutboundsSample = `{
    "outbounds": [
        {
            "tag": "vless-reality",
            "protocol": "vless",
            "settings": { "vnext": [ { "address": "home.example.org", "port": 443, "users": [ { "id": "x", "encryption": "none" } ] } ] }
        },
        { "tag": "direct", "protocol": "freedom" },
        { "tag": "block", "protocol": "blackhole", "settings": { "response": { "type": "http" } } }
    ]
}
`

func keeneticNodeFiles(t *testing.T) map[string]string {
	current := routingFixture(`"ext:geosite_v2fly.dat:telegram"`, "443,"+keeneticUDPRangeDash)
	r := newFakeRunner(map[string]string{keeneticRoutingPath: current, keeneticPortListPath: keeneticUDPRangeColon + "\n"})
	if err := ApplyDomains(r, Keenetic{}, []DomainEntry{{Kind: KindDomain, Name: "example.com"}}, nil); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		keeneticRoutingPath:   r.files[keeneticRoutingPath],
		keeneticPortListPath:  r.files[keeneticPortListPath],
		keeneticOutboundsPath: keeneticOutboundsSample,
	}
}

func TestKeeneticNodes(t *testing.T) {
	r := &nodeRunner{fakeRunner: newFakeRunner(keeneticNodeFiles(t)), probe: map[string]bool{"node-fi1": false, "node-se1": true}}
	set := nodeSet(t, nodesSub)

	res, err := ApplyNodes(r, Keenetic{}, set, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || len(res.Before) != 0 || strings.Join(res.After, ",") != "node-fi1,node-se1" {
		t.Fatalf("результат %+v", res)
	}
	if strings.Join(r.probed, ",") != "node-fi1,node-se1" {
		t.Fatalf("пробы %v", r.probed)
	}

	out := r.files[keeneticOutboundsPath]
	tags, err := keeneticOutboundTags(out)
	if err != nil || strings.Join(tags, ",") != "node-fi1,node-se1,direct,block" {
		t.Fatalf("выходы %v %v\n%s", tags, err, out)
	}
	if !strings.Contains(out, `"burstObservatory"`) || strings.Contains(out, "home.example.org") {
		t.Fatalf("04_outbounds:\n%s", out)
	}

	routing := r.files[keeneticRoutingPath]
	if strings.Contains(routing, `"outboundTag": "vless-reality"`) || !strings.Contains(routing, `"balancers": [{"tag":"vless-reality"`) {
		t.Fatalf("05_routing:\n%s", routing)
	}
	if on, err := (Keenetic{}).ReadState(r, OpUDPProxy); err != nil || !on {
		t.Fatalf("игровые порты после перехода: %v %v", on, err)
	}
	got, err := Keenetic{}.ListDomains(r)
	if err != nil || len(got) != 1 || got[0].Name != "example.com" {
		t.Fatalf("свои сайты после перехода: %v %v", got, err)
	}

	// правки после перехода
	if err := ApplyDomains(r, Keenetic{}, []DomainEntry{{Kind: KindDomain, Name: "example.com"}, {Kind: KindDomain, Name: "example.org"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(r, Keenetic{}, OpUDPProxy, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.files[keeneticRoutingPath], `"outboundTag": "vless-reality"`) {
		t.Fatalf("правило пользователя ушло мимо балансировщика:\n%s", r.files[keeneticRoutingPath])
	}

	// повтор ничего не меняет
	again, err := ApplyNodes(r, Keenetic{}, set, false)
	if err != nil || again.Changed || strings.Join(again.Before, ",") != "node-fi1,node-se1" {
		t.Fatalf("повтор %+v %v", again, err)
	}

	// эталон после перехода
	template := routingFixture(`"ext:geosite_v2fly.dat:telegram", "ext:geosite_v2fly.dat:openai"`, "443")
	tr, err := ApplyTemplate(r, Keenetic{}, template, nil, false)
	if err != nil || !tr.Changed {
		t.Fatalf("эталон: %+v %v", tr, err)
	}
	if err := keeneticCheckBalancer(r.files[keeneticRoutingPath], nodes.Compact([]nodes.Balancer{set.Balancer(keeneticMainTag)})); err != nil {
		t.Fatalf("после эталона: %v\n%s", err, r.files[keeneticRoutingPath])
	}
	if tr, _ := ApplyTemplate(r, Keenetic{}, template, nil, true); tr.Changed {
		t.Fatal("после эталона сравнение должно совпадать")
	}
}

func TestKeeneticNodesAllDeadRollsBack(t *testing.T) {
	files := keeneticNodeFiles(t)
	r := &nodeRunner{fakeRunner: newFakeRunner(files), probe: map[string]bool{"node-fi1": false, "node-se1": false}}
	_, err := ApplyNodes(r, Keenetic{}, nodeSet(t, nodesSub), false)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("ожидался откат, получили %v", err)
	}
	if r.files[keeneticOutboundsPath] != files[keeneticOutboundsPath] || r.files[keeneticRoutingPath] != files[keeneticRoutingPath] {
		t.Fatal("файлы не вернулись")
	}
}

const openwrtConfigSample = `inbounds:
  - listen: 192.168.1.1
    tag: all-in
    port: 1083
    protocol: dokodemo-door
routing:
  domainStrategy: AsIs
  rules:
    - type: field
      inboundTag:
        - dns-in
      outboundTag: dns-out
    - type: field
      inboundTag:
        - all-in
      outboundTag: proxy
outbounds:
  - protocol: dns
    tag: dns-out
    streamSettings:
      sockopt:
        mark: 2
  - protocol: vless
    tag: proxy
    settings:
      vnext:
        - address: home.example.org
          port: 443
    streamSettings:
      network: xhttp
      sockopt:
        mark: 2
dns:
  servers:
    - fakedns
fakedns:
  ipPool: 198.18.0.0/15
`

func TestOpenWrtNodes(t *testing.T) {
	r := &nodeRunner{fakeRunner: newFakeRunner(map[string]string{openwrtXrayConfigPath: openwrtConfigSample}),
		probe: map[string]bool{"node-fi1": true}}
	set := nodeSet(t, nodesSub)

	res, err := ApplyNodes(r, OpenWrt{}, set, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || strings.Join(res.After, ",") != "node-fi1,node-se1" {
		t.Fatalf("результат %+v", res)
	}
	got := r.files[openwrtXrayConfigPath]
	want := []string{
		"routing:\n  balancers: [{\"tag\":\"proxy\",\"selector\":[\"node-\"],\"fallbackTag\":\"node-fi1\"",
		"      balancerTag: proxy\n",
		"      outboundTag: dns-out\n",
		"  - protocol: dns\n    tag: dns-out\n",
		"  - {\"tag\":\"node-fi1\",",
		"\"sockopt\":{\"mark\":2}",
		"dns:\n  servers:\n",
		"\nburstObservatory: {\"subjectSelector\":[\"node-\"]",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("нет %q в\n%s", w, got)
		}
	}
	if strings.Contains(got, "home.example.org") || strings.Contains(got, "outboundTag: proxy") {
		t.Fatalf("старый выход остался:\n%s", got)
	}
	// строки нод json
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "  - {") && !json.Valid([]byte(strings.TrimPrefix(l, "  - "))) {
			t.Fatalf("не json: %s", l)
		}
	}
	var restarted bool
	for _, c := range r.calls {
		restarted = restarted || strings.Contains(c, "/etc/init.d/xray restart")
	}
	if !restarted {
		t.Fatal("xray не перезапущен")
	}

	// замена, а не дописывание
	fewer := nodeSet(t, strings.SplitAfterN(nodesSub, "\n", 2)[1])
	res, err = ApplyNodes(r, OpenWrt{}, fewer, false)
	if err != nil || !res.Changed || strings.Join(res.Before, ",") != "node-fi1,node-se1" {
		t.Fatalf("смена нод %+v %v", res, err)
	}
	got = r.files[openwrtXrayConfigPath]
	if strings.Count(got, "balancers:") != 1 || strings.Count(got, "burstObservatory:") != 1 ||
		strings.Contains(got, "node-fi1") || !strings.Contains(got, "\"fallbackTag\":\"node-se1\"") {
		t.Fatalf("после смены:\n%s", got)
	}
	if tags, _ := openwrtNodeTags(got); strings.Join(tags, ",") != "node-se1" {
		t.Fatalf("теги %v", tags)
	}
}

func TestOpenWrtNodesUnknownFormat(t *testing.T) {
	broken := strings.Replace(openwrtConfigSample, "    tag: proxy\n", "    tag: other\n", 1)
	r := &nodeRunner{fakeRunner: newFakeRunner(map[string]string{openwrtXrayConfigPath: broken})}
	if _, err := ApplyNodes(r, OpenWrt{}, nodeSet(t, nodesSub), false); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("ожидался ErrUnknownFormat, получили %v", err)
	}
}

func TestTemplateWithNodesRejected(t *testing.T) {
	plain := routingFixture(`"ext:geosite_v2fly.dat:telegram"`, "443")
	withNodes, err := keeneticUseBalancer(plain, nodes.Compact([]nodes.Balancer{nodeSet(t, nodesSub).Balancer(keeneticMainTag)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTemplate(TemplateRouting, withNodes); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("эталон с нодами принят: %v", err)
	}
}
