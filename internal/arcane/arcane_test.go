package arcane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvironmentsFallback(t *testing.T) {
	for path, newAPI := range map[string]bool{"/api/agents": true, "/api/environments": false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"7","name":"srv"}]}`))
		}))
		envs, err := New(srv.URL, "k").Environments(context.Background())
		srv.Close()
		if err != nil || len(envs) != 1 || envs[0].ID != "7" || envs[0].Name != "srv" {
			t.Fatalf("newAPI=%v envs=%v err=%v", newAPI, envs, err)
		}
	}
}

func TestClient(t *testing.T) {
	var gotMethod, gotPath, gotKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotKey = r.Method, r.URL.Path, r.Header.Get("X-Api-Key")
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"1","key":"A","isSecret":true,"allEnvironments":true}]}`))
			return
		}
		if r.URL.Path == "/api/variables/bad" {
			http.Error(w, "nope", http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "k")
	ctx := context.Background()
	vars, err := c.List(ctx)
	if err != nil || len(vars) != 1 || vars[0].ID != "1" || !vars[0].AllEnvironments || gotKey != "k" {
		t.Fatalf("list: %v %v key=%q", vars, err, gotKey)
	}
	if err := c.Create(ctx, Variable{Key: "B", Value: "v", IsSecret: true, AllEnvironments: true}); err != nil ||
		gotMethod != "POST" || gotPath != "/api/variables" || gotBody["key"] != "B" || gotBody["isSecret"] != true {
		t.Fatalf("create: %v %s %s %v", err, gotMethod, gotPath, gotBody)
	}
	if err := c.Update(ctx, "1", Variable{Value: "w", IsSecret: true, AllEnvironments: true}); err != nil ||
		gotMethod != "PATCH" || gotPath != "/api/variables/1" || gotBody["value"] != "w" {
		t.Fatalf("update: %v %s %s %v", err, gotMethod, gotPath, gotBody)
	}
	if err := c.Update(ctx, "bad", Variable{}); err == nil {
		t.Fatal("expected error on 409")
	}
}
