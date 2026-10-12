package nodes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const TagPrefix = "node-"

var Ping = PingConfig{
	Destination: "http://cp.cloudflare.com/generate_204",
	Interval:    "10s",
	Sampling:    3,
	Timeout:     "5s",
}

var ErrNoNodes = errors.New("в подписке нет подходящих нод")

type Node struct {
	Tag  string
	Name string

	Address string
	Port    int
	ID      string
	Flow    string

	Path  string
	Host  string
	Mode  string
	Extra json.RawMessage

	ServerName  string
	Fingerprint string
	PublicKey   string
	ShortID     string
	SpiderX     string
	Mldsa65     string
}

type Set struct {
	Nodes   []Node
	Skipped []string // не vless reality xhttp
	Primary string
}

// как подписка представляется панели
type Device struct {
	HWID  string
	OS    string
	Model string
}

var (
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	uuidRe  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const maxBody = 1 << 20

func Fetch(subURL string, dev Device) (Set, error) {
	u, err := url.Parse(subURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Set{}, fmt.Errorf("ссылка на подписку должна начинаться с https://")
	}
	req, err := http.NewRequest(http.MethodGet, subURL, nil)
	if err != nil {
		return Set{}, err
	}
	req.Header.Set("User-Agent", "rt-server")
	if dev.HWID != "" {
		req.Header.Set("X-Hwid", dev.HWID)
		req.Header.Set("X-Device-Os", dev.OS)
		req.Header.Set("X-Device-Model", dev.Model)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Set{}, fmt.Errorf("подписка недоступна: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Set{}, fmt.Errorf("подписка ответила 404, ссылка устарела или у владельца кончились устройства")
	}
	if resp.StatusCode != http.StatusOK {
		return Set{}, fmt.Errorf("подписка ответила %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Set{}, fmt.Errorf("подписка оборвалась: %w", err)
	}
	return Parse(body)
}

// base64 со ссылками vless
func Parse(body []byte) (Set, error) {
	text := strings.TrimSpace(string(body))
	if !strings.Contains(text, "://") {
		raw := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' {
				return -1
			}
			return r
		}, text)
		dec, err := base64.StdEncoding.DecodeString(raw + strings.Repeat("=", (4-len(raw)%4)%4))
		if err != nil {
			if dec, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "=")); err != nil {
				return Set{}, fmt.Errorf("подписка не в base64: %w", err)
			}
		}
		text = string(dec)
	}

	var set Set
	used := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, err := parseLink(line)
		if err != nil {
			set.Skipped = append(set.Skipped, linkName(line))
			continue
		}
		base := TagPrefix + strings.ToLower(strings.SplitN(n.Address, ".", 2)[0])
		used[base]++
		n.Tag = base
		if used[base] > 1 {
			n.Tag = fmt.Sprintf("%s-%d", base, used[base])
		}
		set.Nodes = append(set.Nodes, n)
	}
	if len(set.Nodes) == 0 {
		return set, ErrNoNodes
	}
	return set, nil
}

func parseLink(link string) (Node, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Node{}, err
	}
	if u.Scheme != "vless" {
		return Node{}, fmt.Errorf("протокол %s", u.Scheme)
	}
	q := u.Query()
	if q.Get("security") != "reality" || q.Get("type") != "xhttp" {
		return Node{}, fmt.Errorf("не reality xhttp")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return Node{}, fmt.Errorf("порт %q", u.Port())
	}
	host := strings.ToLower(u.Hostname())
	// заглушки панели
	if ip := net.ParseIP(host); ip != nil || !labelRe.MatchString(strings.SplitN(host, ".", 2)[0]) {
		return Node{}, fmt.Errorf("адрес %q", host)
	}
	n := Node{
		Name:        linkName(link),
		Address:     host,
		Port:        port,
		ID:          u.User.Username(),
		Flow:        q.Get("flow"),
		Path:        q.Get("path"),
		Host:        q.Get("host"),
		Mode:        q.Get("mode"),
		ServerName:  q.Get("sni"),
		Fingerprint: q.Get("fp"),
		PublicKey:   q.Get("pbk"),
		ShortID:     q.Get("sid"),
		SpiderX:     q.Get("spx"),
		Mldsa65:     q.Get("pqv"),
	}
	if !uuidRe.MatchString(n.ID) {
		return Node{}, fmt.Errorf("нет uuid")
	}
	if n.PublicKey == "" || n.ServerName == "" {
		return Node{}, fmt.Errorf("нет ключа или имени reality")
	}
	if n.Fingerprint == "" {
		n.Fingerprint = "chrome"
	}
	if extra := q.Get("extra"); extra != "" {
		if !json.Valid([]byte(extra)) {
			return Node{}, fmt.Errorf("extra не json")
		}
		n.Extra = json.RawMessage(extra)
	}
	return n, nil
}

func linkName(link string) string {
	if i := strings.LastIndexByte(link, '#'); i >= 0 {
		if name, err := url.PathUnescape(link[i+1:]); err == nil && name != "" {
			return name
		}
	}
	return "без имени"
}

// только хосты с меткой, если они есть
func (s Set) Only(mark string) Set {
	if mark == "" {
		return s
	}
	var keep []Node
	for _, n := range s.Nodes {
		if strings.Contains(n.Name, mark) {
			keep = append(keep, n)
		}
	}
	if len(keep) == 0 {
		return s
	}
	out := s
	out.Nodes = keep
	for _, n := range s.Nodes {
		if !strings.Contains(n.Name, mark) {
			out.Skipped = append(out.Skipped, n.Name)
		}
	}
	return out
}

func (s Set) Has(tag string) bool {
	for _, n := range s.Nodes {
		if n.Tag == tag {
			return true
		}
	}
	return false
}

func (s Set) Main() string {
	if s.Has(s.Primary) {
		return s.Primary
	}
	return s.Nodes[0].Tag
}

func (s Set) Backup() string {
	main := s.Main()
	for _, n := range s.Nodes {
		if n.Tag != main {
			return n.Tag
		}
	}
	return main
}

func (s Set) Tags() []string {
	out := make([]string, len(s.Nodes))
	for i, n := range s.Nodes {
		out[i] = n.Tag
	}
	return out
}

func (s Set) Names() []string {
	out := make([]string, len(s.Nodes))
	for i, n := range s.Nodes {
		out[i] = n.Name
	}
	return out
}

func (s Set) Hash() string {
	h := sha256.New()
	for _, n := range s.Nodes {
		b, _ := json.Marshal(n.Outbound(0))
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	h.Write([]byte("main " + s.Main()))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type Outbound struct {
	Tag            string         `json:"tag"`
	Protocol       string         `json:"protocol"`
	Settings       outSettings    `json:"settings"`
	StreamSettings streamSettings `json:"streamSettings"`
}

type outSettings struct {
	Vnext []vnext `json:"vnext"`
}

type vnext struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Users   []user `json:"users"`
}

type user struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
	Flow       string `json:"flow,omitempty"`
}

type streamSettings struct {
	Network  string          `json:"network"`
	XHTTP    xhttpSettings   `json:"xhttpSettings"`
	Security string          `json:"security"`
	Reality  realitySettings `json:"realitySettings"`
	Sockopt  *sockopt        `json:"sockopt,omitempty"`
}

type xhttpSettings struct {
	Path  string          `json:"path,omitempty"`
	Host  string          `json:"host,omitempty"`
	Mode  string          `json:"mode,omitempty"`
	Extra json.RawMessage `json:"extra,omitempty"`
}

type realitySettings struct {
	ServerName    string `json:"serverName"`
	Fingerprint   string `json:"fingerprint"`
	PublicKey     string `json:"publicKey"`
	ShortID       string `json:"shortId,omitempty"`
	SpiderX       string `json:"spiderX,omitempty"`
	Mldsa65Verify string `json:"mldsa65Verify,omitempty"`
}

type sockopt struct {
	Mark int `json:"mark"`
}

func (n Node) Outbound(mark int) Outbound {
	o := Outbound{
		Tag:      n.Tag,
		Protocol: "vless",
		Settings: outSettings{Vnext: []vnext{{
			Address: n.Address,
			Port:    n.Port,
			Users:   []user{{ID: n.ID, Encryption: "none", Flow: n.Flow}},
		}}},
		StreamSettings: streamSettings{
			Network:  "xhttp",
			XHTTP:    xhttpSettings{Path: n.Path, Host: n.Host, Mode: n.Mode, Extra: n.Extra},
			Security: "reality",
			Reality: realitySettings{
				ServerName:    n.ServerName,
				Fingerprint:   n.Fingerprint,
				PublicKey:     n.PublicKey,
				ShortID:       n.ShortID,
				SpiderX:       n.SpiderX,
				Mldsa65Verify: n.Mldsa65,
			},
		},
	}
	if mark != 0 {
		o.StreamSettings.Sockopt = &sockopt{Mark: mark}
	}
	return o
}

type PingConfig struct {
	Destination  string `json:"destination"`
	Connectivity string `json:"connectivity"`
	Interval     string `json:"interval"`
	Sampling     int    `json:"sampling"`
	Timeout      string `json:"timeout"`
}

type Observatory struct {
	SubjectSelector []string   `json:"subjectSelector"`
	PingConfig      PingConfig `json:"pingConfig"`
}

// проверяется только основная
func (s Set) Observatory() Observatory {
	return Observatory{SubjectSelector: []string{s.Main()}, PingConfig: Ping}
}

type Balancer struct {
	Tag         string   `json:"tag"`
	Selector    []string `json:"selector"`
	FallbackTag string   `json:"fallbackTag"`
	Strategy    strategy `json:"strategy"`
}

type strategy struct {
	Type string `json:"type"`
}

// основная, пока жива, иначе запасная
func (s Set) Balancer(tag string) Balancer {
	return Balancer{
		Tag:         tag,
		Selector:    []string{s.Main()},
		FallbackTag: s.Backup(),
		Strategy:    strategy{Type: "leastPing"},
	}
}

func Compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
