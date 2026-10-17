package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeNessie(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v2/trees/main":
			w.Write([]byte(`{"reference": {"type": "BRANCH", "name": "main", "hash": "c3"}}`))
		case r.Method == "GET" && r.URL.Path == "/api/v2/trees/nothing":
			http.NotFound(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/v2/trees":
			if r.URL.Query().Get("page-token") == "" {
				w.Write([]byte(`{"references": [{"type": "BRANCH", "name": "main", "hash": "c3"}, {"type": "TAG", "name": "sales.close-2026-09", "hash": "c1"}], "hasMore": true, "token": "p2"}`))
			} else {
				w.Write([]byte(`{"references": [{"type": "TAG", "name": "marketing.q3", "hash": "c2"}], "hasMore": false}`))
			}
		case r.Method == "POST" && r.URL.Path == "/api/v2/trees":
			var to Ref
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &to)
			if r.URL.Query().Get("type") != "TAG" || to.Name != "main" || to.Hash != "c2" {
				t.Errorf("tag: %s %s", r.URL.RawQuery, body)
			}
			w.Write([]byte(`{"reference": {"type": "TAG", "name": "` + r.URL.Query().Get("name") + `", "hash": "c2"}}`))
		case r.Method == "DELETE":
			if r.URL.EscapedPath() != "/api/v2/trees/sales.close-2026-09@c1" {
				t.Errorf("untag: %s", r.URL.EscapedPath())
			}
		case r.Method == "GET" && r.URL.Path == "/api/v2/trees/main/history":
			if r.URL.Query().Get("page-token") == "" {
				w.Write([]byte(`{"logEntries": [
					{"commitMeta": {"hash": "c3", "message": "update sales.payroll", "committer": "", "commitTime": "2026-10-03T12:00:00Z"},
					 "operations": [{"key": {"elements": ["sales", "payroll"]}}]},
					{"commitMeta": {"hash": "c2", "message": "update sales.orders", "authors": ["bob"], "commitTime": "2026-10-02T12:00:00Z"},
					 "operations": [{"key": {"elements": ["sales", "orders"]}}]}], "hasMore": true, "token": "h2"}`))
			} else {
				w.Write([]byte(`{"logEntries": [
					{"commitMeta": {"hash": "c1", "message": "create sales.orders", "commitTime": "2026-10-01T12:00:00Z"},
					 "operations": [{"key": {"elements": ["sales", "orders"]}}]}], "hasMore": false}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return ts, &calls
}

func TestNessieRefsTagsAndHistory(t *testing.T) {
	ts, _ := fakeNessie(t)
	defer ts.Close()
	n := &Nessie{HTTP: ts.Client(), Base: ts.URL + "/api/v2"}
	ctx := context.Background()
	if r, err := n.Ref(ctx, "main"); err != nil || r.Hash != "c3" {
		t.Errorf("main: %+v %v", r, err)
	}
	if r, err := n.Ref(ctx, "nothing"); r != nil || err != nil {
		t.Errorf("no such ref: %+v %v", r, err)
	}
	tags, err := n.Tags(ctx, "sales.")
	if err != nil || len(tags) != 1 || tags[0].Name != "sales.close-2026-09" {
		t.Errorf("sales' tags (both pages): %+v %v", tags, err)
	}
	if r, err := n.Tag(ctx, "sales.close-2026-10", "c2"); err != nil || r.Name != "sales.close-2026-10" {
		t.Errorf("tag: %+v %v", r, err)
	}
	if err := n.Untag(ctx, "sales.close-2026-09", "c1"); err != nil {
		t.Error(err)
	}
	h, err := n.History(ctx, "sales.orders", 10, 1000)
	if err != nil || len(h) != 2 || h[0].Hash != "c2" || h[0].Author != "bob" || h[1].Hash != "c1" || strings.Join(h[1].Keys, ",") != "sales.orders" {
		t.Errorf("orders' history (both pages, payroll's left out): %+v %v", h, err)
	}
	if h, _ := n.History(ctx, "", 1, 1000); len(h) != 1 || h[0].Hash != "c3" {
		t.Errorf("limit: %+v", h)
	}
	c, err := n.At(ctx, time.Date(2026, 10, 2, 18, 0, 0, 0, time.UTC), 1000)
	if err != nil || c == nil || c.Hash != "c2" {
		t.Errorf("as of Oct 2, 18:00: %+v %v", c, err)
	}
	if c, err := n.At(ctx, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 1000); c != nil || err != nil {
		t.Errorf("before the history kept: %+v %v", c, err)
	}
}
