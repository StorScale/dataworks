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

	"github.com/StorScale/dataworks/internal/pipelines"
	"github.com/StorScale/dataworks/internal/project"
)

// projectsKept is a project store holding specs, beside statuses.
type projectsKept struct {
	statuses
	specs map[string]*project.Project
}

func (p *projectsKept) Projects(context.Context) ([]*project.Project, error) {
	var out []*project.Project
	for _, s := range p.specs {
		out = append(out, s)
	}
	return out, nil
}

func (p *projectsKept) Project(_ context.Context, name string) (*project.Project, error) {
	return p.specs[name], nil
}

func (p *projectsKept) PutProject(_ context.Context, s *project.Project) error {
	p.specs[s.Metadata.Name] = s
	return nil
}

const salesV1 = `
metadata: {name: sales}
spec:
  members: [{user: alice, role: editor}, {user: bob, role: editor}]
  tables: {}
  pipelines: {serviceAccount: sales-pipelines, repo: "https://git.example/sales.git"}
`

func TestAPlanIsAppliedBySomeoneElse(t *testing.T) {
	var triggered []map[string]any
	airflow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/token" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
			return
		}
		if r.URL.Path == "/api/v2/dags/"+PlanDAG+"/dagRuns" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			triggered = append(triggered, body)
		}
		w.Write([]byte("{}"))
	}))
	defer airflow.Close()

	current, err := project.Compose("sales", []byte(salesV1), nil)
	if err != nil {
		t.Fatal(err)
	}
	const pipelinesAccount = "service-account-project-sales-pipelines"
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: []project.Person{
		{Username: pipelinesAccount, Role: "pipelines"}, {Username: "alice", Role: "editor"}, {Username: "bob", Role: "editor"}}}}},
		specs: map[string]*project.Project{"sales": current}}
	files := kept{"semantic/sales.yaml": "metric: {name: revenue}\n"}
	s := &server{cfg: config{Groups: []string{"analysts", "engineers"}, AdminGroups: []string{"engineers"}, InstallTarget: "production"},
		store: st, files: files, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)), airflow: pipelines.New(airflow.URL, "u", "p"),
		tokenUser: func(raw string) (string, error) { return raw, nil },
		sessions: map[string]*session{
			"alice": {User: user{Username: "alice", Groups: []string{"engineers"}}, Expires: time.Now().Add(time.Hour)},
			"bob":   {User: user{Username: "bob", Groups: []string{"engineers"}}, Expires: time.Now().Add(time.Hour)},
		}}
	h := s.routes()
	call := func(who, method, path, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Platform-Request", "1")
		if strings.HasPrefix(who, "service-account") {
			r.Header.Set("Authorization", "Bearer "+who)
		} else {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// alice plans v2: its run is started with the ref.
	code, p := call("alice", "POST", "/api/projects/sales/plans", `{"ref": "v2"}`)
	if code != 200 || p["state"] != "fetching" || len(triggered) != 1 {
		t.Fatalf("plan: %d %v, runs %v", code, p, triggered)
	}
	if conf := triggered[0]["conf"].(map[string]any); conf["ref"] != "v2" || conf["target"] != "production" || conf["plan"] != p["id"] {
		t.Errorf("the run's conf: %v", conf)
	}
	id := p["id"].(string)
	if code, _ := call("alice", "POST", "/api/projects/sales/plans", `{"ref": "--upload-pack=x"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("an option as a ref: %d", code)
	}

	// Only the project's pipelines send the files.
	v2 := strings.Replace(salesV1, "{user: bob, role: editor}]", "{user: bob, role: editor}, {group: analysts, role: reader}]", 1)
	files2, _ := json.Marshal(map[string]any{"commit": "abc123", "files": map[string]string{
		"project.yaml": v2, "semantic.yaml": "metric: {name: revenue}\n---\nmetric: {name: margin}\n",
		"targets/production.yaml": "spec: {description: In production}", "targets/development.yaml": "spec: {description: nope}"}})
	if code, _ := call("bob", "POST", "/api/projects/sales/plans/"+id+"/files", string(files2)); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("files from bob: %d", code)
	}
	code, p = call(pipelinesAccount, "POST", "/api/projects/sales/plans/"+id+"/files", string(files2))
	if code != 200 || p["state"] != "ready" {
		t.Fatalf("files: %d %v", code, p)
	}
	changes, _ := json.Marshal(p["changes"])
	for _, want := range []string{"member added: group analysts as reader", "changed: description", "metric added: margin"} {
		if !strings.Contains(string(changes), want) {
			t.Errorf("changes %s: no %q", changes, want)
		}
	}

	// alice planned it: she can't apply it. bob does.
	if code, _ := call("alice", "POST", "/api/projects/sales/plans/"+id+"/apply", ""); code != http.StatusForbidden {
		t.Errorf("applied by who planned it: %d", code)
	}
	if code, out := call("bob", "POST", "/api/projects/sales/plans/"+id+"/apply", ""); code != 200 || out["state"] != "applied" {
		t.Fatalf("applied by bob: %d %v", code, out)
	}
	if got := st.specs["sales"]; got.Spec.Description != "In production" || len(got.Spec.Members) != 3 {
		t.Errorf("the spec applied: %+v", got.Spec)
	}
	if !strings.Contains(files["semantic/sales.yaml"], "margin") {
		t.Errorf("the model applied: %q", files["semantic/sales.yaml"])
	}
	if code, _ := call("bob", "POST", "/api/projects/sales/plans/"+id+"/apply", ""); code != http.StatusConflict {
		t.Errorf("applied twice: %d", code)
	}

	// The release, to members.
	code, out := call("alice", "GET", "/api/projects/sales/plans", "")
	if code != 200 || out["release"].(map[string]any)["ref"] != "v2" || out["release"].(map[string]any)["appliedBy"] != "bob" {
		t.Errorf("plans: %d %v", code, out)
	}

	// A plan made before the project changed is planned again.
	_, p = call("alice", "POST", "/api/projects/sales/plans", `{"ref": "v1"}`)
	id = p["id"].(string)
	files1, _ := json.Marshal(map[string]any{"commit": "def456", "files": map[string]string{"project.yaml": salesV1}})
	if code, p := call(pipelinesAccount, "POST", "/api/projects/sales/plans/"+id+"/files", string(files1)); code != 200 || p["state"] != "ready" {
		t.Fatalf("v1's files: %d %v", code, p)
	}
	st.specs["sales"].Spec.Description = "changed by hand"
	if code, _ := call("bob", "POST", "/api/projects/sales/plans/"+id+"/apply", ""); code != http.StatusConflict {
		t.Errorf("a stale plan applied: %d", code)
	}

	// A repo whose project.yaml names another project fails its plan.
	_, p = call("alice", "POST", "/api/projects/sales/plans", `{"ref": "main"}`)
	id = p["id"].(string)
	other, _ := json.Marshal(map[string]any{"commit": "0f0", "files": map[string]string{"project.yaml": strings.Replace(salesV1, "name: sales", "name: other", 1)}})
	if _, p := call(pipelinesAccount, "POST", "/api/projects/sales/plans/"+id+"/files", string(other)); p["state"] != "failed" || !strings.Contains(p["error"].(string), "other") {
		t.Errorf("another project's file: %v", p)
	}
}
