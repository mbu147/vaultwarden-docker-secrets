package config

import (
	"os"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != 5*time.Minute || c.EnvFile.Name != ".secrets" || c.EnvFile.FileMode != 0o600 {
		t.Fatalf("bad defaults: %+v", c)
	}
	if c.ArcaneEnabled() {
		t.Fatal("arcane must be off without url")
	}
}

func TestFileAndEnv(t *testing.T) {
	t.Setenv("SECRET_SYNC_ENV_FILE_MODE", "440")
	t.Setenv("ARCANE_ENABLED", "false")
	t.Setenv("SECRET_SYNC_ENV_FILE_ENABLED", "false")
	c, err := Parse([]byte("interval: 1m\nenv_file:\n  dir: /p\narcane:\n  url: https://a.example\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.EnvFileEnabled() {
		t.Fatal("env file must be off")
	}
	if c.Interval != time.Minute || c.ArcaneEnabled() || c.EnvFile.FileMode != os.FileMode(0o440) {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestInvalid(t *testing.T) {
	for name, doc := range map[string]string{
		"old secrets key": "secrets:\n  - name: x\n",
		"short interval":  "interval: 1s\n",
		"webhook token":   "webhook:\n  listen: ':8080'\n",
		"bad mode":        "env_file:\n  mode: rw\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
