// Package config loads and validates the secret-sync configuration.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Action names supported in on_change.
const (
	ActionNone    = "none"
	ActionRestart = "restart"
	ActionSignal  = "signal"
)

// Backend names.
const (
	BackendFile  = "file"
	BackendSwarm = "swarm"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Config is the root configuration document.
type Config struct {
	Bitwarden Bitwarden     `yaml:"bitwarden"`
	Interval  time.Duration `yaml:"interval"`
	// Rotate is the default for secrets that do not set rotate explicitly.
	Rotate    *bool     `yaml:"rotate"`
	SecretDir string    `yaml:"secret_dir"`
	ReadyFile string    `yaml:"ready_file"`
	Webhook   Webhook   `yaml:"webhook"`
	Docker    Docker    `yaml:"docker"`
	Discovery Discovery `yaml:"discovery"`
	Secrets   []Secret  `yaml:"secrets"`
}

// Discovery configures secrets that are declared with container labels.
type Discovery struct {
	// Labels enables reading secret definitions from container labels
	// (secret-sync.<name>.<key>). Requires Docker API access.
	Labels bool `yaml:"labels"`
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

// Docker configures access to the Docker Engine API.
type Docker struct {
	// Host is unix:///path or tcp://host:port. Empty means the default socket.
	Host string `yaml:"host"`
}

// Secret maps one Vaultwarden value to one Docker secret.
type Secret struct {
	Name     string   `yaml:"name"`
	Source   Source   `yaml:"source"`
	Backend  string   `yaml:"backend"`
	Path     string   `yaml:"path"`
	Mode     string   `yaml:"mode"`
	Rotate   *bool    `yaml:"rotate"`
	OnChange OnChange `yaml:"on_change"`

	FileMode os.FileMode `yaml:"-"`
}

// Source selects a value in Vaultwarden.
type Source struct {
	// Item is the item name or ID.
	Item string `yaml:"item"`
	// Folder and Collection optionally narrow the item lookup by name.
	Folder     string `yaml:"folder"`
	Collection string `yaml:"collection"`
	// Field is password (default), username, notes, uri or custom:<name>.
	Field string `yaml:"field"`
}

// OnChange describes what to do with consumers after a rotation.
type OnChange struct {
	Action     string   `yaml:"action"`
	Containers []string `yaml:"containers"`
	Signal     string   `yaml:"signal"`
}

// RotateEnabled reports whether the secret may be updated after creation.
func (c *Config) RotateEnabled(s Secret) bool {
	if s.Rotate != nil {
		return *s.Rotate
	}
	if c.Rotate != nil {
		return *c.Rotate
	}
	return true
}

// Load reads, defaults and validates the configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse parses and validates configuration bytes.
func Parse(raw []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	return &c, nil
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
	if len(c.Secrets) == 0 && !c.Discovery.Labels {
		return fmt.Errorf("no secrets configured (add secrets: entries or enable discovery.labels)")
	}

	seen := map[string]bool{}
	for i := range c.Secrets {
		s := &c.Secrets[i]
		if seen[s.Name] {
			return fmt.Errorf("secrets[%d]: duplicate name %q", i, s.Name)
		}
		seen[s.Name] = true
		if err := NormalizeSecret(s); err != nil {
			return fmt.Errorf("secrets[%d]: %w", i, err)
		}
	}
	return nil
}

// NormalizeSecret applies defaults to a secret and validates it. It is used
// for secrets from the config file and for secrets discovered from labels.
func NormalizeSecret(s *Secret) error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("invalid name %q", s.Name)
	}
	if s.Source.Item == "" {
		return fmt.Errorf("secret %q: source.item is required", s.Name)
	}
	if s.Source.Field == "" {
		s.Source.Field = "password"
	}
	if s.Backend == "" {
		s.Backend = BackendFile
	}
	switch s.Backend {
	case BackendFile:
		if s.Path == "" {
			s.Path = s.Name
		}
	case BackendSwarm:
	default:
		return fmt.Errorf("secret %q: unknown backend %q", s.Name, s.Backend)
	}

	s.FileMode = 0o400
	if s.Mode != "" {
		m, err := strconv.ParseUint(s.Mode, 8, 32)
		if err != nil {
			return fmt.Errorf("secret %q: invalid mode %q", s.Name, s.Mode)
		}
		s.FileMode = os.FileMode(m)
	}

	if s.OnChange.Action == "" {
		s.OnChange.Action = ActionNone
	}
	switch s.OnChange.Action {
	case ActionNone, ActionRestart:
	case ActionSignal:
		if s.OnChange.Signal == "" {
			s.OnChange.Signal = "SIGHUP"
		}
	default:
		return fmt.Errorf("secret %q: unknown on_change.action %q", s.Name, s.OnChange.Action)
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
