package main

import (
	"context"
	"testing"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// secrets is a project store with Secrets only.
type secrets struct {
	store.Store
	data map[string]map[string]string
}

func (s *secrets) Secret(_ context.Context, name string) (map[string]string, error) {
	return s.data[name], nil
}

func TestAProjectsPipelinesAreInItsOwnAirflow(t *testing.T) {
	parse := func(pipelines string) *projectView {
		p, err := project.Parse([]byte(`{"metadata": {"name": "sales"}, "spec": {"members": [{"group": "engineers", "role": "editor"}],
			"pipelines": ` + pipelines + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		return &projectView{Name: "sales", Spec: p}
	}
	st := &secrets{data: map[string]map[string]string{}}
	shared := pipelines.New("http://airflow:8090/pipelines", "storscale-platform", "pw")
	s := &server{cfg: config{ProjectAirflows: true}, store: st, airflow: shared}
	ctx := context.Background()

	if af, ui, err := s.pipelinesOf(ctx, parse(`{"serviceAccount": "airflow-pipelines"}`)); af != shared || ui != "" || err != nil {
		t.Errorf("no repo: %v %q %v, want the shared Airflow", af, ui, err)
	}
	repo := parse(`{"serviceAccount": "airflow-pipelines", "repo": "https://git.example.com/sales.git"}`)
	if _, _, err := s.pipelinesOf(ctx, repo); err == nil {
		t.Error("its Airflow not made yet: want an error")
	}
	st.data["project-sales-airflow"] = map[string]string{"PLATFORM_PASSWORD": "first"}
	af, ui, err := s.pipelinesOf(ctx, repo)
	if err != nil || af.URL != "http://project-sales-airflow:8090/airflow/sales" || af.Password != "first" || ui != "/airflow/sales/" {
		t.Fatalf("its own: %+v %q %v", af, ui, err)
	}
	if again, _, _ := s.pipelinesOf(ctx, repo); again != af {
		t.Error("the same client again: its token is kept")
	}
	st.data["project-sales-airflow"]["PLATFORM_PASSWORD"] = "second" // made again
	if again, _, _ := s.pipelinesOf(ctx, repo); again == af || again.Password != "second" {
		t.Error("a new password: want a new client")
	}
	s.cfg.ProjectAirflows = false // Compose: one shared Airflow
	if af, _, _ := s.pipelinesOf(ctx, repo); af != shared {
		t.Error("in Compose, a repo's pipelines are in the shared Airflow")
	}
}
