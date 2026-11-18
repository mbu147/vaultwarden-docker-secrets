// Package syncer pushes opted-in Vaultwarden items to Compose env files and
// Arcane global variables.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/arcane"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

// VaultSource returns a fresh vault snapshot.
type VaultSource interface {
	Fetch(ctx context.Context) (*bw.Vault, error)
}

// Containers is the subset of the Docker API used to find Compose projects.
type Containers interface {
	// ListContainers includes stopped and created containers.
	ListContainers(ctx context.Context, label string) ([]docker.Container, error)
}

// VariableStore is the subset of the Arcane API used to push variables.
// Secret values are never returned by Arcane, so they cannot be compared.
type VariableStore interface {
	List(ctx context.Context) ([]arcane.Variable, error)
	Create(ctx context.Context, v arcane.Variable) error
	Update(ctx context.Context, id string, v arcane.Variable) error
}

// Syncer performs synchronization runs.
type Syncer struct {
	Cfg        *config.Config
	Vault      VaultSource
	Containers Containers    // may be nil if Docker is not available
	Variables  VariableStore // may be nil if Arcane is not configured

	varHashes map[string]string // pushed variable (key+scope) -> value hash
	trigger   chan struct{}
	healthy   atomic.Bool
	runMu     sync.Mutex
}

// New creates a Syncer.
func New(cfg *config.Config, v VaultSource, c Containers) *Syncer {
	return &Syncer{Cfg: cfg, Vault: v, Containers: c,
		trigger: make(chan struct{}, 1), varHashes: map[string]string{}}
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
		if err := s.SyncOnce(ctx); err != nil {
			slog.Error("sync finished with errors", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.trigger:
		}
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
	items, errs := vault.Synced()

	errs = append(errs, s.syncEnvFiles(ctx, items)...)
	errs = append(errs, s.syncArcane(ctx, items)...)

	if err := s.markReady(len(errs) == 0); err != nil {
		errs = append(errs, err)
	}
	s.healthy.Store(len(errs) == 0)
	slog.Info("sync finished", "items", len(items), "errors", len(errs))
	return errors.Join(errs...)
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func short(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

func (s *Syncer) markReady(ok bool) error {
	if !ok || s.Cfg.ReadyFile == "" {
		return nil
	}
	return os.WriteFile(s.Cfg.ReadyFile, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
}
