package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
)

func TestFileWriteKeepsInode(t *testing.T) {
	dir := t.TempDir()
	f := &File{Dir: dir}
	s := config.Secret{Name: "db", Path: "db", FileMode: 0o400}
	ctx := context.Background()

	if _, exists, _ := f.Current(ctx, s); exists {
		t.Fatal("should not exist yet")
	}
	if err := f.Write(ctx, s, []byte("one")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "db")
	before, _ := os.Stat(p)

	if err := f.Write(ctx, s, []byte("two")); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if !os.SameFile(before, after) {
		t.Fatal("file must be rewritten in place")
	}
	h, exists, err := f.Current(ctx, s)
	if err != nil || !exists || h != Hash([]byte("two")) {
		t.Fatalf("unexpected current state: %q %v %v", h, exists, err)
	}
}

func TestFilePathEscape(t *testing.T) {
	f := &File{Dir: t.TempDir()}
	s := config.Secret{Name: "x", Path: "../../etc/passwd", FileMode: 0o400}
	if err := f.Write(context.Background(), s, []byte("x")); err != nil {
		t.Fatal(err) // cleaned into the secret dir, must not error nor escape
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "etc", "passwd")); err != nil {
		t.Fatalf("path should be confined to secret dir: %v", err)
	}
}

func TestReplaceSecretRefs(t *testing.T) {
	spec := map[string]any{"TaskTemplate": map[string]any{"ContainerSpec": map[string]any{
		"Secrets": []any{
			map[string]any{"SecretID": "old", "SecretName": "db_vold", "File": map[string]any{"Name": "db"}},
			map[string]any{"SecretID": "other", "SecretName": "other"},
		}}}}
	if !replaceSecretRefs(spec, map[string]bool{"old": true}, "new", "db_vnew") {
		t.Fatal("expected change")
	}
	refs := spec["TaskTemplate"].(map[string]any)["ContainerSpec"].(map[string]any)["Secrets"].([]any)
	first := refs[0].(map[string]any)
	if first["SecretID"] != "new" || first["File"].(map[string]any)["Name"] != "db" {
		t.Fatalf("bad rewrite: %v", first)
	}
	if refs[1].(map[string]any)["SecretID"] != "other" {
		t.Fatal("unrelated secret modified")
	}
}
