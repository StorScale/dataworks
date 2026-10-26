package main

import (
	"context"
	"encoding/json"
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

// connsKept is a project store with connections, in memory.
type connsKept struct {
	projectsKept
	conns map[string]store.StoredConnection // by project/name
}

func (c *connsKept) Connections(_ context.Context, p string) ([]store.StoredConnection, error) {
	out := []store.StoredConnection{}
	for k, v := range c.conns {
		if strings.HasPrefix(k, p+"/") {
			out = append(out, v)
		}
	}
	return out, nil
}

func (c *connsKept) PutConnection(_ context.Context, p string, v store.StoredConnection) error {
	if v.Name == "storscale-env" {
		return store.ErrNotTheProjects
	}
	c.conns[p+"/"+v.Name] = v
	return nil
}

func (c *connsKept) DeleteConnection(_ context.Context, p, name string) error {
	delete(c.conns, p+"/"+name)
	return nil
}

func (c *connsKept) Connection(_ context.Context, p, name string) (map[string]string, error) {
	return c.conns[p+"/"+name].Data, nil
}

func TestEditorsGiveTheProjectCredentialsMembersSeeOnlyTheirNames(t *testing.T) {
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"alice","role":"reader"}],
		"tables":{},"notify":{"teams":"sales-teams"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: []project.Person{
		{Username: "bob", Role: "editor"}, {Username: "alice", Role: "reader"}}}}}, specs: map[string]*project.Project{"sales": sales}},
		conns: map[string]store.StoredConnection{"sales/sales-old": {ConnectionMeta: store.ConnectionMeta{Name: "sales-old"},
			Data: map[string]string{"token": "made-with-kubectl"}}}}
	hour := time.Hour
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}},
		store: st, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: time.Now().Add(hour)},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: time.Now().Add(hour)},
			"carol": {User: user{Username: "carol", Groups: []string{"analysts"}}, Expires: time.Now().Add(hour)},
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
	const base = "/api/projects/sales/connections"
	git := `{"name":"sales-dags","kind":"git","values":{"username":"bot","password":"tok-1"}}`
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"alice", "POST", base, git, http.StatusForbidden}, // a reader
		{"carol", "GET", base, "", http.StatusNotFound},    // not a member
		{"bob", "POST", base, strings.Replace(git, "sales-dags", "dags", 1), http.StatusUnprocessableEntity},
		{"bob", "POST", base, strings.Replace(git, "tok-1", "", 1), http.StatusUnprocessableEntity}, // no token
		{"bob", "POST", base, strings.Replace(git, `"kind":"git"`, `"kind":"nope"`, 1), http.StatusUnprocessableEntity},
		{"bob", "POST", base, git, http.StatusOK},
		{"bob", "POST", base, git, http.StatusConflict}, // there already
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.body, code, body, c.want)
		}
	}
	// alice, a reader, sees them: their names, kinds, who added them, never a secret.
	code, body := call("alice", "GET", base, "")
	if code != 200 || strings.Contains(body, "tok-1") || strings.Contains(body, "made-with-kubectl") {
		t.Fatalf("listed: %d %s", code, body)
	}
	var out struct {
		Connections []connectionView `json:"connections"`
		CanEdit     bool             `json:"canEdit"`
	}
	json.Unmarshal([]byte(body), &out)
	if len(out.Connections) != 2 || out.CanEdit {
		t.Fatalf("%+v", out)
	}
	d := out.Connections[0]
	if d.Name == "sales-old" {
		d = out.Connections[1]
	}
	if d.Kind != "git" || d.AddedBy != "bob" || d.Values["username"] != "bot" || len(d.Set) != 1 || d.Set[0] != "password" {
		t.Errorf("sales-dags: %+v", d)
	}
	// bob rotates the token: the user stays, the token is the new one.
	if code, body := call("bob", "PUT", base+"/sales-dags", `{"values":{"password":"tok-2"}}`); code != 200 {
		t.Fatalf("rotate: %d %s", code, body)
	}
	if c := st.conns["sales/sales-dags"]; c.Data["password"] != "tok-2" || c.Data["username"] != "bot" || c.ChangedBy != "bob" || c.AddedBy != "bob" {
		t.Errorf("rotated: %+v", c)
	}
	// A Teams connection the spec names can't go; the old one can.
	call("bob", "POST", base, `{"name":"sales-teams","kind":"teams","values":{"webhook":"https://example.com/hook"}}`)
	if code, body := call("bob", "DELETE", base+"/sales-teams", ""); code != http.StatusConflict || !strings.Contains(body, "notify.teams") {
		t.Errorf("removing one in use: %d %s", code, body)
	}
	if code, _ := call("bob", "DELETE", base+"/sales-old", ""); code != 200 || len(st.conns) != 2 {
		t.Errorf("removed: %d %v", code, st.conns)
	}
}
