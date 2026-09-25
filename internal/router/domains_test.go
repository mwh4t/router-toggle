package router

import (
	"encoding/json"
	"strings"
	"testing"
)

const routingWithGroups = `{
  "routing": {
    "domainStrategy": "IPIfNonMatch",
    "rules": [
      {
        "type": "field",
        "inboundTag": ["redirect", "tproxy"],
        "outboundTag": "block",
        "domain": ["ext:geosite_v2fly.dat:category-ads-all"]
      },
      {
        "type": "field",
        "inboundTag": ["redirect", "tproxy"],
        "outboundTag": "vless-reality",
        "domain": [
          "ext:geosite_v2fly.dat:telegram",

          "ext:geosite_v2fly.dat:netflix"
        ]
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

func keeneticDomainFiles() map[string]string {
	return map[string]string{
		keeneticRoutingPath:  routingWithGroups,
		keeneticPortListPath: "443\n",
	}
}

func TestKeeneticDomainsRoundTrip(t *testing.T) {
	r := newFakeRunner(keeneticDomainFiles())
	entries := []DomainEntry{
		{Kind: KindCategory, Name: "openai"},
		{Kind: KindDomain, Name: "example.com"},
	}
	if err := ApplyDomains(r, Keenetic{}, entries, nil); err != nil {
		t.Fatal(err)
	}
	text := r.files[keeneticRoutingPath]
	if !json.Valid([]byte(text)) {
		t.Fatal("после добавления JSON битый")
	}
	if !strings.Contains(text, `"ruleTag": "added-by-user"`) || !strings.Contains(text, `"domain:example.com"`) {
		t.Fatal("правило не добавлено")
	}
	// основное правило с пустой строкой внутри не тронуто
	if !strings.Contains(text, "\"ext:geosite_v2fly.dat:telegram\",\n\n          \"ext:geosite_v2fly.dat:netflix\"") {
		t.Fatal("форматирование основного правила изменилось")
	}
	// после основного, до direct
	if strings.Index(text, "added-by-user") > strings.Index(text, `"outboundTag": "direct"`) {
		t.Fatal("правило стоит после direct")
	}

	got, _ := Keenetic{}.ListDomains(r)
	if !sameEntries(got, entries) {
		t.Fatalf("прочитано %v", got)
	}

	if err := ApplyDomains(r, Keenetic{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r.files[keeneticRoutingPath] != routingWithGroups {
		t.Fatal("после удаления файл не вернулся к исходному")
	}
}

func TestKeeneticHasElsewhere(t *testing.T) {
	r := newFakeRunner(keeneticDomainFiles())
	has, err := Keenetic{}.HasElsewhere(r, DomainEntry{Kind: KindCategory, Name: "netflix"})
	if err != nil || !has {
		t.Fatalf("netflix уже есть в основном правиле: has=%v err=%v", has, err)
	}
}

const serversSample = `# messengers

# telegram
server=/t.me/127.0.0.1#5353
server=/telegram.org/127.0.0.1#5353
`

func TestOpenWrtDomainsRoundTrip(t *testing.T) {
	r := newFakeRunner(map[string]string{openwrtServersPath: serversSample})
	expand := func(string) []string { return []string{"netflix.com", "nflxvideo.net"} }
	entries := []DomainEntry{
		{Kind: KindCategory, Name: "netflix"},
		{Kind: KindDomain, Name: "example.com"},
	}
	if err := ApplyDomains(r, OpenWrt{}, entries, expand); err != nil {
		t.Fatal(err)
	}
	text := r.files[openwrtServersPath]
	for _, want := range []string{userSection, "# netflix", "server=/nflxvideo.net/127.0.0.1#5353", "server=/example.com/127.0.0.1#5353"} {
		if !strings.Contains(text, want) {
			t.Fatalf("нет строки %q", want)
		}
	}
	if !strings.HasPrefix(text, serversSample) {
		t.Fatal("разделы администратора изменились")
	}

	if err := ApplyDomains(r, OpenWrt{}, nil, expand); err != nil {
		t.Fatal(err)
	}
	if r.files[openwrtServersPath] != serversSample {
		t.Fatalf("после удаления файл не вернулся:\n%s", r.files[openwrtServersPath])
	}
}

func TestValidDomain(t *testing.T) {
	for _, ok := range []string{"example.com", "a.b-c.ru", "xn--80ak6aa92e.xn--p1ai"} {
		if !ValidDomain(ok) {
			t.Fatalf("%s должен проходить", ok)
		}
	}
	for _, bad := range []string{"example", "exa mple.com", "http://x.com", "x.com/a", "-x.com", "x.com\nEOF"} {
		if ValidDomain(bad) {
			t.Fatalf("%q не должен проходить", bad)
		}
	}
}
