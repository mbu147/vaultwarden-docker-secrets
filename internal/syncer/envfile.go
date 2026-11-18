package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/backend"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
)

const (
	// EnvLabel marks containers whose Compose project gets a dotenv file. The
	// value is a comma-separated list of targets: a group, "<group>/<item>", or
	// "<item>" for items without a group.
	EnvLabel = "secret-sync.env"
	// workDirLabel is set by Docker Compose on every container.
	workDirLabel = "com.docker.compose.project.working_dir"
)

// envVars holds variable name -> value and reports conflicting duplicates.
type envVars map[string]string

func (e envVars) add(it bw.SyncedItem, owner string) error {
	var errs []string
	for _, f := range it.Files {
		name := it.EnvName(f.Name)
		if old, ok := e[name]; ok && old != string(f.Value) {
			errs = append(errs, name)
			continue
		}
		e[name] = string(f.Value)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s: conflicting values for variable(s) %s", owner, strings.Join(errs, ", "))
	}
	return nil
}

// syncEnvFiles writes <working_dir>/<env_file.name> for every Compose project
// that has containers labeled EnvLabel.
func (s *Syncer) syncEnvFiles(ctx context.Context, items []bw.SyncedItem) []error {
	base := s.Cfg.EnvFile.Dir
	if !s.Cfg.EnvFileEnabled() {
		return nil
	}
	if s.Containers == nil {
		slog.Warn("env_file.dir is set but Docker is not available; cannot find Compose projects")
		return nil
	}
	list, err := s.Containers.ListContainers(ctx, EnvLabel)
	if err != nil {
		return []error{fmt.Errorf("list containers: %w", err)}
	}

	var errs []error
	projects := map[string]envVars{}
	for _, c := range list {
		label, ok := c.Labels[EnvLabel]
		if !ok {
			continue
		}
		name := strings.TrimPrefix(first(c.Names), "/")
		dir, err := projectDir(base, c.Labels[workDirLabel])
		if err != nil {
			errs = append(errs, fmt.Errorf("container %s: %w", name, err))
			continue
		}
		vars := projects[dir]
		if vars == nil {
			vars = envVars{}
			projects[dir] = vars
		}
		targets := map[string]bool{}
		for _, t := range strings.Split(label, ",") {
			if t = strings.TrimSpace(t); t != "" {
				targets[t] = true
			}
		}
		for _, it := range items {
			if it.SkipEnvFile {
				continue
			}
			for _, t := range it.Targets() {
				if targets[t] {
					if err := vars.add(it, "container "+name); err != nil {
						errs = append(errs, err)
					}
					break
				}
			}
		}
	}

	for dir, vars := range projects {
		if err := s.writeEnvFile(dir, vars); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// projectDir validates that the working directory reported by Compose lies
// below base, so the label cannot be used to write elsewhere.
func projectDir(base, workDir string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("missing label %s", workDirLabel)
	}
	if !filepath.IsAbs(workDir) {
		return "", fmt.Errorf("working dir %q is not absolute", workDir)
	}
	dir := filepath.Clean(workDir)
	rel, err := filepath.Rel(filepath.Clean(base), dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working dir %q is outside env_file.dir", workDir)
	}
	return dir, nil
}

func (s *Syncer) writeEnvFile(dir string, vars envVars) error {
	name := s.Cfg.EnvFile.Name
	path := filepath.Join(dir, name)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file, refusing to write", path)
	}
	content := renderEnv(vars)
	f := &backend.File{Dir: dir}
	cur, exists, err := f.Current(name)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	hash := backend.Hash(content)
	if exists && cur == hash {
		slog.Debug("env file unchanged", "file", path, "variables", sortedNames(vars), "hash", short(hash))
		return nil
	}
	if err := f.Write(name, content, s.Cfg.EnvFile.FileMode); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	slog.Info("env file written; recreate the project to apply it", "file", path,
		"variables", sortedNames(vars), "hash", short(hash))
	return nil
}

func sortedNames(vars envVars) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderEnv renders sorted KEY=value lines in Compose dotenv syntax.
func renderEnv(vars envVars) []byte {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + quoteEnv(vars[k]) + "\n")
	}
	return []byte(b.String())
}

// quoteEnv single-quotes a value (literal: no interpolation, no escapes). A
// value containing a single quote falls back to double quotes with escapes.
func quoteEnv(v string) string {
	if !strings.Contains(v, "'") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "$$", "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(v) + `"`
}
