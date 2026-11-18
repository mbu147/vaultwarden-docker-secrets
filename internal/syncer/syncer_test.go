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
	list []docker.Container
}

func (f *fakeContainers) ListContainers(context.Context, string) ([]docker.Container, error) {
	return f.list, nil
}

var on = fld{"sync_enabled", "true"}

func setup(t *testing.T, items ...bw.Item) (*Syncer, *fakeVault, *fakeContainers) {
	t.Helper()
	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReadyFile = filepath.Join(t.TempDir(), "ready")
	v := &fakeVault{items: items}
	c := &fakeContainers{}
	return New(cfg, v, c), v, c
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestHealthAndReady(t *testing.T) {
	s, v, _ := setup(t, mkItem("db", "one", on))
	if err := s.SyncOnce(context.Background()); err != nil || !s.Healthy() {
		t.Fatalf("sync: %v healthy=%v", err, s.Healthy())
	}
	if _, err := os.Stat(s.Cfg.ReadyFile); err != nil {
		t.Fatal("ready file missing")
	}
	v.err = os.ErrDeadlineExceeded
	if err := s.SyncOnce(context.Background()); err == nil || s.Healthy() {
		t.Fatal("fetch failure must be reported")
	}
}
