package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/StorScale/storscale-platform/internal/gitlab"
	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
)

// fakeNessie keeps branches: main, and those made.
type fakeNessie struct {
	mu       sync.Mutex
	branches map[string]string // name -> hash
}

func (f *fakeNessie) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v2/trees")
	switch {
	case r.Method == "GET":
		name := strings.TrimPrefix(path, "/")
		if h, ok := f.branches[name]; ok {
			json.NewEncoder(w).Encode(map[string]any{"reference": map[string]string{"type": "BRANCH", "name": name, "hash": h}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case r.Method == "POST":
		name := r.URL.Query().Get("name")
		f.branches[name] = f.branches["main"]
		json.NewEncoder(w).Encode(map[string]any{"reference": map[string]string{"type": "BRANCH", "name": name, "hash": f.branches["main"]}})
	case r.Method == "DELETE":
		name, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "@")
		delete(f.branches, name)
	}
}

// fakeGitLab lists these merge requests, and keeps the statuses set.
type fakeGitLab struct {
	mrs      []gitlab.MergeRequest
	statuses []string
}

func (f *fakeGitLab) OpenMergeRequests(context.Context, string) ([]gitlab.MergeRequest, error) {
	return f.mrs, nil
}
func (f *fakeGitLab) Status(_ context.Context, sha, state, _, _, _ string) error {
	f.statuses = append(f.statuses, sha+" "+state)
	return nil
}

// fakeRuns is Airflow's API as far as starting runs: one per run id.
type fakeRuns struct {
	mu   sync.Mutex
	runs map[string]map[string]any // run id -> conf
}

func (f *fakeRuns) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/auth/token") {
		w.Write([]byte(`{"access_token": "jwt"}`))
		return
	}
	var body struct {
		RunID string         `json:"dag_run_id"`
		Conf  map[string]any `json:"conf"`
	}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)
	if _, ok := f.runs[body.RunID]; ok {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"detail": "exists"}`))
		return
	}
	f.runs[body.RunID] = body.Conf
	w.Write([]byte(`{"dag_run_id": "` + body.RunID + `"}`))
}

func TestAMergeRequestsDataBranch(t *testing.T) {
	ctx := context.Background()
	p, err := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{},
		"pipelines":{"serviceAccount":"airflow-pipelines","repo":"https://gitlab.example.com/sales/dags.git","credentials":{"secretRef":"git"},
		"reviews":{"gitlab":"https://gitlab.example.com","dbt":"dbt"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	nessie := &fakeNessie{branches: map[string]string{"main": "c0ffee"}}
	ns := httptest.NewServer(nessie)
	defer ns.Close()
	runs := &fakeRuns{runs: map[string]map[string]any{}}
	as := httptest.NewServer(runs)
	defer as.Close()
	ft := &fakeTrino{catalogs: map[string]bool{"iceberg": true}}
	gl := &fakeGitLab{mrs: []gitlab.MergeRequest{{IID: 7, SHA: "abcdef0123456789", WebURL: "https://gitlab.example.com/sales/dags/-/merge_requests/7"}}}
	var asked []string
	r := &Reconciler{
		Store:   &memStore{secrets: map[string]map[string]string{"sales/git": {"username": "bot", "password": "tok"}}},
		Airflow: &Airflow{API: pipelines.New(as.URL+"/pipelines", "storscale-platform", "pw")},
		Reviewer: &Reviewer{Trino: ft.server(t), Nessie: &Nessie{URL: ns.URL}, GitLab: func(base, token, project string) MergeRequests {
			asked = append(asked, base+" "+token+" "+project)
			return gl
		}},
	}

	// First seen: its branch and catalog, no run yet (Trino's policies first).
	mrs, client, err := r.openReviews(ctx, p)
	if err != nil || len(mrs) != 1 || asked[0] != "https://gitlab.example.com tok sales/dags" {
		t.Fatalf("open: %v %v %v", mrs, err, asked)
	}
	if err := r.applyReviews(ctx, p, mrs, client, nil); err != nil {
		t.Fatal(err)
	}
	if nessie.branches["sales-mr-7"] != "c0ffee" || !ft.catalogs["sales_mr_7"] || len(runs.runs) != 0 {
		t.Fatalf("first pass: branches %v, catalogs %v, runs %v", nessie.branches, ft.catalogs, runs.runs)
	}
	for _, st := range ft.stmts {
		if strings.Contains(st, "CREATE CATALOG") && !strings.Contains(st, "'http://nessie:19120/iceberg/sales-mr-7/'") {
			t.Errorf("its catalog isn't at the branch: %s", st)
		}
	}

	// Seen again: its review starts, once per commit; the merge request told.
	for i := 0; i < 2; i++ {
		if err := r.applyReviews(ctx, p, mrs, client, []int{7}); err != nil {
			t.Fatal(err)
		}
	}
	conf := runs.runs["mr-7-abcdef012345"]
	if len(runs.runs) != 1 || conf["catalog"] != "sales_mr_7" || conf["prodCatalog"] != "iceberg" || conf["gitlabProject"] != "sales/dags" {
		t.Fatalf("runs: %v", runs.runs)
	}
	if strings.Join(gl.statuses, ",") != "abcdef0123456789 pending" {
		t.Errorf("statuses: %v", gl.statuses)
	}

	// Closed: its branch and catalog go.
	if err := r.applyReviews(ctx, p, nil, client, []int{7}); err != nil {
		t.Fatal(err)
	}
	if _, ok := nessie.branches["sales-mr-7"]; ok || ft.catalogs["sales_mr_7"] {
		t.Errorf("closed: branches %v, catalogs %v", nessie.branches, ft.catalogs)
	}
	if pols := p.ReviewPolicies([]int{7}); len(pols) != 3 || pols[0].Resources["catalog"].Values[0] != "sales_mr_7" {
		t.Errorf("policies: %v", pols)
	}
}
