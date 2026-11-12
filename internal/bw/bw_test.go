package bw

import "testing"

func testVault() *Vault {
	mk := func(id, name, folder string, cols []string, pw string) Item {
		it := Item{ID: id, Name: name, FolderID: folder, CollectionIDs: cols, Notes: "n-" + id}
		it.Login = &struct {
			Username string `json:"username"`
			Password string `json:"password"`
			URIs     []struct {
				URI string `json:"uri"`
			} `json:"uris"`
		}{Username: "u-" + id, Password: pw}
		it.Fields = []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{{Name: "token", Value: "t-" + id}}
		return it
	}
	return &Vault{
		Items: []Item{
			mk("1", "db", "f1", []string{"c1"}, "pw1"),
			mk("2", "db", "f2", nil, "pw2"),
			mk("3", "api", "", nil, ""),
		},
		Folders:     map[string]string{"f1": "prod", "f2": "dev"},
		Collections: map[string]string{"c1": "team"},
	}
}

func TestResolve(t *testing.T) {
	v := testVault()
	tests := []struct {
		name                            string
		item, folder, collection, field string
		want                            string
		wantErr                         bool
	}{
		{"by folder", "db", "prod", "", "password", "pw1", false},
		{"by collection", "db", "", "team", "password", "pw1", false},
		{"by id", "2", "", "", "username", "u-2", false},
		{"notes", "2", "", "", "notes", "n-2", false},
		{"custom", "1", "", "", "custom:token", "t-1", false},
		{"ambiguous", "db", "", "", "password", "", true},
		{"missing", "nope", "", "", "password", "", true},
		{"empty value", "api", "", "", "password", "", true},
		{"bad field", "1", "", "", "totp", "", true},
		{"missing custom", "1", "", "", "custom:x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Resolve(tt.item, tt.folder, tt.collection, tt.field)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
