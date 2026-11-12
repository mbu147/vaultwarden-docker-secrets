package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

const (
	labelName = "secret-sync.name"
	labelHash = "secret-sync.hash"
)

// Swarm manages versioned Docker Swarm secrets. Swarm secrets are immutable, so
// every change creates "<name>_v<hash8>", points all services that used an
// older version at the new one, and removes the old versions.
type Swarm struct {
	Docker *docker.Client
}

func (w *Swarm) versions(ctx context.Context, name string) ([]docker.Secret, error) {
	list, err := w.Docker.ListSecrets(ctx, labelName+"="+name)
	if err != nil {
		return nil, err
	}
	// Newest first; CreatedAt is RFC 3339 so string order is time order.
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
	return list, nil
}

// Current implements Backend.
func (w *Swarm) Current(ctx context.Context, s config.Secret) (string, bool, error) {
	list, err := w.versions(ctx, s.Name)
	if err != nil || len(list) == 0 {
		return "", false, err
	}
	return list[0].Spec.Labels[labelHash], true, nil
}

// Write implements Backend.
func (w *Swarm) Write(ctx context.Context, s config.Secret, value []byte) error {
	hash := Hash(value)
	existing, err := w.versions(ctx, s.Name)
	if err != nil {
		return err
	}

	newID := ""
	for _, e := range existing {
		if e.Spec.Labels[labelHash] == hash {
			newID = e.ID // value was used before; reuse that version
		}
	}
	newName := fmt.Sprintf("%s_v%s", s.Name, hash[:8])
	if newID == "" {
		newID, err = w.Docker.CreateSecret(ctx, newName,
			map[string]string{labelName: s.Name, labelHash: hash}, value)
		if err != nil {
			return fmt.Errorf("create secret: %w", err)
		}
	}

	old := map[string]bool{}
	for _, e := range existing {
		if e.ID != newID {
			old[e.ID] = true
		}
	}
	if len(old) == 0 {
		return nil
	}

	if err := w.repoint(ctx, old, newID, newName); err != nil {
		return err
	}
	for id := range old {
		if err := w.Docker.DeleteSecret(ctx, id); err != nil {
			slog.Warn("could not delete old secret version", "secret", s.Name, "error", err)
		}
	}
	return nil
}

// repoint updates every service referencing one of the old secret IDs.
func (w *Swarm) repoint(ctx context.Context, old map[string]bool, newID, newName string) error {
	services, err := w.Docker.ListServices(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, svc := range services {
		if !replaceSecretRefs(svc.Spec, old, newID, newName) {
			continue
		}
		if err := w.Docker.UpdateService(ctx, svc); err != nil {
			errs = append(errs, fmt.Errorf("update service %s: %w", svc.ID, err))
			continue
		}
		slog.Info("service updated with new secret version", "service", serviceName(svc.Spec))
	}
	return errors.Join(errs...)
}

func serviceName(spec map[string]any) string {
	n, _ := spec["Name"].(string)
	return n
}

// replaceSecretRefs rewrites secret references in a service spec and reports
// whether anything changed. The target file name inside the container is kept.
func replaceSecretRefs(spec map[string]any, old map[string]bool, newID, newName string) bool {
	tt, _ := spec["TaskTemplate"].(map[string]any)
	cs, _ := tt["ContainerSpec"].(map[string]any)
	refs, _ := cs["Secrets"].([]any)
	changed := false
	for _, r := range refs {
		ref, _ := r.(map[string]any)
		id, _ := ref["SecretID"].(string)
		if !old[id] {
			continue
		}
		ref["SecretID"] = newID
		ref["SecretName"] = newName
		changed = true
	}
	return changed
}
