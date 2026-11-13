package syncer

import (
	"strings"
	"testing"

	"github.com/mbu147/vaultwarden-docker-secrets/internal/docker"
)

func TestDiscoverSecrets(t *testing.T) {
	containers := []docker.Container{
		{ID: "b", Names: []string{"/db"}, Labels: map[string]string{
			"secret-sync.db_password.item":  "postgres",
			"secret-sync.db_password.mode":  "0444",
			"secret-sync.db_password.field": "password",
			"secret-sync.restart":           "ignored",
			"unrelated":                     "x",
		}},
		{ID: "a", Names: []string{"/worker"}, Labels: map[string]string{
			"secret-sync.db_password.item":  "postgres",
			"secret-sync.db_password.mode":  "0444",
			"secret-sync.db_password.field": "password",
			"secret-sync.api.token.item":    "api",
			"secret-sync.api.token.action":  "signal",
			"secret-sync.api.token.rotate":  "false",
		}},
	}
	got, errs := discoverSecrets(containers, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("got %d secrets: %+v", len(got), got)
	}
	by := map[string]int{}
	for i, s := range got {
		by[s.Name] = i
	}

	db := got[by["db_password"]]
	if len(db.OnChange.Containers) != 2 || db.OnChange.Action != "restart" || db.FileMode != 0o444 {
		t.Fatalf("db_password: %+v", db)
	}
	tok := got[by["api.token"]] // names may contain dots
	if tok.OnChange.Action != "signal" || tok.OnChange.Signal != "SIGHUP" ||
		tok.Rotate == nil || *tok.Rotate || len(tok.OnChange.Containers) != 1 {
		t.Fatalf("api.token: %+v", tok)
	}
}

func TestDiscoverConfigWins(t *testing.T) {
	c := []docker.Container{{ID: "a", Labels: map[string]string{"secret-sync.db.item": "x"}}}
	got, errs := discoverSecrets(c, map[string]bool{"db": true})
	if len(got) != 0 || len(errs) != 0 {
		t.Fatalf("configured secret must not be overridden: %v %v", got, errs)
	}
}

func TestDiscoverErrors(t *testing.T) {
	tests := map[string][]docker.Container{
		"missing item": {{ID: "a", Labels: map[string]string{"secret-sync.db.mode": "0444"}}},
		"unknown key":  {{ID: "a", Labels: map[string]string{"secret-sync.db.item": "x", "secret-sync.db.foo": "1"}}},
		"bad rotate":   {{ID: "a", Labels: map[string]string{"secret-sync.db.item": "x", "secret-sync.db.rotate": "maybe"}}},
		"conflict": {
			{ID: "a", Labels: map[string]string{"secret-sync.db.item": "x"}},
			{ID: "b", Labels: map[string]string{"secret-sync.db.item": "y"}},
		},
	}
	for name, c := range tests {
		got, errs := discoverSecrets(c, nil)
		if len(errs) == 0 || len(got) != 0 {
			t.Errorf("%s: expected error and no secret, got %v / %v", name, got, errs)
		}
	}
	// A failing secret must not hide valid ones.
	got, errs := discoverSecrets([]docker.Container{{ID: "a", Labels: map[string]string{
		"secret-sync.bad.mode": "0444", "secret-sync.ok.item": "x",
	}}}, nil)
	if len(got) != 1 || got[0].Name != "ok" || len(errs) != 1 ||
		!strings.Contains(errs[0].Error(), "bad") {
		t.Fatalf("got %v / %v", got, errs)
	}
}
