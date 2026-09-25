package geosite

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

const URL = "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat"

// типы записей в dlc.dat
const (
	TypePlain  = 0
	TypeRegex  = 1
	TypeDomain = 2
	TypeFull   = 3
)

type Rule struct {
	Type  int
	Value string
}

type Match struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

type Index struct {
	mu   sync.RWMutex
	sets map[string][]Rule
}

func NewIndex() *Index { return &Index{sets: map[string][]Rule{}} }

// скачивает во временный файл, старый остаётся при ошибке
func Download(path string) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dlc.dat: %s", resp.Status)
	}

	tmp := path + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := parse(mustRead(tmp)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("скачанный dlc.dat битый: %w", err)
	}
	return os.Rename(tmp, path)
}

func mustRead(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func (x *Index) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sets, err := parse(data)
	if err != nil {
		return err
	}
	x.mu.Lock()
	x.sets = sets
	x.mu.Unlock()
	return nil
}

func (x *Index) Loaded() bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.sets) > 0
}

func (x *Index) Has(name string) bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	_, ok := x.sets[strings.ToLower(name)]
	return ok && !Hidden(name)
}

// домены для dnsmasq, keyword и regexp он не умеет
func (x *Index) Expand(name string) (domains []string, skipped int) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	seen := map[string]bool{}
	for _, r := range x.sets[strings.ToLower(name)] {
		if r.Type != TypeDomain && r.Type != TypeFull {
			skipped++
			continue
		}
		if !seen[r.Value] {
			seen[r.Value] = true
			domains = append(domains, r.Value)
		}
	}
	sort.Strings(domains)
	return domains, skipped
}

// точное совпадение первым, потом вхождения
func (x *Index) Search(query string, limit int) []Match {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()

	var exact, prefix, contains []Match
	for name, rules := range x.sets {
		if Hidden(name) {
			continue
		}
		m := Match{Name: name, Size: len(rules)}
		switch {
		case name == q:
			exact = append(exact, m)
		case strings.HasPrefix(name, q):
			prefix = append(prefix, m)
		case strings.Contains(name, q):
			contains = append(contains, m)
		}
	}
	byName := func(s []Match) {
		sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
	}
	byName(prefix)
	byName(contains)

	out := append(append(exact, prefix...), contains...)
	if len(out) == 0 {
		out = x.byDomain(q)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// wikipedia -> wikimedia, где есть wikipedia.org
func (x *Index) byDomain(q string) []Match {
	var out []Match
	for name, rules := range x.sets {
		if Hidden(name) {
			continue
		}
		for _, r := range rules {
			if r.Type != TypeDomain && r.Type != TypeFull {
				continue
			}
			if r.Value == q || strings.HasPrefix(r.Value, q+".") || strings.HasSuffix(r.Value, "."+q) {
				out = append(out, Match{Name: name, Size: len(rules)})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	return out
}

// сборные списки людям не показываем
func Hidden(name string) bool {
	n := strings.ToLower(name)
	if strings.Contains(n, "!") || strings.Contains(n, "@") {
		return true
	}
	for _, p := range []string{"category-", "geolocation-", "tld-"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	switch n {
	case "cn", "private", "ads", "win-spy", "win-update", "win-extra":
		return true
	}
	return false
}

// GeoSiteList{GeoSite entry=1}, GeoSite{code=1, Domain domain=2}, Domain{type=1, value=2}
func parse(data []byte) (map[string][]Rule, error) {
	sets := map[string][]Rule{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
		if num != 1 || typ != protowire.BytesType {
			if n = protowire.ConsumeFieldValue(num, typ, data); n < 0 {
				return nil, protowire.ParseError(n)
			}
			data = data[n:]
			continue
		}
		site, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]

		code, rules, err := parseSite(site)
		if err != nil {
			return nil, err
		}
		sets[strings.ToLower(code)] = rules
	}
	if len(sets) == 0 {
		return nil, errors.New("пустой файл")
	}
	return sets, nil
}

func parseSite(data []byte) (string, []Rule, error) {
	var code string
	var rules []Rule
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", nil, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, n := protowire.ConsumeString(data)
			if n < 0 {
				return "", nil, protowire.ParseError(n)
			}
			code, data = v, data[n:]
		case num == 2 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return "", nil, protowire.ParseError(n)
			}
			r, err := parseRule(v)
			if err != nil {
				return "", nil, err
			}
			rules, data = append(rules, r), data[n:]
		default:
			if n = protowire.ConsumeFieldValue(num, typ, data); n < 0 {
				return "", nil, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return code, rules, nil
}

func parseRule(data []byte) (Rule, error) {
	var r Rule
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return r, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return r, protowire.ParseError(n)
			}
			r.Type, data = int(v), data[n:]
		case num == 2 && typ == protowire.BytesType:
			v, n := protowire.ConsumeString(data)
			if n < 0 {
				return r, protowire.ParseError(n)
			}
			r.Value, data = v, data[n:]
		default:
			if n = protowire.ConsumeFieldValue(num, typ, data); n < 0 {
				return r, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return r, nil
}
