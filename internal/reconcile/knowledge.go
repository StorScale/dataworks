package reconcile

import (
	"context"
	"sort"

	"github.com/StorScale/storscale-platform/internal/project"
)

// A knowledge bank on a schedule (use case 12.6) is a pipeline of the
// project's: the platform's DAG file (storscale_knowledge.py, in every
// Airflow) makes a DAG of each bank with a schedule, which asks platformd
// to build it afresh as the project's pipelines.

// A ProjectBank is one bank built on a schedule, as the DAG file lists them.
type ProjectBank struct {
	Project string                `json:"project"`
	Own     bool                  `json:"own"` // in the project's own Airflow, not the shared one
	DAG     string                `json:"dag"`
	Bank    project.KnowledgeBank `json:"bank"`
}

// KnowledgeBanks are the applied projects' banks with a schedule.
func (r *Reconciler) KnowledgeBanks(ctx context.Context) ([]ProjectBank, error) {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectBank{}
	for _, st := range statuses {
		p := st.Spec
		if p == nil || st.Phase == project.Deleting {
			continue
		}
		for _, k := range p.Spec.Knowledge {
			if k.Schedule != "" && p.Spec.Pipelines != nil {
				out = append(out, ProjectBank{Project: p.Metadata.Name, Own: r.ownAirflow(p), DAG: k.DAG(p.Metadata.Name), Bank: k})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DAG < out[j].DAG })
	return out, nil
}
