// Package config loads and validates the secret-sync configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration document. Every setting has a default and
// can be set through an environment variable, so the file is optional.
type Config struct {
	Bitwarden Bitwarden     `yaml:"bitwarden"`
	Interval  time.Duration `yaml:"interval"`
	// Rotate controls whether an existing secret is updated when its value
	// changes in the vault. Default true.
	Rotate *bool `yaml:"rotate"`
	// Prune removes files secret-sync wrote earlier but no longer produces
	// (item deleted, sync_enabled removed, group changed). Default true.
	Prune     *bool   `yaml:"prune"`
	SecretDir string  `yaml:"secret_dir"`
	ReadyFile string  `yaml:"ready_file"`
	FileMode  string  `yaml:"file_mode"`
	Webhook   Webhook `yaml:"webhook"`
	Docker    Docker  `yaml:"docker"`

	// Mode is FileMode parsed (default 0444).
	Mode os.FileMode `yaml:"-"`
}

// Bitwarden holds connection and credential settings. Credentials are read
// from files (preferred, e.g. Docker secrets) or from environment variables.
type Bitwarden struct {
	Server           string `yaml:"server"`
	ClientIDFile     string `yaml:"client_id_file"`
	ClientSecretFile string `yaml:"client_secret_file"`
	PasswordFile     string `yaml:"password_file"`
}

// Webhook configures the optional HTTP trigger endpoint.
type Webhook struct {
	Listen    string `yaml:"listen"`
	TokenFile string `yaml:"token_file"`
}

// Docker configures access to the Docker Engine API (used to restart
// containers after a rotation).
type Docker struct {
	// Host is unix:///path or tcp://host:port. Empty means the default socket.
	Host string `yaml:"host"`
}

// PruneEnabled reports whether stale files are removed (default true).
func (c *Config) PruneEnabled() bool {
	return c.Prune == nil || *c.Prune
}

// RotateEnabled reports whether existing secrets may be updated (default true).
func (c *Config) RotateEnabled() bool {
	return c.Rotate == nil || *c.Rotate
}

// Load reads the optional configuration file, applies environment variable
// overrides and defaults, and validates the result. A missing file is not an
// error: the configuration may consist of environment variables only.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return Parse(raw)
}

// Parse parses configuration bytes (may be empty), applies environment
// variable overrides and defaults, and validates the result.
func Parse(raw []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.applyEnv(os.Getenv); err != nil {
		return nil, err
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyEnv lets environment variables override values from the file.
func (c *Config) applyEnv(get func(string) string) error {
	str := func(dst *string, key string) {
		if v := get(key); v != "" {
			*dst = v
		}
	}
	boolean := func(dst **bool, key string) error {
		v := get(key)
		if v == "" {
			return nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s: invalid boolean %q", key, v)
		}
		*dst = &b
		return nil
	}

	str(&c.Bitwarden.Server, "BW_SERVER")
	str(&c.Bitwarden.ClientIDFile, "BW_CLIENTID_FILE")
	str(&c.Bitwarden.ClientSecretFile, "BW_CLIENTSECRET_FILE")
	str(&c.Bitwarden.PasswordFile, "BW_PASSWORD_FILE")
	str(&c.SecretDir, "SECRET_SYNC_SECRET_DIR")
	str(&c.ReadyFile, "SECRET_SYNC_READY_FILE")
	str(&c.FileMode, "SECRET_SYNC_FILE_MODE")
	str(&c.Webhook.Listen, "SECRET_SYNC_WEBHOOK_LISTEN")
	str(&c.Webhook.TokenFile, "SECRET_SYNC_WEBHOOK_TOKEN_FILE")
	str(&c.Docker.Host, "DOCKER_HOST")

	if v := get("SECRET_SYNC_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("SECRET_SYNC_INTERVAL: %w", err)
		}
		c.Interval = d
	}
	if err := boolean(&c.Rotate, "SECRET_SYNC_ROTATE"); err != nil {
		return err
	}
	return boolean(&c.Prune, "SECRET_SYNC_PRUNE")
}

func (c *Config) applyDefaults() error {
	if c.Interval == 0 {
		c.Interval = 5 * time.Minute
	}
	if c.Interval < 10*time.Second {
		return fmt.Errorf("interval must be at least 10s")
	}
	if c.SecretDir == "" {
		c.SecretDir = "/secrets"
	}
	if c.ReadyFile == "" {
		c.ReadyFile = "/tmp/secret-sync.ready"
	}
	if c.Webhook.Listen != "" && c.Webhook.TokenFile == "" {
		return fmt.Errorf("webhook.token_file is required when webhook.listen is set")
	}
	c.Mode = 0o444
	if c.FileMode != "" {
		m, err := strconv.ParseUint(c.FileMode, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid file_mode %q", c.FileMode)
		}
		c.Mode = os.FileMode(m)
	}
	return nil
}

// ReadCredential returns the trimmed contents of file, falling back to the
// environment variable env when file is empty.
func ReadCredential(file, env string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("no credential provided (file or %s)", env)
}
