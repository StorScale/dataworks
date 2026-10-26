package reconcile

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/pipelines"
	"github.com/StorScale/dataworks/internal/project"
)

func TestScheduledAgents(t *testing.T) {
	ctx := context.Background()
	af := &fakeAirflow{conns: map[string]pipelines.Connection{}}
	as := httptest.NewServer(af)
	defer as.Close()
	p := sales(t, `{"serviceAccount": "airflow-pipelines", "repo": "https://gitlab.example.com/sales/dags.git", "path": "dags"}`)
	p.Spec.Agents = []project.Agent{
		{Name: "nightly", Owner: "bob", Tools: []string{"list_metrics"}, Schedule: "0 6 * * *", Notebook: "notebooks/nightly.ipynb", ApprovedBy: "alice", ApprovedAt: time.Now()},
		{Name: "draft", Owner: "bob", Schedule: "0 6 * * *", Notebook: "notebooks/draft.ipynb"}, // not approved: no DAG
		{Name: "outside", Owner: "bob", ApprovedBy: "alice", ApprovedAt: time.Now()},            // driven from outside: no DAG
	}
	st := &memStore{secrets: map[string]map[string]string{
		"sales/sales-agent-nightly": {"clientId": "agent-sales-nightly", "secret": "s3cret", "llmKey": "sk-1", "llmUrl": "http://llm:4000"},
	}, statuses: []*project.Status{{Name: "sales", Phase: project.Ready, Spec: p, Parts: []project.Part{{System: "agents", OK: true}}}}}
	r := &Reconciler{Store: st, Airflow: &Airflow{API: pipelines.New(as.URL+"/pipelines", "storscale-platform", "pw"), DefaultRef: "main"}, PlatformURL: "https://data.example.com"}

	if err := r.applyAgents(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	c, ok := af.conns["agent_sales_nightly"]
	if !ok || len(af.conns) != 1 || c.Login != "agent-sales-nightly" || c.Password != "s3cret" {
		t.Fatalf("the agent's connection: %+v", af.conns)
	}
	var extra map[string]any
	json.Unmarshal([]byte(c.Extra), &extra)
	if extra["mcp_url"] != "https://data.example.com/mcp" || extra["llm_key"] != "sk-1" || extra["owner"] != "bob" {
		t.Fatalf("its extra: %v", extra)
	}

	// Listed for the DAG file: the one that runs, with the repo and the DAG's name.
	listed, err := r.Agents(ctx)
	if err != nil || len(listed) != 1 || listed[0].DAG != "agent_sales_nightly" || listed[0].ConnID != "agent_sales_nightly" ||
		listed[0].GitConnID != "git_sales" || listed[0].Ref != "main" || listed[0].Path != "dags" || listed[0].Agent.Notebook != "notebooks/nightly.ipynb" {
		t.Fatalf("listed: %+v %v", listed, err)
	}

	// Its secret not kept yet: said, the others untouched.
	before := *p
	p2 := *p
	p2.Spec.Agents = append([]project.Agent{{Name: "late", Owner: "bob", Schedule: "0 7 * * *", Notebook: "n.ipynb", ApprovedBy: "alice", ApprovedAt: time.Now()}}, p.Spec.Agents...)
	if err := r.applyAgents(ctx, &p2, &before); err == nil || !strings.Contains(err.Error(), "agent late: its secret isn't kept yet") {
		t.Fatalf("a secret not kept: %v", err)
	}

	// No longer scheduled: its connection goes.
	p3 := *p
	p3.Spec.Agents = []project.Agent{{Name: "nightly", Owner: "bob", ApprovedBy: "alice", ApprovedAt: time.Now()}}
	if err := r.applyAgents(ctx, &p3, &before); err != nil || len(af.conns) != 0 {
		t.Fatalf("unscheduled: %v, left %v", err, af.conns)
	}
	if err := r.removeAgents(ctx, p); err != nil { // the project gone: nothing left to delete is no error
		t.Fatal(err)
	}
}
