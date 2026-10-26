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

func TestEditorsAddAConnectorsImport(t *testing.T) {
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"alice","role":"reader"}],"tables":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: []project.Person{
		{Username: "bob", Role: "editor"}, {Username: "alice", Role: "reader"}}}}}, specs: map[string]*project.Project{"sales": sales}},
		conns: map[string]store.StoredConnection{"sales/sales-sf": {ConnectionMeta: store.ConnectionMeta{Name: "sales-sf", Kind: "salesforce"}}}}
	s := &server{cfg: config{Groups: []string{"analysts"}}, store: st, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: time.Now().Add(time.Hour)},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: time.Now().Add(time.Hour)},
		}}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Platform-Request", "1")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	crm := `{"source":"salesforce","connection":"sales-sf","schedule":"0 * * * *","salesforce":{"objects":["account","opportunity"],"since":"2026-01-01"}}`
	for _, c := range []struct {
		who, body string
		want      int
	}{
		{"alice", crm, http.StatusForbidden},
		{"bob", strings.Replace(crm, "sales-sf", "nowhere", 1), http.StatusUnprocessableEntity},
		{"bob", strings.Replace(crm, `"opportunity"`, `"spaceship"`, 1), http.StatusUnprocessableEntity},
		{"bob", `{"source":"salesforce","nonsense":1}`, http.StatusBadRequest},
		{"bob", crm, http.StatusOK},
	} {
		if code, body := call(c.who, "PUT", "/api/projects/sales/imports/crm", c.body); code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.who, c.body, code, body, c.want)
		}
	}
	if pl := st.specs["sales"].Spec.Pipelines; pl == nil || pl.ServiceAccount != "airflow-pipelines" {
		t.Errorf("an import runs as the project's pipelines, which it gets: %+v", pl)
	}
	got := st.specs["sales"].Spec.Imports
	if len(got) != 1 || got[0].Name != "crm" || got[0].Salesforce == nil || len(got[0].Salesforce.Objects) != 2 {
		t.Fatalf("saved: %+v", got)
	}
	// Changed: replaced, not added again.
	call("bob", "PUT", "/api/projects/sales/imports/crm", strings.Replace(crm, `,"opportunity"`, "", 1))
	if got := st.specs["sales"].Spec.Imports; len(got) != 1 || len(got[0].Salesforce.Objects) != 1 {
		t.Errorf("replaced: %+v", got)
	}
	if code, _ := call("bob", "DELETE", "/api/projects/sales/imports/crm", ""); code != 200 || len(st.specs["sales"].Spec.Imports) != 0 {
		t.Errorf("removed: %d %+v", code, st.specs["sales"].Spec.Imports)
	}
	if code, _ := call("bob", "DELETE", "/api/projects/sales/imports/crm", ""); code != http.StatusNotFound {
		t.Errorf("removed again: %d", code)
	}
}
