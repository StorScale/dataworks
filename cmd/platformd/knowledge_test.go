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
)

func TestEditorsDeclareAndRemoveKnowledgeBanks(t *testing.T) {
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"alice","role":"reader"}],
		"tables":{},"files":{},"llm":{"budget":5}}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Spec: sales,
		Members: []project.Person{{Username: "bob", Role: "editor"}, {Username: "alice", Role: "reader"}}}}},
		specs: map[string]*project.Project{"sales": sales}}
	hour := time.Now().Add(time.Hour)
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}}, store: st, now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: hour},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: hour}}}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Platform-Request", "1")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const bank = `{"description":"the guides","files":[{"prefix":"landing/docs/"}]}`
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"alice", "PUT", "/api/projects/sales/knowledge/guides", bank, http.StatusForbidden},
		{"bob", "PUT", "/api/projects/sales/knowledge/guides", `{"schedule":"0 3 * * *","files":[{"prefix":"landing/docs/"}]}`, http.StatusUnprocessableEntity}, // no pipelines
		{"bob", "PUT", "/api/projects/sales/knowledge/Guides", bank, http.StatusUnprocessableEntity},
		{"bob", "PUT", "/api/projects/sales/knowledge/guides", bank, http.StatusOK},
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.path, code, body, c.want)
		}
	}
	if k := st.specs["sales"].Spec.Knowledge; len(k) != 1 || k[0].Name != "guides" || k[0].Files[0].Prefix != "landing/docs/" {
		t.Fatalf("declared: %+v", k)
	}
	if code, _ := call("alice", "DELETE", "/api/projects/sales/knowledge/guides", ""); code != http.StatusForbidden {
		t.Errorf("a reader removed it: %d", code)
	}
	if code, _ := call("bob", "DELETE", "/api/projects/sales/knowledge/guides", ""); code != http.StatusNoContent || len(st.specs["sales"].Spec.Knowledge) != 0 {
		t.Errorf("removed: %d %+v", code, st.specs["sales"].Spec.Knowledge)
	}
}
