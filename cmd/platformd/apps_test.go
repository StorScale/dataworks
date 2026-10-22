package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

func TestAnAppIsFetchedAsWhoeverOpensIt(t *testing.T) {
	var asked []map[string]any
	var tokens []string
	semantic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		in["path"] = r.URL.Path
		asked = append(asked, in)
		tokens = append(tokens, r.Header.Get("Authorization"))
		w.Write([]byte(`{"columns":["order__region","revenue"],"rows":[["EU",10]]}`))
	}))
	defer semantic.Close()
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"alice","role":"reader"}],"tables":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Spec: sales,
		Members: []project.Person{{Username: "bob", Role: "editor"}, {Username: "alice", Role: "reader"}}}}},
		specs: map[string]*project.Project{"sales": sales}}
	hour := time.Now().Add(time.Hour)
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}, SemanticURL: semantic.URL}, store: st, now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), tokenUser: func(raw string) (string, error) { return raw, nil },
		sessions: map[string]*session{"bob": {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: hour},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: hour}}}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+who)
		r.Header.Set("X-Platform-Request", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const byRegion = `{"kind":"metric","metrics":["revenue"],"by":"order__region","show":"bar","filters":[{"field":"order__region","values":["EU","US"]}]}`
	const orders = `{"kind":"table","table":"orders","columns":["id","region"],"filters":[{"field":"region"}]}`
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"alice", "PUT", "/api/projects/sales/apps/by-region", byRegion, http.StatusForbidden}, // a reader
		{"bob", "PUT", "/api/projects/sales/apps/by-region", `{"kind":"metric","metrics":["revenue"],"show":"bar"}`, http.StatusUnprocessableEntity},
		{"bob", "PUT", "/api/projects/sales/apps/by-region", byRegion, http.StatusOK},
		{"bob", "PUT", "/api/projects/sales/apps/orders", orders, http.StatusOK},
		{"alice", "POST", "/api/projects/sales/apps/by-region/data", `{"filters":{"order__region":"APAC"}}`, http.StatusBadRequest}, // not one of its values
		{"alice", "POST", "/api/projects/sales/apps/by-region/data", `{"filters":{"order__region":"EU","other":"x"}}`, http.StatusOK},
		{"alice", "POST", "/api/projects/sales/apps/orders/data", `{"filters":{"region":"E'U"}}`, http.StatusOK},
		{"carol", "POST", "/api/projects/sales/apps/orders/data", `{}`, http.StatusForbidden}, // no member
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.path, code, body, c.want)
		}
	}
	if len(asked) != 2 || tokens[0] != "Bearer alice" || tokens[1] != "Bearer alice" {
		t.Fatalf("asked %d times, as %v", len(asked), tokens)
	}
	q, _ := json.Marshal(asked[0])
	if asked[0]["path"] != "/api/projects/sales/semantic/query" || !strings.Contains(string(q), `{{ Dimension('order__region') }} = 'EU'`) || strings.Contains(string(q), "other") {
		t.Errorf("the metric query: %s", q)
	}
	if sql, _ := asked[1]["sql"].(string); asked[1]["path"] != "/api/projects/sales/sql" ||
		sql != `SELECT "id", "region" FROM "iceberg"."sales"."orders" WHERE CAST("region" AS varchar) = 'E''U'` {
		t.Errorf("the table's SQL: %v", asked[1])
	}
}
