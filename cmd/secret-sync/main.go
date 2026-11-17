// Command secret-sync syncs Vaultwarden/Bitwarden items into Docker secrets.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	dataDir, err := os.MkdirTemp("", "bw-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dataDir)

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
	slog.Info("secret-sync started", "version", version, "interval", cfg.Interval, "restarts", containers != nil)

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

// dockerAvailable reports whether restarts can be attempted: a Docker host is
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
