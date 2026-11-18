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
	// DataDir holds the bw CLI state: login and encrypted vault cache (default
	// /data). It survives restarts only if it is a mount point; otherwise a temp
	// directory is used and the cache is rebuilt on every start.
	DataDir   string  `yaml:"data_dir"`
	ReadyFile string  `yaml:"ready_file"`
	Webhook   Webhook `yaml:"webhook"`
	Docker    Docker  `yaml:"docker"`
	EnvFile   EnvFile `yaml:"env_file"`
	Arcane    Arcane  `yaml:"arcane"`
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

// Docker configures access to the Docker Engine API (used to find Compose
// projects for the env file).
type Docker struct {
	// Host is unix:///path or tcp://host:port. Empty means the default socket.
	Host string `yaml:"host"`
}

// EnvFile configures writing dotenv files into Compose project directories.
// It is enabled when Dir is set.
type EnvFile struct {
	// Dir is the base directory that contains the Compose projects (mounted
	// into secret-sync at the same path Compose reports as working_dir). Only
	// projects below it are written.
	Dir string `yaml:"dir"`
	// Name of the file inside each project directory (default .secrets).
	Name string `yaml:"name"`
	// Mode is the octal file mode (default 0600).
	Mode string `yaml:"mode"`

	FileMode os.FileMode `yaml:"-"`
}

// Arcane configures the push of synced items to Arcane global variables. It
// is enabled when URL is set, unless Enabled is false.
type Arcane struct {
	URL        string `yaml:"url"`
	APIKeyFile string `yaml:"api_key_file"`
	// Enabled is a central switch (default true when URL is set).
	Enabled *bool `yaml:"enabled"`
}

// ArcaneEnabled reports whether items are pushed to Arcane.
func (c *Config) ArcaneEnabled() bool {
	return c.Arcane.URL != "" && (c.Arcane.Enabled == nil || *c.Arcane.Enabled)
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
	str(&c.DataDir, "SECRET_SYNC_DATA_DIR")
	str(&c.ReadyFile, "SECRET_SYNC_READY_FILE")
	str(&c.Webhook.Listen, "SECRET_SYNC_WEBHOOK_LISTEN")
	str(&c.Webhook.TokenFile, "SECRET_SYNC_WEBHOOK_TOKEN_FILE")
	str(&c.Docker.Host, "DOCKER_HOST")
	str(&c.EnvFile.Dir, "SECRET_SYNC_ENV_FILE_DIR")
	str(&c.EnvFile.Name, "SECRET_SYNC_ENV_FILE_NAME")
	str(&c.EnvFile.Mode, "SECRET_SYNC_ENV_FILE_MODE")
	str(&c.Arcane.URL, "ARCANE_URL")
	str(&c.Arcane.APIKeyFile, "ARCANE_API_KEY_FILE")
	if err := boolean(&c.Arcane.Enabled, "ARCANE_ENABLED"); err != nil {
		return err
	}

	if v := get("SECRET_SYNC_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("SECRET_SYNC_INTERVAL: %w", err)
		}
		c.Interval = d
	}
	return nil
}

func (c *Config) applyDefaults() error {
	if c.Interval == 0 {
		c.Interval = 5 * time.Minute
	}
	if c.Interval < 10*time.Second {
		return fmt.Errorf("interval must be at least 10s")
	}
	if c.DataDir == "" {
		c.DataDir = "/data"
	}
	if c.ReadyFile == "" {
		c.ReadyFile = "/tmp/secret-sync.ready"
	}
	if c.Webhook.Listen != "" && c.Webhook.TokenFile == "" {
		return fmt.Errorf("webhook.token_file is required when webhook.listen is set")
	}
	if c.EnvFile.Name == "" {
		c.EnvFile.Name = ".secrets"
	}
	if strings.ContainsAny(c.EnvFile.Name, `/\`) || c.EnvFile.Name == "." || c.EnvFile.Name == ".." {
		return fmt.Errorf("invalid env_file.name %q", c.EnvFile.Name)
	}
	c.EnvFile.FileMode = 0o600
	if c.EnvFile.Mode != "" {
		m, err := strconv.ParseUint(c.EnvFile.Mode, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid env_file.mode %q", c.EnvFile.Mode)
		}
		c.EnvFile.FileMode = os.FileMode(m)
	}
	if c.Arcane.URL != "" && !strings.HasPrefix(c.Arcane.URL, "http://") && !strings.HasPrefix(c.Arcane.URL, "https://") {
		return fmt.Errorf("arcane.url must start with http:// or https://")
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
