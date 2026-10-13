package nodes

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const (
	linkFI = "vless://3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90@fi1.example.org:443?encryption=none&type=xhttp" +
		"&path=%2Fmwh4t&mode=auto&security=reality&sni=cdn-fi1.example.org&fp=firefox" +
		"&pbk=4VHgF-DA4aa_ArUBWoPQiGoLSQlo038D5SjQA6fKs3M&sid=4af6eba6&spx=%2F#%F0%9F%87%AB%F0%9F%87%AE%20%D0%A4%D0%B8%D0%BD%D0%BB%D1%8F%D0%BD%D0%B4%D0%B8%D1%8F%20%C2%B7%20XHTTP"
	linkSE = "vless://3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90@se1.example.org:443?encryption=none&type=xhttp" +
		"&path=%2Fe9a8&mode=auto&security=reality&sni=cdn-se1.example.org&fp=firefox&pbk=KEY2&sid=50fa" +
		"&extra=%7B%22xPaddingBytes%22%3A%22100-1000%22%7D#SE"
	linkVision = "vless://3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90@fi1.example.org:443?type=tcp&security=reality" +
		"&sni=fi1.example.org&pbk=K&flow=xtls-rprx-vision#Vision"
	linkStub = "vless://00000000-0000-0000-0000-000000000000@0.0.0.0:1?type=xhttp&security=reality&sni=x&pbk=y#Expired"
)

func encode(lines ...string) []byte {
	return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n"))))
}

func TestParse(t *testing.T) {
	set, err := Parse(encode(linkFI, linkVision, linkSE))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.Tags(), ","); got != "node-fi1,node-se1" {
		t.Fatalf("теги %s", got)
	}
	if len(set.Skipped) != 1 || set.Skipped[0] != "Vision" {
		t.Fatalf("пропущено %v", set.Skipped)
	}
	fi := set.Nodes[0]
	if fi.Mode != "packet-up" {
		t.Fatalf("режим %q, нужен packet-up вместо auto", fi.Mode)
	}
	if fi.Name != "🇫🇮 Финляндия · XHTTP" || fi.Path != "/mwh4t" || fi.ServerName != "cdn-fi1.example.org" ||
		fi.ShortID != "4af6eba6" || fi.SpiderX != "/" || fi.Fingerprint != "firefox" {
		t.Fatalf("разобрано неверно: %+v", fi)
	}

	o := Compact(set.Nodes[1].Outbound(2))
	for _, want := range []string{`"tag":"node-se1"`, `"extra":{"xPaddingBytes":"100-1000"}`, `"sockopt":{"mark":2}`, `"address":"se1.example.org"`} {
		if !strings.Contains(o, want) {
			t.Fatalf("в выходе нет %s: %s", want, o)
		}
	}
	if strings.Contains(Compact(fi.Outbound(0)), "sockopt") {
		t.Fatal("sockopt без mark")
	}
	if !json.Valid([]byte(o)) {
		t.Fatal("не json")
	}
}

func TestParseRejects(t *testing.T) {
	if _, err := Parse(encode(linkStub, linkVision)); err != ErrNoNodes {
		t.Fatalf("заглушки панели должны давать ErrNoNodes, получили %v", err)
	}
	if _, err := Parse([]byte("<html>не подписка</html>")); err == nil {
		t.Fatal("html принят за подписку")
	}
}

func TestParsePlainAndDuplicates(t *testing.T) {
	set, err := Parse([]byte(linkFI + "\r\n" + linkFI + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.Tags(), ","); got != "node-fi1,node-fi1-2" {
		t.Fatalf("теги %s", got)
	}
}

func TestHashStable(t *testing.T) {
	a, _ := Parse(encode(linkFI, linkSE))
	b, _ := Parse(encode(linkFI, linkSE))
	c, _ := Parse(encode(linkSE, linkFI))
	if a.Hash() != b.Hash() || a.Hash() == c.Hash() {
		t.Fatal("хеш должен зависеть от нод и порядка")
	}
	if bal := Compact(a.Balancer("proxy")); bal != `{"tag":"proxy","selector":["node-fi1"],"fallbackTag":"node-se1","strategy":{"type":"leastPing"}}` {
		t.Fatal(bal)
	}
}

func TestPrimary(t *testing.T) {
	a, _ := Parse(encode(linkFI, linkSE))
	before := a.Hash()
	a.Primary = "node-se1"
	if bal := Compact(a.Balancer("proxy")); bal != `{"tag":"proxy","selector":["node-se1"],"fallbackTag":"node-fi1","strategy":{"type":"leastPing"}}` {
		t.Fatal(bal)
	}
	if obs := a.Observatory(); len(obs.SubjectSelector) != 1 || obs.SubjectSelector[0] != "node-se1" {
		t.Fatal(obs)
	}
	if a.Hash() == before {
		t.Fatal("хеш должен зависеть от основной ноды")
	}
	a.Primary = "node-gone"
	if a.Main() != "node-fi1" {
		t.Fatal("пропавшая основная должна смениться на первую")
	}
	one, _ := Parse(encode(linkFI))
	if one.Main() != "node-fi1" || one.Backup() != "node-fi1" {
		t.Fatal("одна нода сама себе запасная")
	}
}

func TestOnly(t *testing.T) {
	home := strings.Replace(linkSE, "#SE", "#%F0%9F%87%B8%F0%9F%87%AA%20%D0%A8%D0%B2%D0%B5%D1%86%D0%B8%D1%8F%20%C2%B7%20%D0%A0%D0%BE%D1%83%D1%82%D0%B5%D1%80", 1)
	set, _ := Parse(encode(linkFI, home))
	got := set.Only("· Роутер")
	if strings.Join(got.Tags(), ",") != "node-se1" || len(got.Skipped) != 1 {
		t.Fatalf("%v %v", got.Tags(), got.Skipped)
	}
	plain, _ := Parse(encode(linkFI, linkSE))
	if len(plain.Only("· Роутер").Nodes) != 2 {
		t.Fatal("без меченых хостов берутся все")
	}
}
