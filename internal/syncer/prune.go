package syncer

import (
	"bufio"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// manifestName lists the files secret-sync wrote into secret_dir, so that
// stale ones can be removed without touching foreign files.
const manifestName = ".secret-sync-managed"

// prune removes files that an earlier run wrote but that are no longer
// produced, and records the current set in the manifest. With remove=false
// stale files are only kept in the manifest.
func (s *Syncer) prune(keep []string, remove bool) {
	dir := s.Cfg.SecretDir
	manifest := filepath.Join(dir, manifestName)
	wanted := map[string]bool{}
	for _, k := range keep {
		wanted[k] = true
	}
	next := map[string]bool{}
	for k := range wanted {
		next[k] = true
	}
	for _, old := range readManifest(manifest) {
		if wanted[old] {
			continue
		}
		if !remove {
			next[old] = true
			continue
		}
		if err := removeManaged(dir, old); err != nil {
			slog.Warn("could not remove stale secret file", "path", old, "error", err)
			next[old] = true
			continue
		}
		slog.Info("stale secret file removed", "path", old)
	}
	if err := writeManifest(manifest, next); err != nil {
		slog.Warn("could not write prune manifest", "error", err)
	}
}

func removeManaged(dir, rel string) error {
	clean := path.Clean("/" + rel)
	p := filepath.Join(dir, filepath.FromSlash(clean))
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Remove empty item and group directories, but never secret_dir itself.
	root := filepath.Clean(dir)
	for d := filepath.Dir(p); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // fails while the directory is not empty
			break
		}
	}
	return nil
}

func readManifest(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func writeManifest(file string, paths map[string]bool) error {
	var b strings.Builder
	keys := sortedKeys(paths)
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "\n")
	}
	if old, err := os.ReadFile(file); err == nil && string(old) == b.String() {
		return nil
	}
	return os.WriteFile(file, []byte(b.String()), 0o600)
}
