// Package arcane is a minimal client for the Arcane global variables API
// (GET/POST /api/variables, PATCH /api/variables/{id}).
package arcane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Variable is a global variable. Arcane never returns the value of secret
// variables, so values cannot be compared with the remote state.
type Variable struct {
	ID                   string   `json:"id,omitempty"`
	Key                  string   `json:"key"`
	Value                string   `json:"value,omitempty"`
	IsSecret             bool     `json:"isSecret"`
	AllEnvironments      bool     `json:"allEnvironments"`
	InjectIntoContainers bool     `json:"injectIntoContainers"`
	EnvironmentIDs       []string `json:"environmentIds,omitempty"`
}

// Client talks to one Arcane instance with an API key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New creates a client for baseURL (e.g. https://arcane.example.com).
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("arcane %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("arcane %s %s: status %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("arcane %s %s: decode response: %w", method, path, err)
	}
	return nil
}

// List returns all global variables (secret values are empty).
func (c *Client) List(ctx context.Context) ([]Variable, error) {
	var out struct {
		Data []Variable `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/variables", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Environment is an Arcane environment (the local one or a remote agent).
type Environment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Environments lists all environments, page by page. Newer Arcane versions
// serve them under /api/agents, older ones under /api/environments.
func (c *Client) Environments(ctx context.Context) ([]Environment, error) {
	const limit = 100
	base := "/api/agents"
	var all []Environment
	seen := map[string]bool{}
	for start := 0; start < 100*limit; start += limit {
		var out struct {
			Data []Environment `json:"data"`
		}
		path := fmt.Sprintf("%s?start=%d&limit=%d", base, start, limit)
		err := c.do(ctx, http.MethodGet, path, nil, &out)
		if err != nil && start == 0 && base == "/api/agents" && strings.Contains(err.Error(), "status 404") {
			base = "/api/environments"
			start -= limit
			continue
		}
		if err != nil {
			return nil, err
		}
		added := 0
		for _, e := range out.Data {
			if !seen[e.ID] {
				seen[e.ID] = true
				all = append(all, e)
				added++
			}
		}
		if len(out.Data) < limit || added == 0 {
			break
		}
	}
	return all, nil
}

// Create adds a variable.
func (c *Client) Create(ctx context.Context, v Variable) error {
	v.ID = ""
	return c.do(ctx, http.MethodPost, "/api/variables", v, nil)
}

// Delete removes a variable.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/variables/"+url.PathEscape(id), nil, nil)
}

// Update replaces value, secret flag, inject flag and scope of a variable.
func (c *Client) Update(ctx context.Context, id string, v Variable) error {
	body := map[string]any{
		"value":                v.Value,
		"isSecret":             v.IsSecret,
		"injectIntoContainers": v.InjectIntoContainers,
		"allEnvironments":      v.AllEnvironments,
		"environmentIds":       v.EnvironmentIDs,
	}
	return c.do(ctx, http.MethodPatch, "/api/variables/"+url.PathEscape(id), body, nil)
}
