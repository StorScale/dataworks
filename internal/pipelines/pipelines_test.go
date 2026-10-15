package pipelines

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeAirflow(t *testing.T, started *map[string]any) *httptest.Server {
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	authed := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer airflow-token" }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/token", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["username"] != "storscale-platform" || in["password"] != "secret" {
			http.Error(w, `{"detail":"Invalid credentials"}`, http.StatusUnauthorized)
			return
		}
		reply(w, map[string]string{"access_token": "airflow-token"})
	})
	// The DAGs, as Airflow 3.3 filters them by bundle and tag: sales' repo has
	// orders_daily; the platform's own sales_ingest is tagged for sales; and
	// ads' repo has a DAG tagged project:sales too, which must not count.
	all := []map[string]any{
		{"dag_id": "orders_daily", "description": "Orders, daily", "is_paused": false, "bundle_name": "project-sales",
			"bundle_version": "3f2a9c1", "has_import_errors": true, "tags": []any{}},
		{"dag_id": "sales_ingest", "description": "Land and load orders", "is_paused": false, "bundle_name": "dags-folder",
			"bundle_version": nil, "has_import_errors": false, "tags": []any{map[string]any{"name": "project:sales"}}},
		{"dag_id": "ads_claims_sales", "description": "Ads, pretending", "is_paused": false, "bundle_name": "project-ads",
			"bundle_version": "77aa001", "has_import_errors": false, "tags": []any{map[string]any{"name": "project:sales"}}},
	}
	mux.HandleFunc("GET /api/v2/dags", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		found := []any{}
		for _, d := range all {
			tagged := q.Get("tags") == ""
			for _, tg := range d["tags"].([]any) {
				tagged = tagged || tg.(map[string]any)["name"] == q.Get("tags")
			}
			if (q.Get("bundle_name") == "" || d["bundle_name"] == q.Get("bundle_name")) && tagged {
				found = append(found, d)
			}
		}
		reply(w, map[string]any{"dags": found, "total_entries": len(found)})
	})
	// orders_daily has also come from ads' repo once: the same ID in two repos.
	mux.HandleFunc("GET /api/v2/dags/{id}/dagVersions", func(w http.ResponseWriter, r *http.Request) {
		vs := []any{map[string]any{"bundle_name": "dags-folder"}}
		if r.PathValue("id") == "orders_daily" {
			vs = []any{map[string]any{"bundle_name": "project-sales"}, map[string]any{"bundle_name": "project-ads"},
				map[string]any{"bundle_name": "project-sales"}}
		}
		reply(w, map[string]any{"dag_versions": vs})
	})
	mux.HandleFunc("GET /api/v2/dags/orders_daily/dagRuns", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"dag_runs": []any{
			map[string]any{"dag_run_id": "scheduled__1", "state": "success", "start_date": "2026-10-15T06:00:00Z", "run_type": "scheduled",
				"conf": map[string]any{}, "bundle_version": "3f2a9c1"},
		}})
	})
	mux.HandleFunc("GET /api/v2/dags/orders_daily/dagRuns/scheduled__1/taskInstances", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"task_instances": []any{}})
	})
	mux.HandleFunc("GET /api/v2/importErrors", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"import_errors": []any{
			map[string]any{"filename": "dags/broken.py", "bundle_name": "project-sales", "stack_trace": "SyntaxError: invalid syntax (broken.py, line 3)"},
			map[string]any{"filename": "dags/other.py", "bundle_name": "project-ads", "stack_trace": "NameError: x"},
		}})
	})
	mux.HandleFunc("GET /api/v2/dags/sales_ingest/dagRuns", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"dag_runs": []any{
			map[string]any{"dag_run_id": "manual__2", "state": "success", "start_date": "2026-10-07T10:00:00Z", "end_date": "2026-10-07T10:01:00Z",
				"run_type": "manual", "conf": map[string]any{"requested_by": "bob"}},
			map[string]any{"dag_run_id": "manual__1", "state": "failed", "start_date": "2026-10-06T10:00:00Z", "end_date": nil, "run_type": "manual", "conf": map[string]any{}},
		}})
	})
	mux.HandleFunc("GET /api/v2/dags/sales_ingest/dagRuns/manual__2/taskInstances", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"task_instances": []any{
			map[string]any{"task_id": "land_orders", "state": "success", "start_date": "2026-10-07T10:00:00Z", "end_date": "2026-10-07T10:00:10Z"},
			map[string]any{"task_id": "load_orders", "state": nil},
		}})
	})
	mux.HandleFunc("POST /api/v2/dags/sales_ingest/dagRuns", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(started)
		reply(w, map[string]string{"dag_run_id": "manual__3"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestOf(t *testing.T) {
	ts := fakeAirflow(t, &map[string]any{})
	ps, err := New(ts.URL, "storscale-platform", "secret").Of(context.Background(), "sales", 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "orders_daily,sales_ingest" {
		t.Fatalf("sales' pipelines: %v, want its repo's and the platform's own tagged for it, not ads' tagged one", ids)
	}
	repo, p := ps[0], ps[1]
	if repo.Bundle != "project-sales" || repo.Commit != "3f2a9c1" || !repo.HasImportErrors || repo.Runs[0].Commit != "3f2a9c1" {
		t.Errorf("the repo's pipeline: %+v, want its bundle, commit, import errors and its run's commit", repo)
	}
	if strings.Join(repo.ClashesWith, ",") != "project-ads" || len(p.ClashesWith) != 0 {
		t.Errorf("clashes: %v and %v, want ads' repo for orders_daily only", repo.ClashesWith, p.ClashesWith)
	}
	if p.ID != "sales_ingest" || p.Description != "Land and load orders" || p.Bundle != "dags-folder" {
		t.Fatalf("the platform's pipeline: %+v", p)
	}
	if len(p.Runs) != 2 || p.Runs[0].RequestedBy != "bob" || p.Runs[1].State != "failed" || !p.Runs[1].Ended.IsZero() {
		t.Errorf("runs: %+v", p.Runs)
	}
	if len(p.Tasks) != 2 || p.Tasks[0].ID != "land_orders" || p.Tasks[1].State != "" {
		t.Errorf("the newest run's tasks: %+v", p.Tasks)
	}
	other, err := New(ts.URL, "storscale-platform", "secret").Of(context.Background(), "marketing", 10)
	if err != nil || len(other) != 0 {
		t.Errorf("another project: %+v, %v (want none)", other, err)
	}
}

func TestStart(t *testing.T) {
	started := map[string]any{}
	ts := fakeAirflow(t, &started)
	c := New(ts.URL, "storscale-platform", "secret")
	run, err := c.Start(context.Background(), "sales", "sales_ingest", "bob")
	if err != nil || run != "manual__3" {
		t.Fatalf("start: %q, %v", run, err)
	}
	if conf, _ := started["conf"].(map[string]any); conf["requested_by"] != "bob" {
		t.Errorf("the run's conf: %v, want who asked for it", started)
	}
	if _, err := c.Start(context.Background(), "sales", "someone_elses_dag", "bob"); err == nil || !strings.Contains(err.Error(), "no pipeline") {
		t.Errorf("a pipeline outside the project: %v, want refused", err)
	}
}

func TestWrongPassword(t *testing.T) {
	ts := fakeAirflow(t, &map[string]any{})
	if _, err := New(ts.URL, "storscale-platform", "wrong").Of(context.Background(), "sales", 10); err == nil {
		t.Error("a wrong password: no error")
	}
}

func TestImportErrorsAreTheProjectsRepos(t *testing.T) {
	ts := fakeAirflow(t, &map[string]any{})
	errs, err := New(ts.URL, "storscale-platform", "secret").ImportErrors(context.Background(), "sales")
	if err != nil || len(errs) != 1 || errs[0].File != "dags/broken.py" || !strings.Contains(errs[0].Error, "line 3") {
		t.Errorf("sales' import errors: %+v %v, want its repo's only", errs, err)
	}
}
