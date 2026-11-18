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
// every synced item. Variables created here are listed in the manifest
// variable manifestKey; the ones that disappear from the vault are deleted,
// but only if prune is set (the vault read was complete).
func (s *Syncer) syncArcane(ctx context.Context, items []bw.SyncedItem, prune bool) []error {
	if s.Variables == nil {
		return nil
	}
	var errs []error
	desired := map[string]desiredVar{}
	var envs []arcane.Environment
	for _, it := range items {
		if it.SkipArcane {
			continue
		}
		agents := it.Agents
		if len(agents) > 0 {
			if envs == nil {
				var err error
				if envs, err = s.Variables.Environments(ctx); err != nil {
					return append(errs, err)
				}
			}
			var err error
			if agents, err = resolveEnvs(agents, envs); err != nil {
				errs = append(errs, fmt.Errorf("arcane item %s: %w", it.Dir, err))
				continue
			}
		}
		for _, f := range it.Files {
			d := desiredVar{key: it.EnvName(f.Name), agents: agents, value: string(f.Value), inject: it.ArcaneInject}
			if old, ok := desired[d.id()]; ok {
				if old.value != d.value {
					errs = append(errs, fmt.Errorf("arcane: conflicting values for variable %s", d.key))
				}
				continue
			}
			desired[d.id()] = d
		}
	}
	if len(errs) > 0 {
		prune = false
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
	failed := map[string]bool{}
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
				failed[id] = true
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
	return append(errs, s.updateManifest(ctx, byID, desired, failed, prune)...)
}

const manifestKey = "SECRET_SYNC_MANAGED"

// updateManifest deletes managed variables that are no longer desired and
// stores the new list of managed variable IDs (separated by ";").
func (s *Syncer) updateManifest(ctx context.Context, byID map[string]arcane.Variable,
	desired map[string]desiredVar, failed map[string]bool, prune bool) []error {
	var errs []error
	man, hasMan := byID[manifestKey+"|"]
	managed := map[string]bool{}
	for _, id := range strings.Split(man.Value, ";") {
		if id != "" {
			managed[id] = true
		}
	}

	next := map[string]bool{}
	for id := range desired {
		if !failed[id] {
			next[id] = true
		}
	}
	for id := range managed {
		if _, ok := desired[id]; ok {
			continue
		}
		cur, found := byID[id]
		switch {
		case !found:
		case !prune:
			next[id] = true
		default:
			if err := s.Variables.Delete(ctx, cur.ID); err != nil {
				errs = append(errs, fmt.Errorf("arcane delete %s: %w", cur.Key, err))
				next[id] = true
				continue
			}
			slog.Info("arcane variable deleted", "key", cur.Key, "agents", agentsLog(cur.EnvironmentIDs))
			delete(s.varHashes, id)
		}
	}

	list := make([]string, 0, len(next))
	for id := range next {
		list = append(list, id)
	}
	sort.Strings(list)
	value := strings.Join(list, ";")
	if hasMan && man.Value == value {
		return errs
	}
	if !hasMan && value == "" {
		return errs
	}
	want := arcane.Variable{Key: manifestKey, Value: value, AllEnvironments: true}
	if hasMan {
		if err := s.Variables.Update(ctx, man.ID, want); err != nil {
			return append(errs, fmt.Errorf("arcane manifest: %w", err))
		}
	} else if err := s.Variables.Create(ctx, want); err != nil {
		return append(errs, fmt.Errorf("arcane manifest: %w", err))
	}
	return errs
}

// resolveEnvs maps environment IDs or names (case-insensitive) to sorted IDs.
func resolveEnvs(refs []string, envs []arcane.Environment) ([]string, error) {
	var out []string
	for _, r := range refs {
		found := ""
		for _, e := range envs {
			if e.ID == r || strings.EqualFold(e.Name, r) {
				found = e.ID
				break
			}
		}
		if found == "" {
			known := make([]string, 0, len(envs))
			for _, e := range envs {
				known = append(known, e.Name+" ("+e.ID+")")
			}
			return nil, fmt.Errorf("unknown agent %q; known: %s", r, strings.Join(known, ", "))
		}
		out = append(out, found)
	}
	sort.Strings(out)
	return out, nil
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
