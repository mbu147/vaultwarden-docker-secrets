// Package syncer writes opted-in Vaultwarden items to the secret directory and
// restarts consumers when a secret changes.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/backend"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

// RestartLabel marks containers that must be restarted when secrets of the
// listed targets change. Targets are comma-separated and either a group name
// or "<group>/<item>" (just "<item>" for items without a group).
const RestartLabel = "secret-sync.restart"

// VaultSource returns a fresh vault snapshot.
type VaultSource interface {
	Fetch(ctx context.Context) (*bw.Vault, error)
}

// Containers is the subset of the Docker API used for restarts.
type Containers interface {
	ListContainers(ctx context.Context, label string) ([]docker.Container, error)
	RestartContainer(ctx context.Context, id string) error
}

// Syncer performs synchronization runs.
type Syncer struct {
	Cfg        *config.Config
	Vault      VaultSource
	Files      *backend.File
	Containers Containers // may be nil if Docker is not available

	trigger chan struct{}
	healthy atomic.Bool
	runMu   sync.Mutex
}

// New creates a Syncer.
func New(cfg *config.Config, v VaultSource, c Containers) *Syncer {
	return &Syncer{Cfg: cfg, Vault: v, Files: &backend.File{Dir: cfg.SecretDir}, Containers: c,
		trigger: make(chan struct{}, 1)}
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

	var keep []string
	targets := map[string]bool{}
	for _, it := range items {
		for _, f := range it.Files {
			rel := it.RelPath(f.Name)
			keep = append(keep, rel)
			changed, err := s.syncFile(rel, f.Value)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			}
			if changed {
				if it.Group != "" {
					targets[it.Group] = true
					targets[it.Group+"/"+it.Dir] = true
				} else {
					targets[it.Dir] = true
				}
			}
		}
	}

	// Pruning only runs on a clean pass, so a transient problem or a naming
	// conflict never deletes files.
	if s.Cfg.PruneEnabled() {
		if len(errs) > 0 {
			slog.Warn("not removing stale files because of errors in this run")
		}
		s.prune(keep, len(errs) == 0)
	}

	if len(targets) > 0 {
		if err := s.restart(ctx, targets); err != nil {
			errs = append(errs, err)
		}
	}

	if err := s.markReady(len(errs) == 0); err != nil {
		errs = append(errs, err)
	}
	s.healthy.Store(len(errs) == 0)
	slog.Info("sync finished", "items", len(items), "errors", len(errs))
	return errors.Join(errs...)
}

// syncFile writes value to rel. It reports true when an existing file was
// changed (a new file never counts as a rotation).
func (s *Syncer) syncFile(rel string, value []byte) (bool, error) {
	cur, exists, err := s.Files.Current(rel)
	if err != nil {
		return false, err
	}
	hash := backend.Hash(value)
	if exists && cur == hash {
		slog.Debug("secret unchanged", "file", rel, "hash", short(hash))
		return false, nil
	}
	if exists && !s.Cfg.RotateEnabled() {
		slog.Warn("secret changed in vault but rotation is disabled; keeping existing file",
			"file", rel, "hash", short(hash))
		return false, nil
	}
	if err := s.Files.Write(rel, value, s.Cfg.Mode); err != nil {
		return false, err
	}
	if exists {
		slog.Info("secret rotated", "file", rel, "hash", short(hash))
	} else {
		slog.Info("secret created", "file", rel, "hash", short(hash))
	}
	return exists, nil
}

func (s *Syncer) restart(ctx context.Context, targets map[string]bool) error {
	if s.Containers == nil {
		slog.Warn("secrets rotated but Docker is not available; cannot restart consumers")
		return nil
	}
	list, err := s.Containers.ListContainers(ctx, RestartLabel)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	var errs []error
	for _, c := range list {
		if !matches(c.Labels[RestartLabel], targets) {
			continue
		}
		name := strings.TrimPrefix(first(c.Names), "/")
		if err := s.Containers.RestartContainer(ctx, c.ID); err != nil {
			errs = append(errs, fmt.Errorf("restart %s: %w", name, err))
			continue
		}
		slog.Info("container restarted", "container", name)
	}
	return errors.Join(errs...)
}

func matches(label string, targets map[string]bool) bool {
	for _, t := range strings.Split(label, ",") {
		if targets[strings.TrimSpace(t)] {
			return true
		}
	}
	return false
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

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
