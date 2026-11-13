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
	if c.Interval != 5*time.Minute || c.SecretDir != "/secrets" || c.Mode != 0o444 {
		t.Fatalf("bad defaults: %+v", c)
	}
	if !c.RotateEnabled() || !c.PruneEnabled() {
		t.Fatal("rotate and prune must default to true")
	}
}

func TestFileAndEnv(t *testing.T) {
	t.Setenv("SECRET_SYNC_ROTATE", "false")
	t.Setenv("SECRET_SYNC_FILE_MODE", "440")
	c, err := Parse([]byte("interval: 1m\nrotate: true\nprune: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != time.Minute || c.RotateEnabled() || c.PruneEnabled() || c.Mode != os.FileMode(0o440) {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestInvalid(t *testing.T) {
	for name, doc := range map[string]string{
		"old secrets key": "secrets:\n  - name: x\n",
		"short interval":  "interval: 1s\n",
		"webhook token":   "webhook:\n  listen: ':8080'\n",
		"bad mode":        "file_mode: rw\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
