// Package bw wraps the Bitwarden CLI and resolves values from vault items.
package bw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// ErrNotFound is returned (wrapped) when no item matches a selector.
var ErrNotFound = errors.New("not found")

// Item is the subset of a Bitwarden item that secret-sync needs.
type Item struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	FolderID      string   `json:"folderId"`
	CollectionIDs []string `json:"collectionIds"`
	Notes         string   `json:"notes"`
	Login         *struct {
		Username string `json:"username"`
		Password string `json:"password"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	} `json:"login"`
	Fields []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"fields"`
}

type named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Vault is a decrypted snapshot of the data needed to resolve secrets.
type Vault struct {
	Items       []Item
	Folders     map[string]string // id -> name
	Collections map[string]string // id -> name
}

// Credentials used to log in and unlock the vault.
type Credentials struct {
	ClientID     string
	ClientSecret string
	Password     string
}

// Client drives the bw CLI.
type Client struct {
	Binary string
	Server string
	Creds  Credentials
	// DataDir is the CLI app data directory; it should live on tmpfs.
	DataDir string

	mu      sync.Mutex
	session string
}

func (c *Client) bin() string {
	if c.Binary == "" {
		return "bw"
	}
	return c.Binary
}

func (c *Client) run(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = append(os.Environ(),
		"BW_NOINTERACTION=true",
		"BITWARDENCLI_APPDATA_DIR="+c.DataDir,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("bw %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Login configures the server, logs in with the API key and unlocks the vault.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Server != "" {
		// Changing the server is only allowed while logged out.
		_, _ = c.run(ctx, nil, "logout")
		if _, err := c.run(ctx, nil, "config", "server", c.Server); err != nil {
			return err
		}
	}
	env := []string{"BW_CLIENTID=" + c.Creds.ClientID, "BW_CLIENTSECRET=" + c.Creds.ClientSecret}
	if _, err := c.run(ctx, env, "login", "--apikey"); err != nil &&
		!strings.Contains(err.Error(), "already logged in") {
		return err
	}
	return c.unlock(ctx)
}

func (c *Client) unlock(ctx context.Context) error {
	out, err := c.run(ctx, []string{"BW_PASSWORD=" + c.Creds.Password},
		"unlock", "--passwordenv", "BW_PASSWORD", "--raw")
	if err != nil {
		return err
	}
	c.session = out
	return nil
}

// Close logs out and discards the session.
func (c *Client) Close(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.run(ctx, nil, "logout")
	c.session = ""
}

// Fetch syncs the vault and returns a decrypted snapshot. It re-unlocks once
// if the session has become invalid.
func (c *Client) Fetch(ctx context.Context) (*Vault, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	v, err := c.fetch(ctx)
	if err == nil {
		return v, nil
	}
	if uerr := c.unlock(ctx); uerr != nil {
		return nil, errors.Join(err, uerr)
	}
	return c.fetch(ctx)
}

func (c *Client) fetch(ctx context.Context) (*Vault, error) {
	env := []string{"BW_SESSION=" + c.session}
	if _, err := c.run(ctx, env, "sync"); err != nil {
		return nil, err
	}
	v := &Vault{Folders: map[string]string{}, Collections: map[string]string{}}

	out, err := c.run(ctx, env, "list", "items")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(out), &v.Items); err != nil {
		return nil, fmt.Errorf("decode items: %w", err)
	}
	for _, kind := range []string{"folders", "collections"} {
		out, err := c.run(ctx, env, "list", kind)
		if err != nil {
			return nil, err
		}
		var list []named
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			return nil, fmt.Errorf("decode %s: %w", kind, err)
		}
		m := v.Folders
		if kind == "collections" {
			m = v.Collections
		}
		for _, n := range list {
			m[n.ID] = n.Name
		}
	}
	return v, nil
}

// Resolve finds exactly one item matching the selector and extracts a field.
func (v *Vault) Resolve(item, folder, collection, field string) ([]byte, error) {
	it, err := v.find(item, folder, collection)
	if err != nil {
		return nil, err
	}
	return extract(it, field)
}

// GroupField is the optional custom field that places an item's secrets in a
// subdirectory (group) of the secret directory.
const GroupField = "group"

// Group returns the value of the custom field "group" of the item. It is empty
// if the field is absent or blank.
func (v *Vault) Group(item, folder, collection string) (string, error) {
	it, err := v.find(item, folder, collection)
	if err != nil {
		return "", err
	}
	for _, f := range it.Fields {
		if f.Name != GroupField {
			continue
		}
		g := strings.TrimSpace(f.Value)
		if g == "." || g == ".." || strings.ContainsAny(g, `/\`) {
			return "", fmt.Errorf("item %q: invalid group %q in field %q", it.Name, g, GroupField)
		}
		return g, nil
	}
	return "", nil
}

func (v *Vault) find(item, folder, collection string) (Item, error) {
	var matches []Item
	for _, it := range v.Items {
		if it.ID != item && it.Name != item {
			continue
		}
		if folder != "" && v.Folders[it.FolderID] != folder {
			continue
		}
		if collection != "" && !v.inCollection(it, collection) {
			continue
		}
		matches = append(matches, it)
	}
	switch len(matches) {
	case 0:
		return Item{}, fmt.Errorf("item %q %w", item, ErrNotFound)
	case 1:
	default:
		return Item{}, fmt.Errorf("item %q is ambiguous (%d matches); use the item ID or narrow by folder/collection", item, len(matches))
	}
	return matches[0], nil
}

func (v *Vault) inCollection(it Item, name string) bool {
	for _, id := range it.CollectionIDs {
		if v.Collections[id] == name {
			return true
		}
	}
	return false
}

func extract(it Item, field string) ([]byte, error) {
	var val string
	switch {
	case field == "notes":
		val = it.Notes
	case field == "password" || field == "username" || field == "uri":
		if it.Login == nil {
			return nil, fmt.Errorf("item %q has no login data", it.Name)
		}
		switch field {
		case "password":
			val = it.Login.Password
		case "username":
			val = it.Login.Username
		default:
			if len(it.Login.URIs) > 0 {
				val = it.Login.URIs[0].URI
			}
		}
	case strings.HasPrefix(field, "custom:"):
		name := strings.TrimPrefix(field, "custom:")
		found := false
		for _, f := range it.Fields {
			if f.Name == name {
				val, found = f.Value, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("item %q has no custom field %q", it.Name, name)
		}
	default:
		return nil, fmt.Errorf("unsupported field %q", field)
	}
	if val == "" {
		return nil, fmt.Errorf("field %q of item %q is empty", field, it.Name)
	}
	return []byte(val), nil
}
