// Package docker is a minimal Docker Engine API client built on the standard
// library. It only implements the calls secret-sync needs.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultSocket = "/var/run/docker.sock"

// Client talks to the Docker Engine over a unix socket or TCP.
type Client struct {
	http *http.Client
	base string
}

// New creates a client. host is "", unix:///path or tcp://host:port.
func New(host string) (*Client, error) {
	tr := &http.Transport{}
	base := "http://docker"
	switch {
	case host == "":
		host = "unix://" + defaultSocket
		fallthrough
	case strings.HasPrefix(host, "unix://"):
		sock := strings.TrimPrefix(host, "unix://")
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}
	case strings.HasPrefix(host, "tcp://"):
		base = "http://" + strings.TrimPrefix(host, "tcp://")
	default:
		return nil, fmt.Errorf("unsupported docker host %q", host)
	}
	return &Client{http: &http.Client{Transport: tr, Timeout: 60 * time.Second}, base: base}, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// APIError is returned for non-2xx responses.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker api: %d: %s", e.Status, e.Message) }

// Container is a subset of the container list response.
type Container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
}

// ListContainers returns running containers matching a label filter
// ("key" or "key=value"; empty for all).
func (c *Client) ListContainers(ctx context.Context, label string) ([]Container, error) {
	q := url.Values{}
	if label != "" {
		f, _ := json.Marshal(map[string][]string{"label": {label}})
		q.Set("filters", string(f))
	}
	var out []Container
	err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &out)
	return out, err
}

// RestartContainer restarts a container by ID or name.
func (c *Client) RestartContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/restart", nil, nil, nil)
}

// KillContainer sends a signal to a container by ID or name.
func (c *Client) KillContainer(ctx context.Context, id, signal string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/kill",
		url.Values{"signal": {signal}}, nil, nil)
}

// Secret is a Swarm secret (metadata only).
type Secret struct {
	ID        string `json:"ID"`
	CreatedAt string `json:"CreatedAt"`
	Spec      struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	} `json:"Spec"`
}

// ListSecrets returns Swarm secrets matching a label filter.
func (c *Client) ListSecrets(ctx context.Context, label string) ([]Secret, error) {
	f, _ := json.Marshal(map[string][]string{"label": {label}})
	var out []Secret
	err := c.do(ctx, http.MethodGet, "/secrets", url.Values{"filters": {string(f)}}, nil, &out)
	return out, err
}

// CreateSecret creates a Swarm secret and returns its ID.
func (c *Client) CreateSecret(ctx context.Context, name string, labels map[string]string, data []byte) (string, error) {
	body := map[string]any{"Name": name, "Labels": labels, "Data": data}
	var out struct {
		ID string `json:"ID"`
	}
	err := c.do(ctx, http.MethodPost, "/secrets/create", nil, body, &out)
	return out.ID, err
}

// DeleteSecret removes a Swarm secret.
func (c *Client) DeleteSecret(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/secrets/"+url.PathEscape(id), nil, nil, nil)
}

// Service holds a Swarm service with its raw spec so it can be round-tripped.
type Service struct {
	ID      string `json:"ID"`
	Version struct {
		Index uint64 `json:"Index"`
	} `json:"Version"`
	Spec map[string]any `json:"Spec"`
}

// ListServices returns all Swarm services including their specs.
func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	var out []Service
	err := c.do(ctx, http.MethodGet, "/services", nil, nil, &out)
	return out, err
}

// UpdateService posts a modified spec for a service.
func (c *Client) UpdateService(ctx context.Context, s Service) error {
	q := url.Values{"version": {fmt.Sprint(s.Version.Index)}}
	return c.do(ctx, http.MethodPost, "/services/"+url.PathEscape(s.ID)+"/update", q, s.Spec, nil)
}
