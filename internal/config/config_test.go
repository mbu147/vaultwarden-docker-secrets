package config

import (
	"testing"
	"time"
)

func TestEnvOverridesAndEmptyFile(t *testing.T) {
	t.Setenv("SECRET_SYNC_DISCOVERY_LABELS", "true")
	t.Setenv("SECRET_SYNC_INTERVAL", "30s")
	t.Setenv("SECRET_SYNC_ROTATE", "false")
	t.Setenv("DOCKER_HOST", "tcp://docker-proxy:2375")
	t.Setenv("BW_SERVER", "https://vault.example.com")

	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Discovery.Labels || c.Interval != 30*time.Second || c.RotateEnabled(Secret{}) ||
		c.Docker.Host != "tcp://docker-proxy:2375" || c.Bitwarden.Server != "https://vault.example.com" {
		t.Fatalf("env not applied: %+v", c)
	}

	// Env wins over the file.
	c, err = Parse([]byte("interval: 1h\ndiscovery:\n  labels: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != 30*time.Second || !c.Discovery.Labels {
		t.Fatalf("env should override file: %+v", c)
	}
}

func TestInvalidEnv(t *testing.T) {
	t.Setenv("SECRET_SYNC_ROTATE", "maybe")
	if _, err := Parse(nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte("secrets:\n  - name: db\n    source: {item: postgres}\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Secrets[0]
	if s.Source.Field != "password" || s.Backend != BackendFile || s.Path != "db" {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if s.FileMode != 0o444 || s.OnChange.Action != ActionNone {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if !c.RotateEnabled(s) {
		t.Fatal("rotation should default to enabled")
	}
}

func TestRotateOverride(t *testing.T) {
	c, err := Parse([]byte("rotate: false\nsecrets:\n  - name: a\n    source: {item: x}\n  - name: b\n    source: {item: y}\n    rotate: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.RotateEnabled(c.Secrets[0]) || !c.RotateEnabled(c.Secrets[1]) {
		t.Fatal("rotate override not respected")
	}
}

func TestDiscoveryOnlyConfig(t *testing.T) {
	if _, err := Parse([]byte("discovery: {labels: true}\n")); err != nil {
		t.Fatalf("labels discovery should not require config secrets: %v", err)
	}
}

func TestParseErrors(t *testing.T) {
	bad := map[string]string{
		"no secrets":   "interval: 1m\n",
		"bad name":     "secrets:\n  - name: ../x\n    source: {item: a}\n",
		"duplicate":    "secrets:\n  - name: a\n    source: {item: a}\n  - name: a\n    source: {item: b}\n",
		"no item":      "secrets:\n  - name: a\n",
		"bad backend":  "secrets:\n  - name: a\n    source: {item: a}\n    backend: x\n",
		"bad action":   "secrets:\n  - name: a\n    source: {item: a}\n    on_change: {action: boom}\n",
		"bad mode":     "secrets:\n  - name: a\n    source: {item: a}\n    mode: \"9\"\n",
		"short poll":   "interval: 1s\nsecrets:\n  - name: a\n    source: {item: a}\n",
		"hook w/o tok": "webhook: {listen: ':8080'}\nsecrets:\n  - name: a\n    source: {item: a}\n",
		"unknown key":  "foo: 1\nsecrets:\n  - name: a\n    source: {item: a}\n",
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
