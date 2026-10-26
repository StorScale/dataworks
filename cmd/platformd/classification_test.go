package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
)

// A project's protected columns are shown to its members; an editor confirms
// or removes a tag in the catalog, and the project's view follows at once.
func TestClassificationIsShownAndDecided(t *testing.T) {
	var patches []map[string]any
	tags := []map[string]any{{"tagFQN": "Protection.personal", "source": "Classification", "labelType": "Automated", "state": "Suggested"}}
	om := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/tables/name/trino.iceberg.sales.customers"):
			json.NewEncoder(w).Encode(map[string]any{"id": "t-1", "columns": []map[string]any{{"name": "customer_id"}, {"name": "email", "tags": tags}, {"name": "phone", "tags": tags}}})
		case r.Method == "PATCH" && r.URL.Path == "/api/v1/tables/t-1":
			var p []map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			patches = append(patches, p...)
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer om.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte("bot-token"), 0o600)
	sales, _ := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"dana","role":"editor"},{"user":"alice","role":"reader"}],"tables":{}}}`))
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: []project.Person{
		{Username: "dana", Role: "editor"}, {Username: "alice", Role: "reader"}}}}}, specs: map[string]*project.Project{"sales": sales}}}
	files := kept{}
	written, _ := json.Marshal(project.Classification{Project: "sales", At: 1, Columns: []project.ProtectedColumn{
		{Table: "customers", Column: "customer_id", Tag: "personal", State: "confirmed", Declared: true},
		{Table: "customers", Column: "email", Tag: "personal", State: "suggested", Automated: true},
		{Table: "customers", Column: "phone", Tag: "personal", State: "suggested", Automated: true}}})
	files.PutFile(context.Background(), "classification/sales.json", written, "application/json")
	s := &server{cfg: config{Groups: []string{"analysts"}}, store: st, files: files, now: time.Now, catalog: catalog.New(om.URL, tokenFile, "trino"),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), tokenUser: func(raw string) (string, error) { return raw, nil }, sessions: map[string]*session{}}
	h := s.routes()
	call := func(token, method, path, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := call("alice", "GET", "/api/projects/sales/classification", "")
	if code != 200 || len(out["columns"].([]any)) != 3 {
		t.Fatalf("shown: %d %v", code, out)
	}
	if code, _ := call("bob", "GET", "/api/projects/sales/classification", ""); code != 403 {
		t.Errorf("not a member: %d", code)
	}
	if code, _ := call("alice", "POST", "/api/projects/sales/classification", `{"table":"customers","column":"email","tag":"personal","action":"confirm"}`); code != 403 {
		t.Errorf("a reader deciding: %d", code)
	}
	// A table's name has underscores (uploads_customers): it's an identifier, not a project's name.
	if code, _ := call("dana", "POST", "/api/projects/sales/classification", `{"table":"uploads_customers","column":"_dlt_id","tag":"personal","action":"remove"}`); code == http.StatusUnprocessableEntity {
		t.Error("a table named with underscores was refused as no identifier")
	}
	if code, _ := call("dana", "POST", "/api/projects/sales/classification", `{"table":"Customers;","column":"email","tag":"personal","action":"confirm"}`); code != http.StatusUnprocessableEntity {
		t.Error("a name that isn't an identifier wasn't refused")
	}
	if code, out := call("dana", "POST", "/api/projects/sales/classification", `{"table":"customers","column":"email","tag":"personal","action":"confirm"}`); code != 200 || out["state"] != "confirmed" {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if code, out := call("dana", "POST", "/api/projects/sales/classification", `{"table":"customers","column":"phone","tag":"personal","action":"remove"}`); code != 200 || out["state"] != "removed" {
		t.Fatalf("remove: %d %v", code, out)
	}
	if len(patches) != 3 || patches[0]["op"] != "replace" || patches[0]["path"] != "/columns/1/tags/0/state" || patches[0]["value"] != "Confirmed" ||
		patches[2]["op"] != "remove" || patches[2]["path"] != "/columns/2/tags/0" {
		t.Errorf("the catalog's patches: %v", patches)
	}
	// The project's view follows at once, ahead of catalog-sync's next round.
	_, out = call("alice", "GET", "/api/projects/sales/classification", "")
	states := map[string]string{}
	for _, c := range out["columns"].([]any) {
		m := c.(map[string]any)
		states[m["column"].(string)] = m["state"].(string)
	}
	if states["email"] != "confirmed" || states["customer_id"] != "confirmed" || states["phone"] != "" {
		t.Errorf("after: %v", states)
	}
	if code, _ := call("dana", "POST", "/api/projects/sales/classification", `{"table":"customers","column":"nothing","tag":"personal","action":"confirm"}`); code != 404 {
		t.Errorf("no such column: %d", code)
	}
	if code, _ := call("dana", "POST", "/api/projects/sales/classification", `{"table":"customers","column":"email","tag":"personal","action":"shrug"}`); code != 422 {
		t.Errorf("no such action: %d", code)
	}
}
