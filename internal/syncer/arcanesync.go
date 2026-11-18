package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/arcane"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/backend"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
)

type desiredVar struct {
	key    string
	agents []string // empty = all agents
	value  string
	inject bool
}

func (d desiredVar) id() string { return d.key + "|" + strings.Join(d.agents, ",") }

// syncArcane creates or updates Arcane global variables (secret flag set) for
// every synced item. Variables are never deleted, because Arcane has
// no marker that identifies the ones created here.
func (s *Syncer) syncArcane(ctx context.Context, items []bw.SyncedItem) []error {
	if s.Variables == nil {
		return nil
	}
	var errs []error
	desired := map[string]desiredVar{}
	for _, it := range items {
		for _, f := range it.Files {
			d := desiredVar{key: it.EnvName(f.Name), agents: it.Agents, value: string(f.Value), inject: it.ArcaneInject}
			if old, ok := desired[d.id()]; ok {
				if old.value != d.value {
					errs = append(errs, fmt.Errorf("arcane: conflicting values for variable %s", d.key))
				}
				continue
			}
			desired[d.id()] = d
		}
	}
	if len(desired) == 0 {
		return errs
	}

	existing, err := s.Variables.List(ctx)
	if err != nil {
		return append(errs, err)
	}
	byID := map[string]arcane.Variable{}
	for _, v := range existing {
		byID[scopeID(v)] = v
	}

	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		d := desired[id]
		hash := backend.Hash([]byte(d.value))
		want := arcane.Variable{
			Key:                  d.key,
			Value:                d.value,
			IsSecret:             true,
			AllEnvironments:      len(d.agents) == 0,
			InjectIntoContainers: d.inject,
			EnvironmentIDs:       d.agents,
		}
		cur, found := byID[id]
		switch {
		case !found:
			if err := s.Variables.Create(ctx, want); err != nil {
				errs = append(errs, fmt.Errorf("arcane variable %s: %w", d.key, err))
				continue
			}
			slog.Info("arcane variable created", "key", d.key, "agents", agentsLog(d.agents), "hash", short(hash))
		case s.varHashes[id] == hash && cur.IsSecret && cur.InjectIntoContainers == want.InjectIntoContainers:
			slog.Debug("arcane variable unchanged", "key", d.key)
			continue
		default:
			if err := s.Variables.Update(ctx, cur.ID, want); err != nil {
				errs = append(errs, fmt.Errorf("arcane variable %s: %w", d.key, err))
				continue
			}
			slog.Info("arcane variable updated", "key", d.key, "agents", agentsLog(d.agents), "hash", short(hash))
		}
		s.varHashes[id] = hash
	}
	return errs
}

// scopeID identifies a remote variable the same way desiredVar.id does.
func scopeID(v arcane.Variable) string {
	if v.AllEnvironments {
		return v.Key + "|"
	}
	a := append([]string(nil), v.EnvironmentIDs...)
	sort.Strings(a)
	return v.Key + "|" + strings.Join(a, ",")
}

func agentsLog(a []string) string {
	if len(a) == 0 {
		return "all"
	}
	return strings.Join(a, ",")
}
