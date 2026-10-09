package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func routingFixture(domains, udpPorts string) string {
	return `{
  "routing": {
    "rules": [
      {
        "type": "field",
        "inboundTag": ["redirect", "tproxy"],
        "outboundTag": "vless-reality",
        "domain": [` + domains + `]
      },
      {
        "type": "field",
        "inboundTag": ["redirect", "tproxy"],
        "outboundTag": "vless-reality",
        "network": "udp",
        "port": "` + udpPorts + `"
      },
      {
        "type": "field",
        "inboundTag": ["redirect", "tproxy"],
        "outboundTag": "direct",
        "network": "tcp,udp"
      }
    ]
  }
}
`
}

func TestKeeneticTemplateKeepsUserAndGames(t *testing.T) {
	current := routingFixture(`"ext:geosite_v2fly.dat:telegram"`, "443,"+keeneticUDPRangeDash)
	r := newFakeRunner(map[string]string{keeneticRoutingPath: current, keeneticPortListPath: keeneticUDPRangeColon + "\n"})
	user := []DomainEntry{{Kind: KindDomain, Name: "example.com"}}
	if err := ApplyDomains(r, Keenetic{}, user, nil); err != nil {
		t.Fatal(err)
	}

	template := routingFixture(`"ext:geosite_v2fly.dat:telegram", "ext:geosite_v2fly.dat:openai"`, "443")
	if err := ValidateTemplate(TemplateRouting, template); err != nil {
		t.Fatal(err)
	}

	before := r.files[keeneticRoutingPath]
	res, err := ApplyTemplate(r, Keenetic{}, template, nil, true)
	if err != nil || !res.Changed || res.Added == 0 {
		t.Fatalf("пробный прогон: %+v %v", res, err)
	}
	if r.files[keeneticRoutingPath] != before {
		t.Fatal("пробный прогон изменил файл")
	}

	if _, err := ApplyTemplate(r, Keenetic{}, template, nil, false); err != nil {
		t.Fatal(err)
	}
	text := r.files[keeneticRoutingPath]
	if !json.Valid([]byte(text)) || !strings.Contains(text, "geosite_v2fly.dat:openai") {
		t.Fatal("эталон не применился")
	}
	if got, _ := keeneticDomains(text); !sameEntries(got, user) {
		t.Fatalf("правило пользователя потерялось: %v", got)
	}
	if ports, _ := keeneticPorts(text); !contains(ports, keeneticUDPRangeDash) {
		t.Fatal("игровые порты потерялись")
	}

	if res, err := ApplyTemplate(r, Keenetic{}, template, nil, true); err != nil || res.Changed {
		t.Fatalf("повторный прогон должен быть пустым: %+v %v", res, err)
	}
}

func TestOpenWrtTemplateKeepsUser(t *testing.T) {
	current := "# old\nserver=/old.example/127.0.0.1#5353\n"
	r := newFakeRunner(map[string]string{openwrtServersPath: current})
	user := []DomainEntry{{Kind: KindDomain, Name: "example.com"}}
	if err := ApplyDomains(r, OpenWrt{}, user, nil); err != nil {
		t.Fatal(err)
	}

	template := "# google\nserver=/google.com/gstatic.com/127.0.0.1#5353\n\n" + userSection + "\n\n# stray\nserver=/stray.example/127.0.0.1#5353\n"
	if err := ValidateTemplate(TemplateServers, template); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyTemplate(r, OpenWrt{}, template, nil, false); err != nil {
		t.Fatal(err)
	}
	text := r.files[openwrtServersPath]
	if strings.Contains(text, "old.example") || strings.Contains(text, "stray.example") {
		t.Fatalf("остались старые строки:\n%s", text)
	}
	if !strings.Contains(text, "server=/google.com/gstatic.com/127.0.0.1#5353") {
		t.Fatal("эталон не применился")
	}
	if !sameEntries(openwrtDomains(text), user) {
		t.Fatalf("секция пользователя потерялась:\n%s", text)
	}
}

func TestValidateTemplateRejects(t *testing.T) {
	cases := map[string]string{
		TemplateRouting: `{"routing": {"rules": []}}`,
		TemplateServers: "server=/ok.example/127.0.0.1#5353\naddress=/bad/1.2.3.4\n",
	}
	for name, content := range cases {
		if err := ValidateTemplate(name, content); err == nil {
			t.Fatalf("%s: ожидал ошибку", name)
		}
	}
	if err := ValidateTemplate("other.txt", "x"); err == nil {
		t.Fatal("неизвестный эталон должен отклоняться")
	}
}
