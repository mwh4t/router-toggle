package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var version = "dev" // задаётся при сборке

const releasesURL = "https://api.github.com/repos/mwh4t/router-toggle/releases/latest"

type release struct {
	Tag string `json:"tag_name"`
	URL string `json:"html_url"`
}

// фоновая проверка
func checkUpdate() <-chan release {
	ch := make(chan release, 1)
	if version == "dev" || strings.Contains(version, "-") {
		return ch
	}
	go func() {
		c := &http.Client{Timeout: 5 * time.Second}
		resp, err := c.Get(releasesURL)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var r release
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.Tag == "" || r.Tag == version {
			return
		}
		ch <- r
	}()
	return ch
}

func showUpdate(ch <-chan release) {
	select {
	case r := <-ch:
		fmt.Printf("\nДоступна новая версия %s (у вас %s): %s\n\n", r.Tag, version, r.URL)
	default:
	}
}
