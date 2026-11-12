package backend

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
)

// File writes secrets as files below Dir, for Docker Compose "file:" secrets.
//
// Files are rewritten in place (same inode) instead of being replaced via
// rename. Compose bind-mounts a secret file directly, and a rename would leave
// running containers looking at the old inode.
type File struct {
	Dir string
}

func (f *File) path(s config.Secret) (string, error) {
	p := filepath.Join(f.Dir, filepath.Clean("/"+s.Path))
	if !strings.HasPrefix(p, filepath.Clean(f.Dir)) {
		return "", fmt.Errorf("secret %q: path escapes secret_dir", s.Name)
	}
	return p, nil
}

// Current implements Backend.
func (f *File) Current(_ context.Context, s config.Secret) (string, bool, error) {
	p, err := f.path(s)
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Hash(b), true, nil
}

// Write implements Backend.
func (f *File) Write(_ context.Context, s config.Secret, value []byte) error {
	p, err := f.path(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// The file may be read-only (e.g. 0400); make it writable for the update.
	_ = os.Chmod(p, 0o600)
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := fh.Write(value); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	return os.Chmod(p, s.FileMode)
}
