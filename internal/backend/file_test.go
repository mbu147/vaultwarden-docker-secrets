package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileWriteKeepsInode(t *testing.T) {
	dir := t.TempDir()
	f := &File{Dir: dir}

	if _, exists, _ := f.Current("g/db/password"); exists {
		t.Fatal("should not exist yet")
	}
	if err := f.Write("g/db/password", []byte("one"), 0o400); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "g", "db", "password")
	before, _ := os.Stat(p)

	if err := f.Write("g/db/password", []byte("two"), 0o400); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if !os.SameFile(before, after) {
		t.Fatal("file must be rewritten in place")
	}
	h, exists, err := f.Current("g/db/password")
	if err != nil || !exists || h != Hash([]byte("two")) {
		t.Fatalf("unexpected current state: %q %v %v", h, exists, err)
	}
}

func TestFilePathEscape(t *testing.T) {
	f := &File{Dir: t.TempDir()}
	if err := f.Write("../../etc/passwd", []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "etc", "passwd")); err != nil {
		t.Fatalf("path should be confined to secret dir: %v", err)
	}
}
