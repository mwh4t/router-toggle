package geosite

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func rule(typ int, value string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(typ))
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	return protowire.AppendString(b, value)
}

func site(code string, rules ...[]byte) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, code)
	for _, r := range rules {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, r)
	}
	return b
}

func list(sites ...[]byte) []byte {
	var b []byte
	for _, s := range sites {
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendBytes(b, s)
	}
	return b
}

func sample() *Index {
	data := list(
		site("NETFLIX",
			rule(TypeDomain, "netflix.com"),
			rule(TypeFull, "www.netflix.net"),
			rule(TypePlain, "netflix"),
			rule(TypeRegex, `^nflx.*\.com$`)),
		site("NETFLIX-ADS", rule(TypeDomain, "ads.netflix.com")),
		site("CATEGORY-MEDIA", rule(TypeDomain, "x.com")),
	)
	sets, err := parse(data)
	if err != nil {
		panic(err)
	}
	return &Index{sets: sets}
}

func TestExpandSkipsKeywordAndRegex(t *testing.T) {
	domains, skipped := sample().Expand("netflix")
	if len(domains) != 2 || skipped != 2 {
		t.Fatalf("domains=%v skipped=%d", domains, skipped)
	}
}

func TestSearchExactFirstAndHidden(t *testing.T) {
	m := sample().Search("netflix", 10)
	if len(m) != 2 || m[0].Name != "netflix" {
		t.Fatalf("получил %+v", m)
	}
	if len(sample().Search("category", 10)) != 0 {
		t.Fatal("служебная категория попала в поиск")
	}
}

func TestSearchByDomainInside(t *testing.T) {
	x := sample()
	x.sets["wikimedia"] = []Rule{{Type: TypeDomain, Value: "wikipedia.org"}}
	m := x.Search("wikipedia", 10)
	if len(m) != 1 || m[0].Name != "wikimedia" {
		t.Fatalf("получил %+v", m)
	}
}
