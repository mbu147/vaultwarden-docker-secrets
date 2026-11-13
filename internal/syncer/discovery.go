package syncer

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

const labelPrefix = "secret-sync."

// discoverSecrets builds secrets from container labels of the form
// secret-sync.<name>.<key>. The restart label (secret-sync.restart) has no key
// segment and is ignored here. Containers that declare a secret become targets
// of its on_change action, which defaults to restart.
//
// Secrets already defined in the config file win over labels. Containers
// declaring the same name must agree on its definition.
func discoverSecrets(containers []docker.Container, configured map[string]bool) ([]config.Secret, []error) {
	sorted := append([]docker.Container(nil), containers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	var errs []error
	var out []config.Secret
	index := map[string]int{}
	failed := map[string]bool{}

	for _, c := range sorted {
		for name, keys := range groupLabels(c.Labels) {
			if configured[name] || failed[name] {
				continue
			}
			sec, err := secretFromLabels(name, keys)
			if err != nil {
				failed[name] = true
				errs = append(errs, fmt.Errorf("container %s: %w", containerName(c), err))
				continue
			}
			if i, ok := index[name]; ok {
				if !sameDefinition(out[i], sec) {
					failed[name] = true
					errs = append(errs, fmt.Errorf("secret %q is declared with conflicting labels (container %s)", name, containerName(c)))
					continue
				}
				out[i].OnChange.Containers = append(out[i].OnChange.Containers, c.ID)
				continue
			}
			sec.OnChange.Containers = []string{c.ID}
			index[name] = len(out)
			out = append(out, sec)
		}
	}

	// Drop secrets that turned out to be conflicting after being added.
	kept := out[:0]
	for _, s := range out {
		if !failed[s.Name] {
			kept = append(kept, s)
		}
	}
	return kept, errs
}

// groupLabels returns key/value pairs per secret name.
func groupLabels(labels map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for k, v := range labels {
		rest, ok := strings.CutPrefix(k, labelPrefix)
		if !ok {
			continue
		}
		// The name may contain dots, so the key is the last segment.
		i := strings.LastIndex(rest, ".")
		if i <= 0 {
			continue
		}
		name, key := rest[:i], rest[i+1:]
		if out[name] == nil {
			out[name] = map[string]string{}
		}
		out[name][key] = v
	}
	return out
}

func secretFromLabels(name string, keys map[string]string) (config.Secret, error) {
	s := config.Secret{Name: name, OnChange: config.OnChange{Action: config.ActionRestart}}
	for k, v := range keys {
		switch k {
		case "item":
			s.Source.Item = v
		case "folder":
			s.Source.Folder = v
		case "collection":
			s.Source.Collection = v
		case "field":
			s.Source.Field = v
		case "path":
			s.Path = v
		case "mode":
			s.Mode = v
		case "action":
			s.OnChange.Action = v
		case "signal":
			s.OnChange.Signal = v
		case "rotate":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return s, fmt.Errorf("secret %q: invalid rotate value %q", name, v)
			}
			s.Rotate = &b
		default:
			return s, fmt.Errorf("secret %q: unknown label key %q", name, k)
		}
	}
	if err := config.NormalizeSecret(&s); err != nil {
		return s, err
	}
	return s, nil
}

func sameDefinition(a, b config.Secret) bool {
	a.OnChange.Containers, b.OnChange.Containers = nil, nil
	return reflect.DeepEqual(a, b)
}

func containerName(c docker.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}
