package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/config"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

type fld = struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func mkItem(name, pw string, fields ...fld) bw.Item {
	it := bw.Item{ID: name, Name: name, Fields: fields}
	it.Login = &struct {
		Username string `json:"username"`
		Password string `json:"password"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	}{Password: pw}
	return it
}

type fakeVault struct {
	items []bw.Item
	err   error
}

func (f *fakeVault) Fetch(context.Context) (*bw.Vault, error) {
	return &bw.Vault{Items: f.items}, f.err
}

type fakeContainers struct {
	list      []docker.Container
	restarted []string
}

func (f *fakeContainers) ListContainers(context.Context, string) ([]docker.Container, error) {
	return f.list, nil
}

func (f *fakeContainers) RestartContainer(_ context.Context, id string) error {
	f.restarted = append(f.restarted, id)
	return nil
}

var on = fld{"sync_enabled", "true"}

func setup(t *testing.T, items ...bw.Item) (*Syncer, *fakeVault, *fakeContainers, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SecretDir = dir
	cfg.ReadyFile = filepath.Join(t.TempDir(), "ready")
	v := &fakeVault{items: items}
	c := &fakeContainers{list: []docker.Container{
		{ID: "db", Names: []string{"/db"}, Labels: map[string]string{RestartLabel: "mariadb"}},
		{ID: "other", Names: []string{"/other"}, Labels: map[string]string{RestartLabel: "web, redis/cache"}},
	}}
	s := New(cfg, v, c)
	return s, v, c, dir
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestSyncCreateAndRotate(t *testing.T) {
	s, v, c, dir := setup(t, mkItem("root pw", "one", on, fld{"group", "mariadb"}), mkItem("ignored", "x"))
	ctx := context.Background()
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "mariadb", "root_pw", "password")
	if read(t, p) != "one" || read(t, filepath.Join(dir, "ignored", "password")) != "<missing>" {
		t.Fatal("unexpected files after first sync")
	}
	if len(c.restarted) != 0 || !s.Healthy() {
		t.Fatalf("creation must not restart: %v", c.restarted)
	}
	if _, err := os.Stat(s.Cfg.ReadyFile); err != nil {
		t.Fatal("ready file missing")
	}

	v.items = []bw.Item{mkItem("root pw", "two", on, fld{"group", "mariadb"})}
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if read(t, p) != "two" || len(c.restarted) != 1 || c.restarted[0] != "db" {
		t.Fatalf("rotation failed: %q %v", read(t, p), c.restarted)
	}
}

func TestRestartByItemTarget(t *testing.T) {
	s, v, c, _ := setup(t, mkItem("cache", "a", on, fld{"group", "redis"}))
	ctx := context.Background()
	_ = s.SyncOnce(ctx)
	v.items = []bw.Item{mkItem("cache", "b", on, fld{"group", "redis"})}
	_ = s.SyncOnce(ctx)
	if len(c.restarted) != 1 || c.restarted[0] != "other" {
		t.Fatalf("restarted: %v", c.restarted)
	}
}

func TestRotateDisabled(t *testing.T) {
	s, v, c, dir := setup(t, mkItem("db", "one", on))
	f := false
	s.Cfg.Rotate = &f
	ctx := context.Background()
	_ = s.SyncOnce(ctx)
	v.items = []bw.Item{mkItem("db", "two", on)}
	_ = s.SyncOnce(ctx)
	if read(t, filepath.Join(dir, "db", "password")) != "one" || len(c.restarted) != 0 {
		t.Fatal("existing secret must be kept when rotate is false")
	}
}

func TestPrune(t *testing.T) {
	s, v, _, dir := setup(t, mkItem("db", "one", on, fld{"group", "g"}), mkItem("keep", "k", on))
	ctx := context.Background()
	_ = s.SyncOnce(ctx)
	foreign := filepath.Join(dir, "foreign")
	_ = os.WriteFile(foreign, []byte("x"), 0o600)

	// Group change moves the file; sync_enabled removal deletes it.
	v.items = []bw.Item{mkItem("db", "one", on, fld{"group", "h"}), mkItem("keep", "k")}
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "g", "db", "password")) != "<missing>" ||
		read(t, filepath.Join(dir, "h", "db", "password")) != "one" ||
		read(t, filepath.Join(dir, "keep", "password")) != "<missing>" {
		t.Fatal("stale files not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "g")); err == nil {
		t.Fatal("empty group dir should be removed")
	}
	if read(t, foreign) != "x" {
		t.Fatal("foreign file must never be removed")
	}

	// Item deleted.
	v.items = nil
	_ = s.SyncOnce(ctx)
	if read(t, filepath.Join(dir, "h", "db", "password")) != "<missing>" {
		t.Fatal("deleted item not pruned")
	}
}

func TestPruneSkippedOnErrors(t *testing.T) {
	s, v, _, dir := setup(t, mkItem("db", "one", on))
	ctx := context.Background()
	_ = s.SyncOnce(ctx)
	v.items = []bw.Item{mkItem("bad", "x", on, fld{"group", "../x"})}
	if err := s.SyncOnce(ctx); err == nil {
		t.Fatal("expected error")
	}
	if read(t, filepath.Join(dir, "db", "password")) != "one" {
		t.Fatal("nothing may be removed when the run had errors")
	}
	v.err = os.ErrDeadlineExceeded
	if err := s.SyncOnce(ctx); err == nil || s.Healthy() {
		t.Fatal("fetch failure must be reported")
	}
}

func TestPruneDisabled(t *testing.T) {
	s, v, _, dir := setup(t, mkItem("db", "one", on))
	f := false
	s.Cfg.Prune = &f
	_ = s.SyncOnce(context.Background())
	v.items = nil
	_ = s.SyncOnce(context.Background())
	if read(t, filepath.Join(dir, "db", "password")) != "one" {
		t.Fatal("prune=false must keep files")
	}
}
