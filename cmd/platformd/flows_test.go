package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/flow"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/store"
)

// A flow is read from the repo and compiled, previewed as the person, and
// saved as a merge request; a model changed by hand is a code step.
func TestAFlowIsReadPreviewedAndSavedAsAMergeRequest(t *testing.T) {
	gl := &fakeGitLab{branches: map[string]bool{"main": true}, files: map[string]bool{}, contents: map[string]string{}, token: true}
	ts := httptest.NewServer(gl)
	defer ts.Close()
	// semanticd: runs SQL as whoever's token it's given, answering two columns.
	var ran []string
	var ranAs []string
	sem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if sql, ok := body["sql"].(string); ok {
			ran = append(ran, sql)
			ranAs = append(ranAs, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		}
		if r.URL.Path == "/api/projects/sales/semantic/query" {
			json.NewEncoder(w).Encode(map[string]any{"sql": fmt.Sprintf("SELECT region AS order__region, SUM(amount) AS revenue FROM iceberg.sales.orders GROUP BY 1 -- %v %v %v", body["metrics"], body["explain"], body["limit"])})
			return
		}
		if r.URL.Path != "/api/projects/sales/sql" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"columns": []string{"order_id", "amount"}, "types": []string{"bigint", "decimal(10,2)"},
			"rows": [][]any{{1, "10.00"}, {2, "12.50"}}, "seconds": 0.2})
	}))
	defer sem.Close()
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"dana","role":"editor"},{"user":"alice","role":"reader"}],
		"tables":{},"pipelines":{"serviceAccount":"sales-pipelines","repo":"` + ts.URL + `/sales/pipelines.git","path":"dags","credentials":{"secretRef":"sales-git"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: []project.Person{
		{Username: "dana", Role: "editor"}, {Username: "alice", Role: "reader"}}}}},
		specs: map[string]*project.Project{"sales": sales}},
		conns: map[string]store.StoredConnection{"sales/sales-git": {ConnectionMeta: store.ConnectionMeta{Name: "sales-git"},
			Data: map[string]string{"username": "bot", "password": "repo-token"}}}}
	s := &server{cfg: config{Groups: []string{"analysts"}, SemanticURL: sem.URL}, store: st, files: kept{}, now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), tokenUser: func(raw string) (string, error) { return raw, nil },
		sessions: map[string]*session{"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: time.Now().Add(time.Hour)}},
		tableColumns: func(_ context.Context, ns, table string) ([]flow.Column, error) {
			if ns == "sales" && table == "orders" {
				return []flow.Column{{Name: "order_id", Type: "bigint"}, {Name: "region", Type: "varchar"}, {Name: "amount", Type: "decimal(10,2)"}, {Name: "test", Type: "boolean"}}, nil
			}
			return nil, fmt.Errorf("no table %s.%s", ns, table)
		}}
	h := s.routes()
	call := func(token, method, path, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token == "alice" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			r.Header.Set("X-Platform-Request", "1")
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	f := flow.Flow{Name: "weekly", Schedule: "0 6 * * 1", Steps: []flow.Step{
		{ID: "orders", Kind: flow.Input, Table: "orders", X: 10, Y: 10},
		{ID: "real", Kind: flow.Filter, Inputs: []string{"orders"}, Where: "not test", Description: "No test rows"},
		{ID: "by_region", Kind: flow.Group, Inputs: []string{"real"}, By: []string{"region"}, Aggregates: []flow.Aggregate{{Function: "sum", Column: "amount"}}},
		{ID: "out", Kind: flow.Output, Inputs: []string{"by_region"}, Table: "revenue_by_region", Checks: []flow.Check{{Column: "region", Test: "not_null"}}},
	}}
	body := func(f flow.Flow, step string) string {
		b, _ := json.Marshal(map[string]any{"flow": f, "step": step})
		return string(b)
	}

	// No flows yet; nothing to read.
	if code, out := call("dana", "GET", "/api/projects/sales/flows", ""); code != 200 || len(out["flows"].([]any)) != 0 {
		t.Errorf("no flows: %d %v", code, out)
	}
	if code, _ := call("dana", "GET", "/api/projects/sales/flows/weekly", ""); code != 404 {
		t.Errorf("a flow that isn't there: %d", code)
	}
	if code, _ := call("bob", "GET", "/api/projects/sales/flows", ""); code != 403 {
		t.Errorf("not a member: %d", code)
	}

	// Compiled: columns and SQL per step, nothing run.
	code, out := call("alice", "POST", "/api/projects/sales/flows/compile", body(f, ""))
	if code != 200 || len(out["errors"].([]any)) != 0 {
		t.Fatalf("compile: %d %v", code, out)
	}
	steps := out["steps"].(map[string]any)
	if cols := mustJSON(steps["by_region"].(map[string]any)["columns"]); cols != `[{"name":"region","type":"varchar"},{"name":"sum_amount","type":"decimal(38,2)"}]` {
		t.Errorf("by_region's columns: %s", cols)
	}
	if len(ran) != 0 {
		t.Errorf("compiling ran SQL: %v", ran)
	}
	bad := f
	bad.Steps = append([]flow.Step{}, f.Steps...)
	bad.Steps[2] = flow.Step{ID: "by_region", Kind: flow.Group, Inputs: []string{"real"}, By: []string{"week"}, Aggregates: []flow.Aggregate{{Function: "sum", Column: "amount"}}}
	if code, out := call("alice", "POST", "/api/projects/sales/flows/compile", body(bad, "")); code != 200 || !strings.Contains(mustJSON(out["errors"]), "group by: no column week") {
		t.Errorf("a bad step: %d %v", code, out)
	}

	// A metric step: its SQL the semantic layer's (asked as the person, uncapped), its columns Trino's.
	metric := flow.Flow{Name: "rev", Steps: []flow.Step{
		{ID: "revenue", Kind: flow.Metric, Metrics: []string{"revenue"}, By: []string{"order__region"}},
		{ID: "out", Kind: flow.Output, Inputs: []string{"revenue"}, Table: "revenue_by_region"}}}
	code, out = call("dana", "POST", "/api/projects/sales/flows/compile", body(metric, ""))
	if code != 200 || len(out["errors"].([]any)) != 0 {
		t.Fatalf("a metric step: %d %v", code, out)
	}
	rev := out["steps"].(map[string]any)["revenue"].(map[string]any)
	if !strings.HasPrefix(rev["sql"].(string), "SELECT region AS order__region, SUM(amount) AS revenue FROM iceberg.sales.orders GROUP BY 1 -- [revenue] true 0") || mustJSON(rev["columns"]) != `[{"name":"order_id","type":"bigint"},{"name":"amount","type":"decimal(10,2)"}]` {
		t.Errorf("the metric step: %v", rev)
	}
	if len(ran) != 1 || !strings.HasPrefix(ran[0], "select * from (\nSELECT region AS order__region") || !strings.HasSuffix(ran[0], ") as metric_step where false") || ranAs[0] != "dana" {
		t.Errorf("Trino asked about the metric step: %v as %v", ran, ranAs)
	}
	ran, ranAs = nil, nil
	if code, out := call("alice", "POST", "/api/projects/sales/flows/compile", body(metric, "")); code != 200 || out["steps"].(map[string]any)["revenue"].(map[string]any)["unknown"] != true {
		t.Errorf("a metric step without a token: %d %v", code, out)
	}

	// Previewed, as the person: the steps up to it as CTEs, rows back.
	code, out = call("dana", "POST", "/api/projects/sales/flows/preview", body(f, "by_region"))
	if code != 200 || len(out["rows"].([]any)) != 2 || !strings.Contains(out["sql"].(string), `with "orders" as (`) || !strings.Contains(out["sql"].(string), `limit 100`) {
		t.Fatalf("preview: %d %v", code, out)
	}
	if len(ranAs) != 1 || ranAs[0] != "dana" || !strings.Contains(ran[0], `from "iceberg"."sales"."orders"`) {
		t.Errorf("ran as %v: %v", ranAs, ran)
	}
	if code, out := call("alice", "POST", "/api/projects/sales/flows/preview", body(f, "by_region")); code != 401 {
		t.Errorf("a session without a token: %d %v", code, out)
	}
	if code, out := call("dana", "POST", "/api/projects/sales/flows/preview", body(bad, "out")); code != 422 || !strings.Contains(out["error"].(string), "step by_region: group by: no column week") {
		t.Errorf("a preview after a bad step: %d %v", code, out)
	}

	// Saved: a branch of its own, the models with headers, sources and
	// schema, the DAG, a dbt project (the repo had none), a merge request.
	if code, _ := call("alice", "PUT", "/api/projects/sales/flows/weekly", body(f, "")); code != 403 {
		t.Errorf("a reader saving: %d", code)
	}
	if code, out := call("dana", "PUT", "/api/projects/sales/flows/weekly", body(bad, "")); code != 422 || !strings.Contains(out["error"].(string), "can't be compiled") {
		t.Errorf("saving a bad flow: %d %v", code, out)
	}
	code, out = call("dana", "PUT", "/api/projects/sales/flows/weekly", body(f, ""))
	if code != 200 || out["branch"] != "storscale/flow-weekly" || out["new"] != true || out["dag"] != "flow_weekly" {
		t.Fatalf("saved: %d %v", code, out)
	}
	c := gl.commits[0]
	if c["start_branch"] != "main" {
		t.Errorf("the commit: %v", c)
	}
	written := map[string]string{}
	for _, a := range c["actions"].([]any) {
		act := a.(map[string]any)
		written[act["file_path"].(string)] = act["action"].(string) + ":" + act["content"].(string)
	}
	for _, fp := range []string{"dags/dbt/dbt_project.yml", "dags/dbt/models/flows/weekly/weekly__orders.sql", "dags/dbt/models/flows/weekly/weekly__real.sql",
		"dags/dbt/models/flows/weekly/weekly__by_region.sql", "dags/dbt/models/flows/weekly/revenue_by_region.sql", "dags/dbt/models/flows/weekly/sources.yml",
		"dags/dbt/models/flows/weekly/schema.yml", "dags/flow_weekly.py"} {
		if !strings.HasPrefix(written[fp], "create:") {
			t.Errorf("%s not created: %q", fp, written[fp])
		}
	}
	if dag := written["dags/flow_weekly.py"]; !strings.Contains(dag, `dbt.build(project="sales", path="dbt", select="flows.weekly")`) || !strings.Contains(dag, `schedule="0 6 * * 1"`) {
		t.Errorf("the DAG: %s", dag)
	}
	if m := written["dags/dbt/models/flows/weekly/weekly__real.sql"]; !strings.Contains(m, "-- storscale-step:\n--   kind: filter") || !strings.Contains(m, "{{ ref('weekly__orders') }}") {
		t.Errorf("the filter's model: %s", m)
	}
	if len(gl.mrs) != 1 || gl.mrs[0]["source_branch"] != "storscale/flow-weekly" || gl.mrs[0]["target_branch"] != "main" {
		t.Errorf("the merge request: %v", gl.mrs)
	}

	// Listed now (on its branch, unmerged), and read back from that branch as the same steps.
	code, out = call("alice", "GET", "/api/projects/sales/flows", "")
	if code != 200 || mustJSON(out["flows"]) != `[{"branch":"storscale/flow-weekly","mergeRequest":"https://gitlab.example/sales/pipelines/-/merge_requests/1","name":"weekly"}]` {
		t.Errorf("listed: %d %v", code, out)
	}
	code, out = call("alice", "GET", "/api/projects/sales/flows/weekly", "")
	if code != 200 || out["branch"] != "storscale/flow-weekly" || len(out["errors"].([]any)) != 0 {
		t.Fatalf("read back: %d %v", code, out)
	}
	var back flow.Flow
	json.Unmarshal([]byte(mustJSON(out["flow"])), &back)
	if len(back.Steps) != 4 || back.Steps[1].Kind != flow.Filter || back.Steps[1].Where != "not test" || back.Steps[1].Description != "No test rows" || back.Steps[0].X != 10 || back.Steps[3].Checks[0].Test != "not_null" {
		t.Errorf("read back as %s", mustJSON(back))
	}

	// bob changes a model by hand: a code step, its columns Trino's (asked as
	// the person), the steps after it typed from them; saved again, it's
	// written as it is, and a step dropped from the flow loses its model.
	key := "storscale/flow-weekly:dags/dbt/models/flows/weekly/weekly__real.sql"
	gl.contents[key] = strings.Replace(gl.contents[key], "where not test", "where not test and amount > 0", 1)
	ran = nil
	code, out = call("dana", "GET", "/api/projects/sales/flows/weekly", "")
	if code != 200 || len(out["errors"].([]any)) != 2 { // the group step, and the output after it
		t.Fatalf("read with a code step: %d %v", code, out)
	}
	json.Unmarshal([]byte(mustJSON(out["flow"])), &back)
	if back.Steps[1].Kind != flow.Code || !strings.Contains(back.Steps[1].SQL, "amount > 0") || back.Steps[1].Inputs[0] != "orders" || back.Steps[2].Kind != flow.Group {
		t.Errorf("the code step: %s", mustJSON(back.Steps[1]))
	}
	if len(ran) != 1 || !strings.Contains(ran[0], "amount > 0") || !strings.Contains(ran[0], "limit 0") {
		t.Errorf("Trino asked about the code step: %v", ran)
	}
	steps = out["steps"].(map[string]any)
	if cols := mustJSON(steps["real"].(map[string]any)["columns"]); cols != `[{"name":"order_id","type":"bigint"},{"name":"amount","type":"decimal(10,2)"}]` {
		t.Errorf("the code step's columns, Trino's: %s", cols)
	}
	if e := steps["by_region"].(map[string]any)["error"]; e != "group by: no column region in its input (it has order_id, amount)" {
		t.Errorf("after the code step: %v", e)
	}
	back.Steps[2] = flow.Step{ID: "by_region", Kind: flow.Group, Inputs: []string{"real"}, Aggregates: []flow.Aggregate{{Function: "sum", Column: "amount"}}}
	back.Steps[3].Checks = nil // region is gone
	code, out = call("dana", "PUT", "/api/projects/sales/flows/weekly", body(back, ""))
	if code != 200 || out["new"] != false {
		t.Fatalf("saved again: %d %v", code, out)
	}
	c = gl.commits[1]
	written = map[string]string{}
	for _, a := range c["actions"].([]any) {
		act := a.(map[string]any)
		content, _ := act["content"].(string)
		written[act["file_path"].(string)] = act["action"].(string) + ":" + content
	}
	if c["start_branch"] != nil || !strings.HasPrefix(written["dags/dbt/models/flows/weekly/weekly__real.sql"], "update:-- storscale-step:") || !strings.Contains(written["dags/dbt/models/flows/weekly/weekly__real.sql"], "amount > 0") {
		t.Errorf("the code step saved: %q", written["dags/dbt/models/flows/weekly/weekly__real.sql"])
	}
	if _, has := written["dags/dbt/dbt_project.yml"]; has {
		t.Error("dbt_project.yml written again")
	}
	if len(gl.mrs) != 1 {
		t.Errorf("%d merge requests", len(gl.mrs))
	}
	// A step dropped: its model deleted.
	gone := back
	gone.Steps = []flow.Step{back.Steps[0], {ID: "out", Kind: flow.Output, Inputs: []string{"orders"}, Table: "revenue_by_region"}}
	call("dana", "PUT", "/api/projects/sales/flows/weekly", body(gone, ""))
	deleted := []string{}
	for _, a := range gl.commits[2]["actions"].([]any) {
		if act := a.(map[string]any); act["action"] == "delete" {
			deleted = append(deleted, act["file_path"].(string))
		}
	}
	if mustJSON(deleted) != `["dags/dbt/models/flows/weekly/weekly__by_region.sql","dags/dbt/models/flows/weekly/weekly__real.sql"]` {
		t.Errorf("deleted: %v", deleted)
	}
	if !gl.token {
		t.Error("a call to GitLab without the repo's token")
	}
}
