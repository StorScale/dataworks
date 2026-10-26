package main

import (
	"context"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
)

// Run now on an import waits for the operator to apply its latest change:
// Airflow's DAG is made from the spec the operator applied.
func TestRunNowWaitsForAnImportsChangeToBeApplied(t *testing.T) {
	parse := func(tables string) *project.Project {
		p, err := project.Parse([]byte(`{"metadata":{"name":"retail"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{},"imports":[{"name":"uploads","source":"files","files":{"tables":[` + tables + `]}}]}}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := parse(`{"name":"orders","file":"orders.csv"}`)
	after := parse(`{"name":"orders","file":"orders.csv"},{"name":"reviews","file":"reviews.csv"}`)
	st := &statuses{list: []*project.Status{{Name: "retail", Spec: before}}}
	s := &server{store: st}
	v := &projectView{Name: "retail", Spec: after}

	if s.importApplied(context.Background(), v, "import_retail_uploads", 0) {
		t.Error("the operator hasn't applied the table added: want false")
	}
	if !s.importApplied(context.Background(), v, "flow_campaign_results", 0) {
		t.Error("a pipeline that isn't an import: want true")
	}
	st.list = []*project.Status{{Name: "retail", Spec: parse(`{"name":"orders","file":"orders.csv"},{"name":"reviews","file":"reviews.csv"}`)}}
	if !s.importApplied(context.Background(), v, "import_retail_uploads", 0) {
		t.Error("applied: want true")
	}
}
