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

	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
)

// manifestName lists the files secret-sync wrote into secret_dir, so that
// stale ones can be removed without touching foreign files.
const manifestName = ".secret-sync-managed"

const (
	originConfig = "config"
	originLabel  = "label"
)

type origSecret struct {
	config.Secret
	origin string
}

type managedFile struct {
	Origin string
	Name   string
	Path   string // relative to secret_dir, slash separated
}

// prune removes files that an earlier run wrote but that are no longer
// produced. keep is what this run still manages. A file that came from a
// container label is only removed while its secret name is still discovered
// (e.g. because its group changed or its item was deleted); labels of stopped
// containers are invisible, so those files are left alone. Nothing is removed
// when label discovery failed.
func (s *Syncer) prune(keep []managedFile, discovered map[string]bool, discoveryOK bool) {
	dir := s.Cfg.SecretDir
	old := readManifest(filepath.Join(dir, manifestName))

	wanted := map[string]bool{}
	next := append([]managedFile(nil), keep...)
	for _, k := range keep {
		wanted[k.Path] = true
	}
	for _, o := range old {
		if wanted[o.Path] {
			continue
		}
		stale := discoveryOK && (o.Origin == originConfig || discovered[o.Name])
		if !stale {
			next = append(next, o)
			continue
		}
		if err := removeManaged(dir, o.Path); err != nil {
			slog.Warn("could not remove stale secret file", "secret", o.Name, "path", o.Path, "error", err)
			next = append(next, o)
			continue
		}
		slog.Info("stale secret file removed", "secret", o.Name, "path", o.Path)
	}

	if err := writeManifest(filepath.Join(dir, manifestName), next); err != nil {
		slog.Warn("could not write prune manifest", "error", err)
	}
}

func removeManaged(dir, rel string) error {
	clean := path.Clean("/" + rel)
	p := filepath.Join(dir, filepath.FromSlash(clean))
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Remove empty group directories, but never secret_dir itself.
	root := filepath.Clean(dir)
	for d := filepath.Dir(p); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // fails while the directory is not empty
			break
		}
	}
	return nil
}

func readManifest(file string) []managedFile {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []managedFile
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) == 3 {
			out = append(out, managedFile{Origin: parts[0], Name: parts[1], Path: parts[2]})
		}
	}
	return out
}

func writeManifest(file string, files []managedFile) error {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var b strings.Builder
	seen := map[string]bool{}
	for _, m := range files {
		if seen[m.Path] {
			continue
		}
		seen[m.Path] = true
		b.WriteString(m.Origin + "\t" + m.Name + "\t" + m.Path + "\n")
	}
	if old, err := os.ReadFile(file); err == nil && string(old) == b.String() {
		return nil
	}
	return os.WriteFile(file, []byte(b.String()), 0o600)
}
