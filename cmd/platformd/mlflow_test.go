package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

// A fake MLflow: what it was asked, in which workspace; a version registered by bob.
type fakeMLflow struct {
	mu   sync.Mutex
	seen []map[string]any
}

func (f *fakeMLflow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	json.Unmarshal(body, &in)
	f.mu.Lock()
	f.seen = append(f.seen, map[string]any{"path": r.URL.Path, "method": r.Method, "workspace": r.Header.Get("X-MLFLOW-WORKSPACE"),
		"auth": r.Header.Get("Authorization"), "body": in})
	f.mu.Unlock()
	switch r.URL.Path {
	case "/api/2.0/mlflow/model-versions/get":
		w.Write([]byte(`{"model_version":{"version":"1","tags":[{"key":"storscale.registered_by","value":"bob"}]}}`))
	default:
		w.Write([]byte(`{}`))
	}
}

func (f *fakeMLflow) last(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.seen) - 1; i >= 0; i-- {
		if f.seen[i]["path"] == path {
			return f.seen[i]
		}
	}
	return nil
}

func TestMLflowIsTheProjectsWorkspaceAndApprovalThePlatforms(t *testing.T) {
	fake := &fakeMLflow{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	sales, _ := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"user":"dana","role":"editor"},{"user":"alice","role":"reader"}],"tables":{}}}`))
	members := []project.Person{{Username: "bob", Role: "editor"}, {Username: "dana", Role: "editor"}, {Username: "alice", Role: "reader"}, {Username: "service-account-pipes", Role: "pipelines"}}
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Spec: sales, Members: members}}}, specs: map[string]*project.Project{"sales": sales}}
	hour := time.Now().Add(time.Hour)
	sess := map[string]*session{}
	for _, u := range []string{"bob", "dana", "alice", "carol"} {
		sess[u] = &session{User: user{Username: u, Groups: []string{"analysts"}}, Expires: hour}
	}
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}, MLflowURL: srv.URL}, store: st, now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), tokenUser: func(raw string) (string, error) { return raw, nil }, sessions: sess}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+who)
		r.Header.Set("X-Platform-Request", "1")
		r.Header.Set("X-MLFLOW-WORKSPACE", "other-project") // a caller's guess, which the platform replaces
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const api = "/mlflow/sales/api/2.0/mlflow/"
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"carol", "POST", api + "experiments/search", `{}`, http.StatusForbidden},                 // no member
		{"alice", "POST", api + "experiments/search", `{}`, http.StatusOK},                        // a reader reads
		{"alice", "POST", api + "runs/create", `{"experiment_id":"1"}`, http.StatusForbidden},     // but doesn't write
		{"bob", "GET", "/mlflow/sales/api/3.0/mlflow/workspaces", ``, http.StatusNotFound},        // not the workspaces API
		{"bob", "POST", "/mlflow/sales/api/2.0/mlflow/gateway/routes", `{}`, http.StatusNotFound}, // nor MLflow's others
		{"bob", "POST", api + "model-versions/set-tag", `{"key":"storscale.approved_by","value":"bob"}`, http.StatusForbidden},
		{"bob", "POST", api + "registered-models/alias", `{"name":"churn","alias":"approved","version":"1"}`, http.StatusForbidden},
		{"bob", "POST", api + "runs/log-batch", `{"run_id":"r","tags":[{"key":"storscale.user","value":"x"}]}`, http.StatusForbidden},
		{"bob", "POST", api + "runs/create", `{"experiment_id":"1","tags":[{"key":"storscale.user","value":"mallory"},{"key":"team","value":"a"}]}`, http.StatusOK},
		{"bob", "POST", api + "model-versions/create", `{"name":"churn","source":"runs:/r/model"}`, http.StatusOK},
		{"service-account-pipes", "POST", api + "runs/create", `{"experiment_id":"1"}`, http.StatusOK},     // the project's pipelines write
		{"bob", "POST", "/api/projects/sales/models/churn/versions/1/approve", ``, http.StatusForbidden},   // his own
		{"alice", "POST", "/api/projects/sales/models/churn/versions/1/approve", ``, http.StatusForbidden}, // a reader
		{"dana", "POST", "/api/projects/sales/models/churn/versions/1/approve", ``, http.StatusOK},
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.path, code, body, c.want)
		}
	}
	for _, seen := range fake.seen {
		if p, _ := seen["path"].(string); p != "/api/3.0/mlflow/workspaces" && (seen["workspace"] != "sales" || seen["auth"] != "") {
			t.Errorf("%s reached MLflow in workspace %q with auth %q", p, seen["workspace"], seen["auth"])
		}
	}
	created := 0
	for _, seen := range fake.seen {
		if seen["path"] == "/api/3.0/mlflow/workspaces" {
			created++
		}
	}
	if created != 1 {
		t.Errorf("the workspace was made %d times", created)
	}
	var bobs string // bob's run: his own tag kept, the forged storscale.user replaced by his
	for _, seen := range fake.seen {
		if b, _ := json.Marshal(seen["body"]); seen["path"] == "/api/2.0/mlflow/runs/create" && strings.Contains(string(b), `"team"`) {
			bobs = string(b)
		}
	}
	if strings.Contains(bobs, "mallory") || !strings.Contains(bobs, `{"key":"storscale.user","value":"bob"}`) {
		t.Errorf("bob's run's tags: %s", bobs)
	}
	version, _ := json.Marshal(fake.last("/api/2.0/mlflow/model-versions/create")["body"])
	if !strings.Contains(string(version), `{"key":"storscale.registered_by","value":"bob"}`) {
		t.Errorf("the version's tags: %s", version)
	}
	alias, _ := json.Marshal(fake.last("/api/2.0/mlflow/registered-models/alias")["body"])
	if !strings.Contains(string(alias), `"alias":"approved"`) || !strings.Contains(string(alias), `"version":"1"`) {
		t.Errorf("dana's approval: %s", alias)
	}
}
