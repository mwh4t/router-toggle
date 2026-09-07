package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Preset struct {
	Name       string
	Firmware   string // openwrt или keenetic
	LocalIP    string
	LocalPort  int
	TunnelPort int
	SSHUser    string
	AuthType   string
	AuthSecret string
}

const (
	defaultLocalIP = "192.168.1.1"
	defaultSSHUser = "root"
)

func localPortFor(firmware string) int {
	if firmware == "openwrt" {
		return 22
	}
	return 222 // keenetic
}

func GetAnonymousIDToken(apiKey string) (string, error) {
	url := fmt.Sprintf("https://identitytoolkit.googleapis.com/v1/accounts:signUp?key=%s", apiKey)
	reqBody, _ := json.Marshal(map[string]bool{"returnSecureToken": true})

	resp, err := http.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("анонимный вход в Firebase не удался (статус %d): %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		IDToken string `json:"idToken"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.IDToken == "" {
		return "", fmt.Errorf("Firebase не вернул idToken — проверь, что Anonymous auth включён в консоли")
	}
	return parsed.IDToken, nil
}

func FetchPresets(projectID, idToken string) ([]Preset, error) {
	url := fmt.Sprintf(
		"https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/presets",
		projectID,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+idToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Firestore вернул статус %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		Documents []struct {
			Name   string `json:"name"`
			Fields map[string]struct {
				StringValue string `json:"stringValue"`
			} `json:"fields"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	var presets []Preset
	for _, doc := range raw.Documents {
		parts := strings.Split(doc.Name, "/")
		documentID := parts[len(parts)-1]

		tunnelPort, err := strconv.Atoi(documentID)
		if err != nil {
			fmt.Printf("Пропускаю документ %q: ID не похож на номер порта\n", documentID)
			continue
		}

		firmware := doc.Fields["firmware"].StringValue
		displayName := doc.Fields["name"].StringValue
		if displayName == "" {
			displayName = documentID
		}

		presets = append(presets, Preset{
			Name:       displayName,
			Firmware:   firmware,
			LocalIP:    defaultLocalIP,
			LocalPort:  localPortFor(firmware),
			TunnelPort: tunnelPort,
			SSHUser:    defaultSSHUser,
			AuthType:   "password",
			AuthSecret: doc.Fields["auth_secret"].StringValue,
		})
	}
	return presets, nil
}
