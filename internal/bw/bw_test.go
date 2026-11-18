package bw

import "testing"

type fld = struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func item(name string, fields ...fld) Item {
	it := Item{ID: name, Name: name, Notes: "note", Fields: fields}
	it.Login = &struct {
		Username string `json:"username"`
		Password string `json:"password"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	}{Username: "user", Password: "pw"}
	return it
}

func files(si SyncedItem) map[string]string {
	m := map[string]string{}
	for _, f := range si.Files {
		m[f.Name] = string(f.Value)
	}
	return m
}

func TestSyncedOptIn(t *testing.T) {
	v := &Vault{Items: []Item{
		item("off"),
		item("explicit-off", fld{"sync_enabled", "false"}),
		item("My DB", fld{"sync_enabled", "true"}, fld{"sync_group", " mariadb "}, fld{"api token", "t"}, fld{"empty", ""}),
		item("plain", fld{"sync_enabled", "TRUE"}, fld{"sync_group", ""}),
	}}
	got, errs := v.Synced()
	if len(errs) != 0 || len(got) != 2 {
		t.Fatalf("got %d items, errs %v", len(got), errs)
	}
	db := got[0]
	if db.Group != "mariadb" || db.Dir != "My_DB" || db.RelPath("password") != "mariadb/My_DB/password" {
		t.Fatalf("bad item: %+v", db)
	}
	f := files(db)
	if f["password"] != "pw" || f["username"] != "user" || f["notes"] != "note" || f["api_token"] != "t" {
		t.Fatalf("bad files: %v", f)
	}
	if _, ok := f["empty"]; ok {
		t.Fatal("empty values must be skipped")
	}
	if _, ok := f["sync_group"]; ok {
		t.Fatal("control fields must not become files")
	}
	if got[1].Group != "" || got[1].RelPath("password") != "plain/password" {
		t.Fatalf("blank group must mean no group: %+v", got[1])
	}
}

func TestSyncedErrors(t *testing.T) {
	on := fld{"sync_enabled", "true"}
	v := &Vault{Items: []Item{
		item("bad", on, fld{"sync_group", "../x"}),
		item("a b", on),
		item("a_b", on),
		item("clash", on, fld{"password", "x"}),
	}}
	got, errs := v.Synced()
	if len(errs) != 3 || len(got) != 1 || got[0].Dir != "a_b" && got[0].Dir != "a b" {
		t.Fatalf("got %+v, errs %v", got, errs)
	}
}

func TestSyncedMultipleGroups(t *testing.T) {
	v := &Vault{Items: []Item{item("pw", fld{"sync_enabled", "true"}, fld{"sync_group", "mariadb, caddy,,mariadb"})}}
	got, errs := v.Synced()
	if len(errs) != 0 || len(got) != 2 || got[0].Group != "mariadb" || got[1].Group != "caddy" {
		t.Fatalf("got %+v, errs %v", got, errs)
	}
}

func TestArcaneFieldsAndNames(t *testing.T) {
	v := &Vault{Items: []Item{
		item("my db", fld{"sync_enabled", "true"}, fld{"sync_arcane_inject_all", "true"}, fld{"sync_arcane_agents", "b, a, b"}, fld{"api-key", "k"}),
		item("every", fld{"sync_enabled", "true"}, fld{"sync_arcane_agents", "x, ALL"}),
		item("plain", fld{"sync_enabled", "true"}),
	}}
	got, errs := v.Synced()
	if len(errs) != 0 || len(got) != 3 {
		t.Fatalf("%v %v", got, errs)
	}
	if !got[0].ArcaneInject || len(got[0].Agents) != 2 || got[0].Agents[0] != "a" || got[0].Agents[1] != "b" {
		t.Fatalf("agents: %+v", got[0])
	}
	if got[1].ArcaneInject || got[1].Agents != nil || got[2].ArcaneInject {
		t.Fatalf("all agents: %+v", got[1])
	}
	for _, f := range got[0].Files {
		if f.Name == "sync_arcane_agents" || f.Name == "sync_arcane_inject_all" {
			t.Fatalf("control field written: %s", f.Name)
		}
	}
	if n := got[0].EnvName("api-key"); n != "MY_DB_API_KEY" {
		t.Fatal(n)
	}
	if n := (SyncedItem{Dir: "1x"}).EnvName("pw"); n != "_1X_PW" {
		t.Fatal(n)
	}
}

func TestOptOutFields(t *testing.T) {
	v := &Vault{Items: []Item{
		item("a", fld{"sync_enabled", "true"}, fld{"sync_env_file", "false"}, fld{"sync_arcane", "true"}),
		item("b", fld{"sync_enabled", "true"}, fld{"sync_arcane", "No"}),
		item("c", fld{"sync_enabled", "true"}, fld{"sync_arcane", ""}),
	}}
	got, errs := v.Synced()
	if len(errs) != 0 || len(got) != 3 {
		t.Fatalf("%v %v", got, errs)
	}
	if !got[0].SkipEnvFile || got[0].SkipArcane || got[1].SkipEnvFile || !got[1].SkipArcane || got[2].SkipArcane {
		t.Fatalf("opt-out: %+v", got)
	}
	for _, g := range got {
		for _, f := range g.Files {
			if f.Name == "sync_arcane" || f.Name == "sync_env_file" {
				t.Fatalf("control field synced: %s", f.Name)
			}
		}
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{"a b/c": "a_b_c", "..x": "__x", "ok-1.2": "ok-1.2", "ä": "_"} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
