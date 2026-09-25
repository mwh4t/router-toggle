package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"router-toggle/internal/api"
)

type Client struct {
	baseURL string
	code    string
	http    *http.Client
}

// ошибка с кодом контракта
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func New(baseURL, code string) *Client {
	return &Client{
		baseURL: baseURL,
		code:    code,
		http:    &http.Client{Timeout: 3 * time.Minute},
	}
}

func (c *Client) Status(routerID int) (api.StateResponse, error) {
	var out api.StateResponse
	err := c.post("/v1/status", api.StatusRequest{Code: c.code, RouterID: routerID}, &out)
	return out, err
}

func (c *Client) Apply(routerID int, op string, value bool) (api.StateResponse, error) {
	var out api.StateResponse
	err := c.post("/v1/apply", api.ApplyRequest{Code: c.code, RouterID: routerID, Op: op, Value: value}, &out)
	return out, err
}

func (c *Client) Reboot(routerID int) error {
	var out map[string]string
	return c.post("/v1/reboot", api.ActionRequest{Code: c.code, RouterID: routerID}, &out)
}

func (c *Client) Check(routerID int) (api.HealthResponse, error) {
	var out api.HealthResponse
	err := c.post("/v1/check", api.ActionRequest{Code: c.code, RouterID: routerID}, &out)
	return out, err
}

func (c *Client) AddRouter(req api.AddRouterRequest) (api.AddRouterResponse, error) {
	req.Code = c.code
	var out api.AddRouterResponse
	err := c.post("/v1/routers/add", req, &out)
	return out, err
}

func (c *Client) Domains(routerID int) (api.DomainsResponse, error) {
	var out api.DomainsResponse
	err := c.post("/v1/domains", api.ActionRequest{Code: c.code, RouterID: routerID}, &out)
	return out, err
}

func (c *Client) SearchDomains(routerID int, query string) (api.DomainSearchResponse, error) {
	var out api.DomainSearchResponse
	err := c.post("/v1/domains/search", api.DomainSearchRequest{Code: c.code, RouterID: routerID, Query: query}, &out)
	return out, err
}

func (c *Client) AddDomain(routerID int, kind, name string) (api.DomainsResponse, error) {
	var out api.DomainsResponse
	err := c.post("/v1/domains/add", api.DomainChangeRequest{Code: c.code, RouterID: routerID, Kind: kind, Name: name}, &out)
	return out, err
}

func (c *Client) RemoveDomain(routerID int, kind, name string) (api.DomainsResponse, error) {
	var out api.DomainsResponse
	err := c.post("/v1/domains/remove", api.DomainChangeRequest{Code: c.code, RouterID: routerID, Kind: kind, Name: name}, &out)
	return out, err
}

func (c *Client) Routers() (api.RoutersResponse, error) {
	var out api.RoutersResponse
	err := c.post("/v1/routers", api.StatusRequest{Code: c.code}, &out)
	return out, err
}

func (c *Client) post(path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.VersionHeader, api.Version)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("нет связи с сервером: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Code == "" {
			// сервер не понял запрос
			return &APIError{Code: api.ErrInternal,
				Message: "Сервер ответил неожиданно. Возможно, нужна новая версия приложения."}
		}
		return &APIError{Code: e.Code, Message: e.Message}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
