package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/arcane"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/bw"
	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

func TestProjectDir(t *testing.T) {
	base := "/app/data/projects"
	for _, ok := range []string{"/app/data/projects/hcwels", "/app/data/projects/a/../b"} {
		if _, err := projectDir(base, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "relative", "/etc", "/app/data/projects", "/app/data/projects/..", "/app/data/projects-evil/x", "/app/data/projects/../x"} {
		if _, err := projectDir(base, bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestQuoteEnv(t *testing.T) {
	cases := map[string]string{
		"plain":   `'plain'`,
		"a$b #c":  `'a$b #c'`,
		"it's":    `"it's"`,
		"it's $x": `"it's $$x"`,
		"q\"'\n":  `"q\"'\n"`,
	}
	for in, want := range cases {
		if got := quoteEnv(in); got != want {
			t.Errorf("quoteEnv(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestEnvFile(t *testing.T) {
	s, v, c := setup(t, mkItem("root pw", "one", on, fld{"sync_group", "mariadb"}), mkItem("other", "x", on))
	base := t.TempDir()
	proj := filepath.Join(base, "hcwels")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	s.Cfg.EnvFile.Dir = base
	s.Cfg.EnvFile.Name = ".secrets"
	s.Cfg.EnvFile.FileMode = 0o600
	c.list = []docker.Container{
		{ID: "a", Names: []string{"/a"}, Labels: map[string]string{EnvLabel: "mariadb", workDirLabel: proj}},
		{ID: "b", Names: []string{"/b"}, Labels: map[string]string{EnvLabel: "other", workDirLabel: proj}},
		{ID: "c", Names: []string{"/c"}, Labels: map[string]string{EnvLabel: "mariadb", workDirLabel: "/etc"}},
		{ID: "d", Names: []string{"/d"}, Labels: map[string]string{workDirLabel: proj}},
	}
	err := s.SyncOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "outside env_file.dir") {
		t.Fatalf("expected outside-dir error, got %v", err)
	}
	got := read(t, filepath.Join(proj, ".secrets"))
	if got != "OTHER_PASSWORD='x'\nROOT_PW_PASSWORD='one'\n" {
		t.Fatalf("env file: %q", got)
	}
	if fi, _ := os.Stat(filepath.Join(proj, ".secrets")); fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("mode %v", fi.Mode())
	}

	// Rotation rewrites the file.
	v.items = []bw.Item{mkItem("root pw", "two", on, fld{"sync_group", "mariadb"}), mkItem("other", "x", on)}
	_ = s.SyncOnce(context.Background())
	if got := read(t, filepath.Join(proj, ".secrets")); !strings.Contains(got, "ROOT_PW_PASSWORD='two'") {
		t.Fatalf("not rotated: %q", got)
	}
}

type fakeVars struct {
	vars    []arcane.Variable
	created []arcane.Variable
	updated []arcane.Variable
	deleted []string
}

func (f *fakeVars) List(context.Context) ([]arcane.Variable, error) { return f.vars, nil }
func (f *fakeVars) Environments(context.Context) ([]arcane.Environment, error) {
	return []arcane.Environment{{ID: "id-a", Name: "A"}, {ID: "id-b", Name: "b"}}, nil
}
func (f *fakeVars) Create(_ context.Context, v arcane.Variable) error {
	if v.Key != manifestKey {
		f.created = append(f.created, v)
	}
	f.vars = append(f.vars, arcane.Variable{ID: "id-" + v.Key + strings.Join(v.EnvironmentIDs, ","), Key: v.Key, Value: manifestValue(v), IsSecret: v.IsSecret,
		AllEnvironments: v.AllEnvironments, EnvironmentIDs: v.EnvironmentIDs, InjectIntoContainers: v.InjectIntoContainers})
	return nil
}
func (f *fakeVars) Update(_ context.Context, id string, v arcane.Variable) error {
	v.ID = id
	for i := range f.vars {
		if f.vars[i].ID == id {
			f.vars[i].Value = manifestValue(v)
		}
	}
	if v.Key != manifestKey {
		f.updated = append(f.updated, v)
	}
	return nil
}
func (f *fakeVars) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	for i := range f.vars {
		if f.vars[i].ID == id {
			f.vars = append(f.vars[:i], f.vars[i+1:]...)
			break
		}
	}
	return nil
}

// manifestValue keeps only the non-secret manifest value, like Arcane does.
func manifestValue(v arcane.Variable) string {
	if v.Key == manifestKey {
		return v.Value
	}
	return ""
}

func TestArcanePrune(t *testing.T) {
	s, v, _ := setup(t, mkItem("db", "one", on), mkItem("old", "x", on))
	fv := &fakeVars{vars: []arcane.Variable{{ID: "foreign", Key: "FOREIGN", AllEnvironments: true}}}
	s.Variables = fv
	ctx := context.Background()
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	v.items = []bw.Item{mkItem("db", "one", on)}
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fv.deleted) != 1 || fv.deleted[0] != "id-OLD_PASSWORD" {
		t.Fatalf("deleted=%v", fv.deleted)
	}
	for _, x := range fv.vars {
		if x.Key == "OLD_PASSWORD" {
			t.Fatal("old variable still present")
		}
	}

	// Removing the last item deletes it too; unmanaged variables stay.
	v.items = nil
	_ = s.SyncOnce(ctx)
	if len(fv.deleted) != 2 || fv.deleted[1] != "id-DB_PASSWORD" {
		t.Fatalf("deleted=%v", fv.deleted)
	}
	for _, x := range fv.vars {
		if x.Key == "FOREIGN" {
			return
		}
	}
	t.Fatal("foreign variable was deleted")
}

func TestArcane(t *testing.T) {
	s, v, _ := setup(t,
		mkItem("db", "one", on, fld{"sync_arcane_agents", "b, a"}, fld{"sync_arcane_inject_all", "true"}),
		mkItem("all", "p", on),
	)
	fv := &fakeVars{}
	s.Variables = fv
	ctx := context.Background()
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fv.created) != 2 || len(fv.updated) != 0 {
		t.Fatalf("created=%v updated=%v", fv.created, fv.updated)
	}
	for _, c := range fv.created {
		if !c.IsSecret || c.InjectIntoContainers != (c.Key == "DB_PASSWORD") {
			t.Fatalf("flags: %+v", c)
		}
		switch c.Key {
		case "DB_PASSWORD":
			if c.AllEnvironments || strings.Join(c.EnvironmentIDs, ",") != "id-a,id-b" {
				t.Fatalf("scope: %+v", c)
			}
		case "ALL_PASSWORD":
			if !c.AllEnvironments {
				t.Fatalf("scope: %+v", c)
			}
		default:
			t.Fatalf("unexpected %s", c.Key)
		}
	}

	// Unchanged: no calls. Changed: only that variable is updated.
	_ = s.SyncOnce(ctx)
	if len(fv.created) != 2 || len(fv.updated) != 0 {
		t.Fatal("unchanged values must not be pushed again")
	}
	v.items = []bw.Item{
		mkItem("db", "two", on, fld{"sync_arcane_agents", "b, a"}, fld{"sync_arcane_inject_all", "true"}),
		mkItem("all", "p", on),
	}
	_ = s.SyncOnce(ctx)
	if len(fv.updated) != 1 || fv.updated[0].Key != "DB_PASSWORD" || fv.updated[0].Value != "two" {
		t.Fatalf("updated=%v", fv.updated)
	}
}
