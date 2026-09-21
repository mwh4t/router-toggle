package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Entry struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Admin bool   `json:"admin,omitempty"`
}

type Config struct {
	APIURL  string  `json:"api_url"`
	Entries []Entry `json:"entries"`

	Code string `json:"code,omitempty"` // старый формат
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "router-toggle", "config.json"), nil
}

func loadConfig() (Config, error) {
	var c Config
	path, err := configPath()
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	// перенос из старого формата
	if c.Code != "" && len(c.Entries) == 0 {
		c.Entries = []Entry{{Code: c.Code}}
	}
	c.Code = ""
	return c, nil
}

func saveConfig(c Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func resetConfig() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (c *Config) has(code string) bool {
	for _, e := range c.Entries {
		if e.Code == code {
			return true
		}
	}
	return false
}
