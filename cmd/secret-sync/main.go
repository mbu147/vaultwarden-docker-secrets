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

	"github.com/mbu147/vaultwarden-docker-secrets/internal/backend"
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

	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*cfgPath, *once, *health); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
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
	if err := client.Login(ctx); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client.Close(c)
	}()

	backends := map[string]backend.Backend{
		config.BackendFile: &backend.File{Dir: cfg.SecretDir},
	}
	var containers syncer.Containers
	if needsDocker(cfg) {
		dc, err := docker.New(cfg.Docker.Host)
		if err != nil {
			return err
		}
		containers = dc
		backends[config.BackendSwarm] = &backend.Swarm{Docker: dc}
	}

	s := syncer.New(cfg, client, backends, containers)
	slog.Info("secret-sync started", "version", version, "secrets", len(cfg.Secrets), "interval", cfg.Interval)

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

// needsDocker reports whether any secret uses the swarm backend or an action.
func needsDocker(cfg *config.Config) bool {
	for _, s := range cfg.Secrets {
		if s.Backend == config.BackendSwarm || s.OnChange.Action != config.ActionNone {
			return true
		}
	}
	return false
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
