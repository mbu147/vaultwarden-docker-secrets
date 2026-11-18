// Package backend writes secret files below the secret directory.
package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// File writes secrets as files below Dir.
//
// Files are rewritten in place (same inode) instead of being replaced via
// rename. Compose bind-mounts a secret file directly, and a rename would leave
// running containers looking at the old inode.
type File struct {
	Dir string
}

// Hash returns the hex SHA-256 of a secret value.
func Hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (f *File) path(rel string) (string, error) {
	p := filepath.Join(f.Dir, filepath.Clean("/"+filepath.FromSlash(rel)))
	if !strings.HasPrefix(p, filepath.Clean(f.Dir)) {
		return "", fmt.Errorf("path %q escapes base directory", rel)
	}
	return p, nil
}

// Current returns the hash of the file at rel (slash separated, relative to
// Dir), and whether it exists.
func (f *File) Current(rel string) (string, bool, error) {
	p, err := f.path(rel)
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

// Write stores value at rel with the given mode, creating directories.
func (f *File) Write(rel string, value []byte, mode os.FileMode) error {
	p, err := f.path(rel)
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
	return os.Chmod(p, mode)
}
