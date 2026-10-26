package reconcile

import (
	"context"
	"sort"
	"strings"

	"github.com/StorScale/dataworks/internal/project"
)

// A scoring (use case 15.1, R15.3) is a pipeline of the project's: the
// platform's DAG file (storscale_scoring.py, in every Airflow) makes a DAG of
// each, which runs its model's approved version over its table as the
// project's pipelines.

// A ProjectScoring is one scoring, as the DAG file lists them.
type ProjectScoring struct {
	Project   string          `json:"project"`
	Own       bool            `json:"own"` // in the project's own Airflow, not the shared one
	Catalog   string          `json:"catalog"`
	Namespace string          `json:"namespace"`
	DAG       string          `json:"dag"`
	Scoring   project.Scoring `json:"scoring"`
}

// Scorings are the applied projects' scorings.
func (r *Reconciler) Scorings(ctx context.Context) ([]ProjectScoring, error) {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectScoring{}
	for _, st := range statuses {
		p := st.Spec
		if p == nil || st.Phase == project.Deleting || p.Spec.Tables == nil || p.Spec.Pipelines == nil {
			continue
		}
		cat, ns := "iceberg", strings.ReplaceAll(p.Metadata.Name, "-", "_")
		if p.Spec.Tables.Catalog != "" {
			cat = p.Spec.Tables.Catalog
		}
		if p.Spec.Tables.Namespace != "" {
			ns = p.Spec.Tables.Namespace
		}
		for _, s := range p.Spec.Scoring {
			out = append(out, ProjectScoring{Project: p.Metadata.Name, Own: r.ownAirflow(p), Catalog: cat, Namespace: ns, DAG: s.DAG(p.Metadata.Name), Scoring: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DAG < out[j].DAG })
	return out, nil
}
