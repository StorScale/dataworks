package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/project"
)

// What an agent may read of its project (use case 12.2), by its own token
// (its Keycloak client's, as Bearer): its pipelines' runs and its alerts,
// read-only, as the Pipelines tab and the project's alerts show them to its
// people. The MCP server's list_runs and list_alerts come here with the
// agent's token; a person's token works the same way.

// agentProject is the project an agent's token may read: one it's a member of.
func (s *server) agentProject(w http.ResponseWriter, r *http.Request) (*projectView, string) {
	who, err := s.bearer(r)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, err.Error())
		return nil, ""
	}
	if s.store == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "projects aren't set up")
		return nil, ""
	}
	name := r.PathValue("name")
	st, err := s.store.Status(r.Context(), name)
	if err != nil || st == nil || st.Spec == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return nil, ""
	}
	role := ""
	for _, m := range st.Members {
		if m.Username == who {
			role = m.Role
		}
	}
	if role == "" {
		s.recordRefusal(r, who, "agent.read", "project:"+name, "not a member")
		s.fail(w, r, http.StatusForbidden, who+" isn't a member of "+name)
		return nil, ""
	}
	return &projectView{Name: name, Spec: st.Spec, Status: st, Role: role}, who
}

// handleAgentRuns are a project's pipelines and their latest runs, newest
// first, to an agent: what ran, when, and how it ended.
func (s *server) handleAgentRuns(w http.ResponseWriter, r *http.Request) {
	v, _ := s.agentProject(w, r)
	if v == nil {
		return
	}
	if s.airflow == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pipelines": []any{}, "note": "pipelines aren't set up here"})
		return
	}
	limit := 20
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	af, _, err := s.pipelinesOf(r.Context(), v)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	ps, err := af.Of(r.Context(), v.Name, 5)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if af != s.airflow { // its imports run in the platform's Airflow
		if shared, err := s.airflow.Of(r.Context(), v.Name, 5); err == nil {
			ps = append(ps, shared...)
		}
	}
	type run struct {
		Pipeline string `json:"pipeline"`
		Run      string `json:"run"`
		State    string `json:"state"`
		Type     string `json:"type"`
		Started  string `json:"started"`
		Ended    string `json:"ended,omitempty"`
	}
	runs := []run{}
	names := []string{}
	for _, p := range ps {
		names = append(names, p.ID)
		for _, x := range p.Runs {
			ended := ""
			if !x.Ended.IsZero() {
				ended = x.Ended.UTC().Format("2006-01-02T15:04:05Z")
			}
			runs = append(runs, run{Pipeline: p.ID, Run: x.ID, State: x.State, Type: x.Type, Started: x.Started.UTC().Format("2006-01-02T15:04:05Z"), Ended: ended})
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Started > runs[j].Started })
	if len(runs) > limit {
		runs = runs[:limit]
	}
	out := map[string]any{"pipelines": names, "runs": runs}
	if pl := v.Spec.Spec.Pipelines; pl != nil && pl.Repo != "" {
		if errs, err := af.ImportErrors(r.Context(), v.Name); err == nil && len(errs) > 0 {
			out["importErrors"] = errs
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAgentAlerts are a project's issues now, to an agent: failed runs
// and checks, stale tables, products off their contracts, budgets.
func (s *server) handleAgentAlerts(w http.ResponseWriter, r *http.Request) {
	v, _ := s.agentProject(w, r)
	if v == nil {
		return
	}
	out := []issue{}
	checked := false
	if pa := s.projectAlerts; pa != nil {
		pa.mu.Lock()
		checked = pa.checked
		for _, is := range pa.issues[v.Name] {
			if is.Kind == "" || is.Key == "" {
				continue
			}
			out = append(out, is)
		}
		pa.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeJSON(w, http.StatusOK, map[string]any{"checked": checked, "issues": out, "phase": v.Status.Phase})
}

var _ = project.Reader // the view's role is the member's: reader, editor, pipelines or agent

// handleAgentAsk asks the gateway one question on the project's key, for a
// member (its bearer token): semanticd's plain-language tests (12.3) ask
// through it, so the project's key stays with the platform. The gateway's
// guardrail and the project's budget apply; a refusal is said as it came.
func (s *server) handleAgentAsk(w http.ResponseWriter, r *http.Request) {
	v, who := s.agentProject(w, r)
	if v == nil {
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Prompt) == "" {
		s.fail(w, r, http.StatusBadRequest, "a prompt, as JSON: {\"prompt\": ...}")
		return
	}
	conn, err := s.store.Connection(r.Context(), v.Name, v.Name+"-llm")
	if err != nil || conn["llmKey"] == "" || conn["llmUrl"] == "" {
		s.fail(w, r, http.StatusUnprocessableEntity, "the project has no gateway key: give it an LLM budget (llm.budget in its spec) and the operator makes one")
		return
	}
	asker := &flow.GatewayAsker{URL: conn["llmUrl"], Key: conn["llmKey"], User: who}
	answer, cost, err := asker.Ask(r.Context(), in.Prompt)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, who, "llm.asked", "project:"+v.Name, fmt.Sprintf("%.4f spent", cost))
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "cost": cost})
}
