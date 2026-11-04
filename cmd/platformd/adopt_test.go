package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/store"
)

// A project's editors adopt tables of an external store, and stop adopting
// one still followed; a reader can't, nor can anyone name a store the
// install hasn't, adopt a table twice, or let go of one taken over.
func TestEditorsAdoptAndStopAdoptingTables(t *testing.T) {
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"alice","role":"editor"},{"user":"erin","role":"reader"}],
		"tables":{"adopt":[{"table":"old","from":"lakehouse.sales.old","takenOver":"2026-11-04T12:00:00Z","takenOverBy":"bob"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	members := []project.Person{{Username: "alice", Role: "editor"}, {Username: "erin", Role: "reader"}}
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: members}}},
		specs: map[string]*project.Project{"sales": sales}}, conns: map[string]store.StoredConnection{}}
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}, ExternalStores: []string{"lakehouse"}, TakeoverStores: []string{"lakehouse"}},
		store: st, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tokenUser: func(raw string) (string, error) { return raw, nil }, sessions: map[string]*session{}}
	for _, n := range []string{"alice", "erin"} {
		s.sessions[n] = &session{User: user{Username: n, Groups: []string{"analysts"}}, Expires: time.Now().Add(time.Hour)}
	}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+who)
		r.Header.Set("X-Platform-Request", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	adopts := func() *project.Tables { return st.specs["sales"].Spec.Tables }

	if code, body := call("alice", "GET", "/api/projects/sales/adoptable", ""); code != http.StatusOK || !strings.Contains(body, `"name":"lakehouse"`) || !strings.Contains(body, `"takeover":true`) {
		t.Errorf("the stores: %d %s", code, body)
	}
	if code, _ := call("erin", "POST", "/api/projects/sales/adopt", `{"tables":[{"table":"orders","from":"lakehouse.sales.orders"}]}`); code != http.StatusForbidden {
		t.Errorf("a reader adopts: %d", code)
	}
	if code, body := call("alice", "POST", "/api/projects/sales/adopt", `{"tables":[{"table":"orders","from":"elsewhere.sales.orders"}]}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a store the install hasn't: %d %s", code, body)
	}
	if code, body := call("alice", "POST", "/api/projects/sales/adopt", `{"tables":[{"table":"orders","from":"lakehouse.sales.orders"},{"table":"custs","from":"lakehouse.sales.custs"}]}`); code != http.StatusOK {
		t.Fatalf("an editor adopts two: %d %s", code, body)
	}
	if a := adopts(); a.Adoption("orders") == nil || a.Adoption("custs") == nil || !a.TakenOver("old") {
		t.Fatalf("adopted, the takeover kept: %+v", a.Adopt)
	}
	if code, _ := call("alice", "POST", "/api/projects/sales/adopt", `{"tables":[{"table":"orders2","from":"lakehouse.sales.orders"}]}`); code != http.StatusConflict {
		t.Errorf("the same table twice: %d", code)
	}
	if code, _ := call("alice", "DELETE", "/api/projects/sales/adopt/custs", ""); code != http.StatusNoContent || adopts().Adoption("custs") != nil {
		t.Errorf("stop adopting custs: %d %+v", code, adopts().Adopt)
	}
	if code, body := call("alice", "DELETE", "/api/projects/sales/adopt/old", ""); code != http.StatusConflict || !strings.Contains(body, "taken over") || adopts().Adoption("old") == nil {
		t.Errorf("one taken over is let go: %d %s", code, body)
	}
}
