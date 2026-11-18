// Package bw wraps the Bitwarden CLI and turns vault items into secret files.
package bw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// Control fields. Both are custom fields on the vault item.
const (
	// SyncField opts an item in: only items where it is true are synced.
	SyncField = "sync_enabled"
	// GroupField optionally places the item below a subdirectory.
	GroupField = "sync_group"
)

// Item is the subset of a Bitwarden item that secret-sync needs.
type Item struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Notes string `json:"notes"`
	Login *struct {
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

// Vault is a decrypted snapshot of the vault items.
type Vault struct {
	Items []Item
}

// File is one secret file of an item.
type File struct {
	Name  string // file name inside the item directory
	Value []byte
}

// SyncedItem is an opted-in item, ready to be written as a directory.
type SyncedItem struct {
	Group string // optional subdirectory
	Dir   string // sanitized item name
	Files []File
}

// RelPath returns the slash separated path of a file below the secret dir.
func (s SyncedItem) RelPath(file string) string {
	if s.Group == "" {
		return s.Dir + "/" + file
	}
	return s.Group + "/" + s.Dir + "/" + file
}

var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Sanitize turns a vault name into a safe file or directory name: unsafe
// character runs become "_" and leading dots are replaced.
func Sanitize(name string) string {
	s := unsafeRe.ReplaceAllString(strings.TrimSpace(name), "_")
	if t := strings.TrimLeft(s, "."); t != s {
		s = strings.Repeat("_", len(s)-len(t)) + t
	}
	return s
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

func (it Item) field(name string) (string, bool) {
	for _, f := range it.Fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// Synced returns all items with sync_enabled set, converted to secret files.
// Items that cannot be mapped (invalid group, empty or duplicate names) are
// skipped and reported in the returned errors.
func (v *Vault) Synced() ([]SyncedItem, []error) {
	var out []SyncedItem
	var errs []error
	owner := map[string]string{} // group/dir -> item name
	for _, it := range v.Items {
		if val, ok := it.field(SyncField); !ok || !truthy(val) {
			continue
		}
		si, groups, err := it.toSynced()
		if err != nil {
			errs = append(errs, fmt.Errorf("item %q: %w", it.Name, err))
			continue
		}
		for _, g := range groups {
			cp := si
			cp.Group = g
			key := cp.RelPath("")
			if other, dup := owner[key]; dup {
				errs = append(errs, fmt.Errorf("item %q: directory %q is already used by item %q", it.Name, key, other))
				continue
			}
			owner[key] = it.Name
			out = append(out, cp)
		}
	}
	return out, errs
}

// toSynced returns the item with its files and the groups it belongs to
// (a single empty group if the field is absent or blank).
func (it Item) toSynced() (SyncedItem, []string, error) {
	si := SyncedItem{Dir: Sanitize(it.Name)}
	if si.Dir == "" || strings.Trim(si.Dir, "_") == "" {
		return si, nil, errors.New("name contains no usable characters")
	}
	groups := []string{""}
	if raw, ok := it.field(GroupField); ok {
		groups = nil
		seen := map[string]bool{}
		for _, g := range strings.Split(raw, ",") {
			g = strings.TrimSpace(g)
			if g == "" || seen[g] {
				continue
			}
			if g == "." || g == ".." || strings.ContainsAny(g, `/\`) {
				return si, nil, fmt.Errorf("invalid group %q", g)
			}
			seen[g] = true
			groups = append(groups, g)
		}
		if len(groups) == 0 {
			groups = []string{""}
		}
	}

	add := func(name, value string) {
		if value != "" {
			si.Files = append(si.Files, File{Name: name, Value: []byte(value)})
		}
	}
	add("notes", it.Notes)
	if it.Login != nil {
		add("password", it.Login.Password)
		add("username", it.Login.Username)
		if len(it.Login.URIs) > 0 {
			add("uri", it.Login.URIs[0].URI)
		}
	}
	used := map[string]bool{}
	for _, f := range si.Files {
		used[f.Name] = true
	}
	for _, f := range it.Fields {
		if f.Name == SyncField || f.Name == GroupField || f.Value == "" {
			continue
		}
		name := Sanitize(f.Name)
		if name == "" {
			return si, nil, fmt.Errorf("custom field %q has no usable name", f.Name)
		}
		if used[name] {
			return si, nil, fmt.Errorf("custom field %q collides with another file %q", f.Name, name)
		}
		used[name] = true
		add(name, f.Value)
	}
	return si, groups, nil
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
	// DataDir is the CLI app data directory. It holds the login and the
	// encrypted vault cache, which is served while the server is unreachable.
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

	if c.cachedLogin(ctx) {
		return c.unlock(ctx)
	}
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

// cachedLogin reports whether DataDir already holds a login for c.Server.
func (c *Client) cachedLogin(ctx context.Context) bool {
	out, err := c.run(ctx, nil, "status")
	if err != nil {
		return false
	}
	var st struct {
		ServerURL string `json:"serverUrl"`
		Status    string `json:"status"`
	}
	if json.Unmarshal([]byte(out), &st) != nil || st.Status == "unauthenticated" {
		return false
	}
	return c.Server == "" || strings.TrimRight(st.ServerURL, "/") == strings.TrimRight(c.Server, "/")
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
		slog.Warn("vault sync failed, using cached vault", "error", err)
	}
	out, err := c.run(ctx, env, "list", "items")
	if err != nil {
		return nil, err
	}
	v := &Vault{}
	if err := json.Unmarshal([]byte(out), &v.Items); err != nil {
		return nil, fmt.Errorf("decode items: %w", err)
	}
	return v, nil
}
