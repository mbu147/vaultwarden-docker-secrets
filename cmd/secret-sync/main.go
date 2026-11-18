// Command secret-sync syncs Vaultwarden/Bitwarden items into Docker secrets.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/arcane"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/syncer"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", "/etc/secret-sync/config.yaml", "path to the configuration file")
	once := flag.Bool("once", false, "run a single sync and exit")
	health := flag.Bool("healthcheck", false, "exit 0 if the first sync has completed (for Docker HEALTHCHECK)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	setupLogging()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*cfgPath, *once, *health); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// setupLogging writes all logs to stdout (container log viewers mark stderr as
// errors) and honors SECRET_SYNC_LOG_LEVEL (debug|info|warn|error) and
// SECRET_SYNC_LOG_FORMAT (text|json).
func setupLogging() {
	var level slog.Level
	if err := level.UnmarshalText([]byte(os.Getenv("SECRET_SYNC_LOG_LEVEL"))); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if os.Getenv("SECRET_SYNC_LOG_FORMAT") == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}

func run(cfgPath string, once, health bool) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if health {
		if _, err := os.Stat(cfg.ReadyFile); err != nil {
			return fmt.Errorf("not ready: %w", err)
		}
		return nil
	}
	_ = os.Remove(cfg.ReadyFile)

	creds, err := loadCredentials(cfg.Bitwarden)
	if err != nil {
		return err
	}
	dataDir := cfg.DataDir
	if isMountPoint(dataDir) {
		slog.Info("vault cache is persistent", "dir", dataDir)
	} else {
		slog.Info("vault cache is not persistent (no volume mounted at data_dir)", "dir", dataDir)
		if dataDir, err = os.MkdirTemp("", "bw-"); err != nil {
			return err
		}
		defer os.RemoveAll(dataDir)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := &bw.Client{Server: cfg.Bitwarden.Server, Creds: creds, DataDir: dataDir}
	if err := loginWithRetry(ctx, client, once); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client.Close(c)
	}()

	var containers syncer.Containers
	if dockerAvailable(cfg) {
		dc, err := docker.New(cfg.Docker.Host)
		if err != nil {
			return err
		}
		containers = dc
	}

	s := syncer.New(cfg, client, containers)
	if cfg.ArcaneEnabled() {
		key, err := config.ReadCredential(cfg.Arcane.APIKeyFile, "ARCANE_API_KEY")
		if err != nil {
			return fmt.Errorf("arcane api key: %w", err)
		}
		s.Variables = arcane.New(cfg.Arcane.URL, key)
	}
	slog.Info("secret-sync started", "version", version, "interval", cfg.Interval, "docker", containers != nil,
		"env_files", cfg.EnvFile.Dir != "", "arcane", s.Variables != nil)

	if once {
		return s.SyncOnce(ctx)
	}
	if cfg.Webhook.Listen != "" {
		token, err := config.ReadCredential(cfg.Webhook.TokenFile, "")
		if err != nil {
			return fmt.Errorf("webhook token: %w", err)
		}
		go func() {
			if err := s.ServeWebhook(ctx, cfg.Webhook.Listen, token); err != nil {
				slog.Error("webhook stopped", "error", err)
				stop()
			}
		}()
	}
	s.Run(ctx)
	return nil
}

// isMountPoint reports whether dir is a mount point (Linux); a plain directory
// in the container layer would not survive a recreate anyway.
func isMountPoint(dir string) bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	dir = filepath.Clean(dir)
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 4 && f[4] == dir {
			return true
		}
	}
	return false
}

// loginWithRetry keeps trying while the vault is unreachable (e.g. it starts
// together with secret-sync). A single-shot run fails immediately.
func loginWithRetry(ctx context.Context, c *bw.Client, once bool) error {
	delay := 2 * time.Second
	for {
		err := c.Login(ctx)
		if err == nil || once || ctx.Err() != nil {
			return err
		}
		slog.Warn("vault login failed, retrying", "error", err, "in", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < time.Minute {
			delay *= 2
		}
	}
}

// dockerAvailable reports whether containers can be listed: a Docker host is
// configured or the default socket is mounted.
func dockerAvailable(cfg *config.Config) bool {
	if cfg.Docker.Host != "" {
		return true
	}
	_, err := os.Stat("/var/run/docker.sock")
	return err == nil
}

func loadCredentials(b config.Bitwarden) (bw.Credentials, error) {
	var c bw.Credentials
	var err error
	if c.ClientID, err = config.ReadCredential(b.ClientIDFile, "BW_CLIENTID"); err != nil {
		return c, fmt.Errorf("client id: %w", err)
	}
	if c.ClientSecret, err = config.ReadCredential(b.ClientSecretFile, "BW_CLIENTSECRET"); err != nil {
		return c, fmt.Errorf("client secret: %w", err)
	}
	if c.Password, err = config.ReadCredential(b.PasswordFile, "BW_PASSWORD"); err != nil {
		return c, fmt.Errorf("master password: %w", err)
	}
	return c, nil
}
