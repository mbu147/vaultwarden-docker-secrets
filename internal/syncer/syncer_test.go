package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/backend"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

type fakeVault struct{ password string }

func (f *fakeVault) Fetch(context.Context) (*bw.Vault, error) {
	it := bw.Item{ID: "1", Name: "db"}
	it.Login = &struct {
		Username string `json:"username"`
		Password string `json:"password"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	}{Password: f.password}
	return &bw.Vault{Items: []bw.Item{it}}, nil
}

type fakeContainers struct{ restarted, killed []string }

func (f *fakeContainers) ListContainers(context.Context, string) ([]docker.Container, error) {
	return []docker.Container{{ID: "labeled", Labels: map[string]string{RestartLabel: "a, db"}}}, nil
}
func (f *fakeContainers) RestartContainer(_ context.Context, id string) error {
	f.restarted = append(f.restarted, id)
	return nil
}
func (f *fakeContainers) KillContainer(_ context.Context, id, _ string) error {
	f.killed = append(f.killed, id)
	return nil
}

func newTest(t *testing.T, rotate bool, action string) (*Syncer, *fakeVault, *fakeContainers, string) {
	t.Helper()
	dir := t.TempDir()
	yml := "secret_dir: " + filepath.ToSlash(dir) + "\n" +
		"ready_file: " + filepath.ToSlash(filepath.Join(dir, "ready")) + "\n" +
		"secrets:\n  - name: db\n    source: {item: db}\n"
	if !rotate {
		yml += "    rotate: false\n"
	}
	yml += "    on_change: {action: " + action + ", containers: [app]}\n"
	cfg, err := config.Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	v := &fakeVault{password: "one"}
	c := &fakeContainers{}
	s := New(cfg, v, map[string]backend.Backend{"file": &backend.File{Dir: dir}}, c)
	return s, v, c, dir
}

func TestGroupField(t *testing.T) {
	for _, tc := range []struct{ value, file string }{
		{"mariadb", "mariadb/db"},
		{"  ", "db"}, // blank field: unchanged
	} {
		dir := t.TempDir()
		cfg, err := config.Parse([]byte("secret_dir: " + filepath.ToSlash(dir) + "\nready_file: " +
			filepath.ToSlash(filepath.Join(dir, "ready")) + "\nsecrets:\n  - name: db\n    source: {item: db}\n"))
		if err != nil {
			t.Fatal(err)
		}
		s := New(cfg, &groupVault{group: tc.value}, map[string]backend.Backend{"file": &backend.File{Dir: dir}}, &fakeContainers{})
		if err := s.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(tc.file))); string(got) != "pw" {
			t.Errorf("group %q: %s has %q", tc.value, tc.file, got)
		}
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Parse([]byte("secret_dir: " + filepath.ToSlash(dir) + "\nready_file: " +
		filepath.ToSlash(filepath.Join(dir, "ready")) + "\nsecrets:\n  - name: db\n    source: {item: db}\n"))
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "foreign")
	if err := os.WriteFile(foreign, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := &groupVault{group: "mariadb"}
	s := New(cfg, v, map[string]backend.Backend{"file": &backend.File{Dir: dir}}, &fakeContainers{})
	ctx := context.Background()
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// Moving the item to another group removes the old file and its empty dir.
	v.group = "other"
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mariadb")); err == nil {
		t.Error("old group directory should be removed")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "other", "db")); string(got) != "pw" {
		t.Errorf("new file missing: %q", got)
	}

	// Deleting the item in the vault removes the file; foreign files stay.
	v.missing = true
	if err := s.SyncOnce(ctx); err == nil {
		t.Fatal("expected not-found error")
	}
	if _, err := os.Stat(filepath.Join(dir, "other", "db")); err == nil {
		t.Error("file of deleted item should be removed")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("foreign file must not be removed")
	}
}

func TestPruneDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Parse([]byte("prune: false\nsecret_dir: " + filepath.ToSlash(dir) + "\nready_file: " +
		filepath.ToSlash(filepath.Join(dir, "ready")) + "\nsecrets:\n  - name: db\n    source: {item: db}\n"))
	if err != nil {
		t.Fatal(err)
	}
	v := &groupVault{}
	s := New(cfg, v, map[string]backend.Backend{"file": &backend.File{Dir: dir}}, &fakeContainers{})
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	v.missing = true
	_ = s.SyncOnce(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "db")); err != nil {
		t.Error("prune disabled: file must stay")
	}
}

type groupVault struct {
	group   string
	missing bool
}

func (g *groupVault) Fetch(context.Context) (*bw.Vault, error) {
	if g.missing {
		return &bw.Vault{}, nil
	}
	it := bw.Item{ID: "1", Name: "db"}
	it.Login = &struct {
		Username string `json:"username"`
		Password string `json:"password"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	}{Password: "pw"}
	it.Fields = []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{{Name: "group", Value: g.group}}
	return &bw.Vault{Items: []bw.Item{it}}, nil
}

func TestRotation(t *testing.T) {
	s, v, c, dir := newTest(t, true, "restart")
	ctx := context.Background()

	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "db")); string(got) != "one" {
		t.Fatalf("got %q", got)
	}
	if len(c.restarted) != 0 {
		t.Fatal("initial creation must not restart consumers")
	}

	v.password = "two"
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "db")); string(got) != "two" {
		t.Fatalf("got %q", got)
	}
	if len(c.restarted) != 2 { // "app" from config + "labeled" from label
		t.Fatalf("restarted = %v", c.restarted)
	}
}

func TestRotationDisabled(t *testing.T) {
	s, v, c, dir := newTest(t, false, "restart")
	ctx := context.Background()
	_ = s.SyncOnce(ctx)
	v.password = "two"
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "db")); string(got) != "one" {
		t.Fatalf("value must not change, got %q", got)
	}
	if len(c.restarted) != 0 {
		t.Fatal("no restart expected")
	}
}

func TestSignal(t *testing.T) {
	s, v, c, _ := newTest(t, true, "signal")
	_ = s.SyncOnce(context.Background())
	v.password = "two"
	_ = s.SyncOnce(context.Background())
	if len(c.killed) != 2 {
		t.Fatalf("killed = %v", c.killed)
	}
}
