package config

import "testing"

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte("secrets:\n  - name: db\n    source: {item: postgres}\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Secrets[0]
	if s.Source.Field != "password" || s.Backend != BackendFile || s.Path != "db" {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if s.FileMode != 0o400 || s.OnChange.Action != ActionNone {
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
