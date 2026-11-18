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
	if host == "" {
		host = "unix://" + defaultSocket
	}
	switch {
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

// ListContainers returns all containers (including stopped and created ones)
// matching a label filter ("key" or "key=value"; empty for all).
func (c *Client) ListContainers(ctx context.Context, label string) ([]Container, error) {
	q := url.Values{"all": {"1"}}
	if label != "" {
		f, _ := json.Marshal(map[string][]string{"label": {label}})
		q.Set("filters", string(f))
	}
	var out []Container
	err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &out)
	return out, err
}
