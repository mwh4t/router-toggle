package main

import (
	"strings"
	"testing"
	"time"

	"router-toggle/internal/api"
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
