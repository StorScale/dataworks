package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
)

// An agent on a schedule (use case 12.2, second part) is a pipeline of the
// project's: the platform's DAG file (storscale_agents.py, in every
// Airflow) makes a DAG of each approved agent with a notebook and a
// schedule, which checks the project's repo out and runs the notebook as
// the agent (its Keycloak client, its gateway key), as 7.7 runs a notebook
// as the pipelines. The agent's client and key reach the DAG as the Airflow
// connection ConnID, which the operator keeps.

// A ProjectAgent is one agent the platform runs, as the DAG file lists them.
type ProjectAgent struct {
	Project   string        `json:"project"`
	Own       bool          `json:"own"`       // in the project's own Airflow, not the shared one
	ConnID    string        `json:"connId"`    // the agent's client and key
	GitConnID string        `json:"gitConnId"` // the repo's
	Repo      string        `json:"repo"`
	Ref       string        `json:"ref"`
	Path      string        `json:"path"` // the pipelines' folder in the repo, where the notebook's path starts
	DAG       string        `json:"dag"`
	Agent     project.Agent `json:"agent"`
}

// AgentConnID is the Airflow connection holding an agent's client and key.
func AgentConnID(p *project.Project, a project.Agent) string {
	return "agent_" + strings.ReplaceAll(p.Metadata.Name, "-", "_") + "_" + strings.ReplaceAll(a.Name, "-", "_")
}

// Agents are the applied projects' agents that run on a schedule: approved,
// with a notebook and a schedule, in a project with a repo.
func (r *Reconciler) Agents(ctx context.Context) ([]ProjectAgent, error) {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectAgent{}
	for _, st := range statuses {
		p := st.Spec
		if p == nil || st.Phase == project.Deleting {
			continue
		}
		hasRepo := p.Spec.Pipelines != nil && p.Spec.Pipelines.Repo != ""
		ready := false
		for _, pt := range st.Parts {
			ready = ready || pt.System == "agents" && pt.OK
		}
		if !ready {
			continue
		}
		for _, a := range p.Spec.Agents {
			if !a.Runs() {
				continue
			}
			pa := ProjectAgent{Project: p.Metadata.Name, Own: r.ownAirflow(p), ConnID: AgentConnID(p, a), DAG: a.DAG(p.Metadata.Name), Agent: a}
			if a.Notebook != "" { // a notebook runs from the repo; a prompt needs none
				if !hasRepo {
					continue
				}
				pl := p.Spec.Pipelines
				pa.GitConnID, pa.Repo, pa.Ref, pa.Path = p.GitConnID(), pl.Repo, pl.Ref, strings.Trim(pl.Path, "/")
				if pa.Ref == "" && r.Airflow != nil {
					pa.Ref = r.Airflow.DefaultRef
				}
			}
			out = append(out, pa)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnID < out[j].ConnID })
	return out, nil
}

// applyAgents gives the Airflow the project's DAGs run in each scheduled
// agent's client and gateway key (one connection each), and takes away
// those of agents gone, revoked or no longer scheduled.
func (r *Reconciler) applyAgents(ctx context.Context, p *project.Project, before *project.Project) error {
	api, err := r.projectAirflowAPI(ctx, p)
	if err != nil {
		return err
	}
	name := p.Metadata.Name
	var errs []error
	want := map[string]bool{}
	for _, a := range p.Spec.Agents {
		if !a.Runs() {
			continue
		}
		sec, err := r.Store.Connection(ctx, name, a.ConnectionName(name))
		if err == nil && (sec == nil || sec["secret"] == "") {
			err = errors.New("its secret isn't kept yet")
		}
		if err == nil {
			extra := map[string]any{"owner": a.Owner, "tools": a.Tools, "mcp_url": strings.TrimRight(r.PlatformURL, "/") + "/mcp",
				"llm_url": sec["llmUrl"], "llm_key": sec["llmKey"]}
			b, _ := json.Marshal(extra)
			err = api.PutConnection(ctx, pipelines.Connection{ID: AgentConnID(p, a), Type: "generic", Login: sec["clientId"], Password: sec["secret"], Extra: string(b),
				Description: "The agent " + a.Name + " of the project " + name + ": its client and its gateway key (the platform keeps it)"})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("agent %s: %w", a.Name, err))
			continue
		}
		want[AgentConnID(p, a)] = true
	}
	if before != nil {
		for _, a := range before.Spec.Agents {
			if id := AgentConnID(before, a); !want[id] {
				errs = append(errs, api.DeleteConnection(ctx, id))
			}
		}
	}
	return errors.Join(errs...)
}

// removeAgents takes a deleted project's agents' connections from the shared
// Airflow (a project's own Airflow goes with it, connections and all).
func (r *Reconciler) removeAgents(ctx context.Context, p *project.Project) error {
	if r.Airflow == nil || r.ownAirflow(p) {
		return nil
	}
	var errs []error
	for _, a := range p.Spec.Agents {
		errs = append(errs, r.Airflow.API.DeleteConnection(ctx, AgentConnID(p, a)))
	}
	return errors.Join(errs...)
}
