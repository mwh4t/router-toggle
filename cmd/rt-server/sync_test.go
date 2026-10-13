package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"router-toggle/internal/api"
	"router-toggle/internal/nodes"
	"router-toggle/internal/store"
)

func TestMaskSub(t *testing.T) {
	got := maskSub("https://sub.example.org/AbCdEf123456")
	if got != "https://sub.example.org/…3456" || strings.Contains(got, "AbCd") {
		t.Fatal(got)
	}
	if maskSub("") != "" {
		t.Fatal("пустая ссылка")
	}
}

func TestNextSync(t *testing.T) {
	loc := time.FixedZone("msk", 3*3600)
	before := time.Date(2026, 10, 12, 5, 0, 0, 0, loc)
	if got := nextSync(before); !got.Equal(time.Date(2026, 10, 12, 6, 30, 0, 0, loc)) {
		t.Fatal(got)
	}
	after := time.Date(2026, 10, 12, 6, 30, 0, 0, loc)
	if got := nextSync(after); !got.Equal(time.Date(2026, 10, 13, 6, 30, 0, 0, loc)) {
		t.Fatal(got)
	}
}

func TestSyncReport(t *testing.T) {
	added, removed := tagDiff([]string{"node-fi1", "node-old"}, []string{"node-fi1", "node-se1"})
	r := api.SyncResult{Router: api.RouterInfo{Name: "r-test"}, Status: "applied", Added: added, Removed: removed}
	if got := syncReport([]api.SyncResult{r}, false); !strings.Contains(got, "r-test · +se1 −old") {
		t.Fatal(got)
	}
	first := api.SyncResult{Router: api.RouterInfo{Name: "r-new"}, Status: "applied", First: true, Added: []string{"fi1", "se1"}}
	if got := syncReport([]api.SyncResult{first}, true); !strings.Contains(got, "переведён на ноды: fi1, se1") {
		t.Fatal(got)
	}
}

func TestPickPrimary(t *testing.T) {
	set := nodes.Set{Nodes: []nodes.Node{{Tag: "node-fi1"}, {Tag: "node-se1"}}}
	cases := []struct {
		fi, se float64
		want   string
	}{
		{50, 60, "node-fi1"},
		{50, 70, "node-se1"},
		{0, 10, "node-se1"},
		{0, 0, "node-fi1"},
		{15, 15, "node-fi1"},
	}
	for _, c := range cases {
		if got := pickPrimary(set, map[string]float64{"node-fi1": c.fi, "node-se1": c.se}); got != c.want {
			t.Fatalf("%v: %s", c, got)
		}
	}
	set.Primary = "node-se1"
	if got := pickPrimary(set, map[string]float64{"node-fi1": 70, "node-se1": 60}); got != "node-se1" {
		t.Fatal("без запаса основная не меняется", got)
	}
}

func TestDevice(t *testing.T) {
	d := device(store.Router{TunnelPort: 22007, Firmware: "openwrt"})
	if !regexp.MustCompile(`^[A-Za-z0-9=-]{10,64}$`).MatchString(d.HWID) {
		t.Fatal(d.HWID)
	}
}

func TestSwitchReport(t *testing.T) {
	r := api.SyncResult{Router: api.RouterInfo{Name: "r-test"}, Status: "applied", Switched: true,
		Primary: "edge-se1", Speeds: map[string]float64{"edge-fi1": 20, "edge-se1": 41.5}}
	if got := syncReport([]api.SyncResult{r}, true); !strings.Contains(got, "основная теперь edge-se1 (edge-fi1 20, edge-se1 42 Мбит/с)") {
		t.Fatal(got)
	}
}

func TestLowMemMessage(t *testing.T) {
	var err error = &lowMemError{37448}
	var low *lowMemError
	if !errors.As(err, &low) || err.Error() != "замер пропущен, у роутера свободно 36 МБ" {
		t.Fatal(err)
	}
}
