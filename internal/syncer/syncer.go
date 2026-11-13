// Package syncer ties Vaultwarden, the backends and container actions together.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/backend"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

// RestartLabel marks containers that must be restarted when the listed
// secrets (comma-separated names) change.
const RestartLabel = "secret-sync.restart"

// VaultSource returns a fresh vault snapshot.
type VaultSource interface {
	Fetch(ctx context.Context) (*bw.Vault, error)
}

// Containers is the subset of the Docker API used for rotation actions.
type Containers interface {
	ListContainers(ctx context.Context, label string) ([]docker.Container, error)
	RestartContainer(ctx context.Context, id string) error
	KillContainer(ctx context.Context, id, signal string) error
}

// Syncer performs synchronization runs.
type Syncer struct {
	Cfg        *config.Config
	Vault      VaultSource
	Backends   map[string]backend.Backend
	Containers Containers // may be nil if no actions are configured

	trigger chan struct{}
	healthy atomic.Bool
	runMu   sync.Mutex
}

// New creates a Syncer.
func New(cfg *config.Config, v VaultSource, b map[string]backend.Backend, c Containers) *Syncer {
	return &Syncer{Cfg: cfg, Vault: v, Backends: b, Containers: c, trigger: make(chan struct{}, 1)}
}

// Healthy reports whether the last run succeeded.
func (s *Syncer) Healthy() bool { return s.healthy.Load() }

// Trigger requests an immediate run. Concurrent requests are coalesced.
func (s *Syncer) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run syncs immediately and then on every interval or trigger until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Cfg.Interval)
	defer ticker.Stop()
	for {
		s.runLogged(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.trigger:
		}
	}
}

func (s *Syncer) runLogged(ctx context.Context) {
	if err := s.SyncOnce(ctx); err != nil {
		slog.Error("sync finished with errors", "error", err)
	}
}

// SyncOnce performs one full synchronization run.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	vault, err := s.Vault.Fetch(ctx)
	if err != nil {
		s.healthy.Store(false)
		return fmt.Errorf("fetch vault: %w", err)
	}

	secrets := s.Cfg.Secrets
	var errs []error
	if s.Cfg.Discovery.Labels && s.Containers != nil {
		found, derrs, err := s.discover(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("label discovery: %w", err))
		}
		errs = append(errs, derrs...)
		secrets = append(append([]config.Secret(nil), secrets...), found...)
	}

	for _, sec := range secrets {
		if err := s.syncSecret(ctx, vault, sec); err != nil {
			errs = append(errs, fmt.Errorf("secret %q: %w", sec.Name, err))
		}
	}
	err = errors.Join(errs...)
	s.healthy.Store(err == nil)
	if err == nil {
		s.markReady()
	}
	return err
}

func (s *Syncer) discover(ctx context.Context) ([]config.Secret, []error, error) {
	containers, err := s.Containers.ListContainers(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	configured := map[string]bool{}
	for _, sec := range s.Cfg.Secrets {
		configured[sec.Name] = true
	}
	found, errs := discoverSecrets(containers, configured)
	return found, errs, nil
}

func (s *Syncer) markReady() {
	if s.Cfg.ReadyFile == "" {
		return
	}
	if err := os.WriteFile(s.Cfg.ReadyFile, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644); err != nil {
		slog.Warn("could not write ready file", "error", err)
	}
}

func (s *Syncer) syncSecret(ctx context.Context, vault *bw.Vault, sec config.Secret) error {
	be, ok := s.Backends[sec.Backend]
	if !ok {
		return fmt.Errorf("backend %q is not available", sec.Backend)
	}
	value, err := vault.Resolve(sec.Source.Item, sec.Source.Folder, sec.Source.Collection, sec.Source.Field)
	if err != nil {
		return err
	}
	hash := backend.Hash(value)

	current, exists, err := be.Current(ctx, sec)
	if err != nil {
		return fmt.Errorf("read current value: %w", err)
	}
	if exists && current == hash {
		return nil
	}
	if exists && !s.Cfg.RotateEnabled(sec) {
		slog.Warn("value changed in vault but rotation is disabled; keeping existing value",
			"secret", sec.Name)
		return nil
	}

	if err := be.Write(ctx, sec, value); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	slog.Info("secret written", "secret", sec.Name, "backend", sec.Backend,
		"rotated", exists, "hash", hash[:8])

	// A newly created secret has no consumers that could be using an old value.
	if !exists {
		return nil
	}
	return s.runAction(ctx, sec)
}

func (s *Syncer) runAction(ctx context.Context, sec config.Secret) error {
	// Swarm services are updated by the backend itself.
	if sec.Backend == config.BackendSwarm || sec.OnChange.Action == config.ActionNone {
		return nil
	}
	if s.Containers == nil {
		return errors.New("on_change action configured but Docker API is not available")
	}
	targets, err := s.targets(ctx, sec)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range targets {
		switch sec.OnChange.Action {
		case config.ActionRestart:
			err = s.Containers.RestartContainer(ctx, id)
		case config.ActionSignal:
			err = s.Containers.KillContainer(ctx, id, sec.OnChange.Signal)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", sec.OnChange.Action, id, err))
			continue
		}
		slog.Info("rotation action applied", "secret", sec.Name, "action", sec.OnChange.Action, "container", id)
	}
	return errors.Join(errs...)
}

// targets merges containers named in the config with those labeled for the secret.
func (s *Syncer) targets(ctx context.Context, sec config.Secret) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, c := range sec.OnChange.Containers {
		add(c)
	}
	labeled, err := s.Containers.ListContainers(ctx, RestartLabel)
	if err != nil {
		return nil, err
	}
	for _, c := range labeled {
		for _, n := range strings.Split(c.Labels[RestartLabel], ",") {
			if strings.TrimSpace(n) == sec.Name {
				add(c.ID)
			}
		}
	}
	return out, nil
}
