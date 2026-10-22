package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// A project's apps (use case 14.1, R14.1, R14.6): its editors declare them
// (project.App); its members open them. Every app's data is fetched here as
// whoever opens it, with their token: a metric app's through the semantic
// layer, a table app's as SQL in Trino, a question app's from its knowledge
// bank, so a reader sees what their rules let them see and nothing else.

const appRows = 500

// handleApps are the project's apps, to its members.
func (s *server) handleApps(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, role, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	apps := p.Spec.Apps
	if apps == nil {
		apps = []project.App{}
	}
	var banks []string
	for _, k := range p.Spec.Knowledge {
		banks = append(banks, k.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps, "canEdit": role == project.Editor, "banks": banks,
		"hasTables": p.Spec.Tables != nil, "hasBudget": p.Spec.LLM != nil && p.Spec.LLM.Budget > 0})
}

// handlePutApp makes an app of the project's, or changes one: its editors.
func (s *server) handlePutApp(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, "make its apps")
	if sess == nil {
		return
	}
	var a project.App
	dec := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		s.fail(w, r, http.StatusBadRequest, "an app, as JSON: "+err.Error())
		return
	}
	a.Name = r.PathValue("app")
	p := *v.Spec
	p.Spec.Apps = slices.Clone(p.Spec.Apps)
	i := slices.IndexFunc(p.Spec.Apps, func(x project.App) bool { return x.Name == a.Name })
	if i < 0 {
		p.Spec.Apps = append(p.Spec.Apps, a)
	} else {
		p.Spec.Apps[i] = a
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "app.saved", "project:"+v.Name+"/app:"+a.Name, map[bool]string{true: "made ", false: "changed "}[i < 0]+a.Kind+" app "+a.Name)
	writeJSON(w, http.StatusOK, a)
}

// handleDeleteApp takes an app out of the project: its editors.
func (s *server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, "remove its apps")
	if sess == nil {
		return
	}
	name := r.PathValue("app")
	p := *v.Spec
	before := len(p.Spec.Apps)
	p.Spec.Apps = slices.DeleteFunc(slices.Clone(p.Spec.Apps), func(x project.App) bool { return x.Name == name })
	if len(p.Spec.Apps) == before {
		s.fail(w, r, http.StatusNotFound, "no such app")
		return
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "app.removed", "project:"+v.Name+"/app:"+name, "")
	w.WriteHeader(http.StatusNoContent)
}

// appOf is the request's project and app, for a member.
func (s *server) appOf(w http.ResponseWriter, r *http.Request) (*project.Project, *project.App, string, string, bool) {
	who, token, ok := s.person(w, r)
	if !ok {
		return nil, nil, "", "", false
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return nil, nil, "", "", false
	}
	a := p.AppOf(r.PathValue("app"))
	if a == nil {
		s.fail(w, r, http.StatusNotFound, "no such app")
		return nil, nil, "", "", false
	}
	return p, a, who, token, true
}

// handleAppData is an app's rows, as the person opening it, with the filters they picked.
func (s *server) handleAppData(w http.ResponseWriter, r *http.Request) {
	p, a, _, token, ok := s.appOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Filters map[string]string `json:"filters"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			s.fail(w, r, http.StatusBadRequest, "the filters, as JSON: {\"filters\": {\"order__region\": \"EU\"}}")
			return
		}
	}
	picked := map[string]string{}
	for _, f := range a.Filters { // only the app's own filters, with a value picked
		if v := strings.TrimSpace(in.Filters[f.Field]); v != "" {
			if len(f.Values) > 0 && !slices.Contains(f.Values, v) {
				s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("%s is one of %s", firstOf(f.Label, f.Field), strings.Join(f.Values, ", ")))
				return
			}
			picked[f.Field] = v
		}
	}
	limit := a.Limit
	if limit <= 0 {
		limit = appRows
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	switch a.Kind {
	case "metric":
		q := map[string]any{"metrics": a.Metrics, "limit": limit}
		if a.By != "" {
			q["group_by"], q["order_by"] = []string{a.By}, []string{a.By}
		}
		var where []string
		for _, f := range a.Filters {
			if v, ok := picked[f.Field]; ok {
				where = append(where, fmt.Sprintf("{{ Dimension('%s') }} = %s", f.Field, sqlString(v)))
			}
		}
		if where != nil {
			q["where"] = where
		}
		var out map[string]any
		if code, err := s.semanticJSON(ctx, token, "/api/projects/"+p.Metadata.Name+"/semantic/query", q, &out); err != nil {
			s.fail(w, r, code, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "table":
		cat, ns := tablesOf(p)
		cols := "*"
		if len(a.Columns) > 0 {
			var q []string
			for _, c := range a.Columns {
				q = append(q, trino.Quote(c))
			}
			cols = strings.Join(q, ", ")
		}
		sql := "SELECT " + cols + " FROM " + trino.Quote(cat) + "." + trino.Quote(ns) + "." + trino.Quote(a.Table)
		var conds []string
		for _, f := range a.Filters {
			if v, ok := picked[f.Field]; ok {
				conds = append(conds, "CAST("+trino.Quote(f.Field)+" AS varchar) = "+sqlString(v))
			}
		}
		if conds != nil {
			sql += " WHERE " + strings.Join(conds, " AND ")
		}
		out, code, err := s.runSQL(ctx, token, p.Metadata.Name, sql, limit)
		if err != nil {
			s.fail(w, r, code, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		s.fail(w, r, http.StatusUnprocessableEntity, "a question app answers questions: ask it")
	}
}

// handleAppAsk answers a question app's question: its bank searched as the
// person, the closest passages given to the model (on the project's key,
// under the guardrail), which answers from them alone, citing them.
func (s *server) handleAppAsk(w http.ResponseWriter, r *http.Request) {
	p, a, who, token, ok := s.appOf(w, r)
	if !ok {
		return
	}
	if a.Kind != "qa" {
		s.fail(w, r, http.StatusUnprocessableEntity, "only a question app is asked")
		return
	}
	var in struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Question) == "" {
		s.fail(w, r, http.StatusBadRequest, "a question, as JSON: {\"question\": ...}")
		return
	}
	conn, err := s.store.Connection(r.Context(), p.Metadata.Name, p.Metadata.Name+"-llm")
	if err != nil || conn["llmKey"] == "" || conn["llmUrl"] == "" {
		s.fail(w, r, http.StatusUnprocessableEntity, "the project has no gateway key yet: give it an LLM budget (llm.budget)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	asker := &flow.GatewayAsker{URL: conn["llmUrl"], Key: conn["llmKey"], User: who}
	v, cost, err := asker.Embed(ctx, "", []string{in.Question})
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "the question: "+err.Error())
		return
	}
	tc, err := s.trinoAs(token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "Trino: "+err.Error())
		return
	}
	cat, ns := tablesOf(p)
	table := trino.Quote(cat) + "." + trino.Quote(ns) + "." + trino.Quote(project.KnowledgeBank{Name: a.Bank}.KnowledgeTable())
	rows, err := tc.Exec(ctx, fmt.Sprintf("SELECT source, ref, text FROM %s ORDER BY cosine_similarity(embedding, %s) DESC LIMIT 5", table, sqlVector(v[0])))
	if err != nil {
		why := err.Error()
		if strings.Contains(why, "does not exist") {
			why = "the bank " + a.Bank + " isn't built yet: build it on the Agents tab's Knowledge card"
		}
		s.fail(w, r, http.StatusUnprocessableEntity, why)
		return
	}
	passages := []knowledgeHit{}
	var b strings.Builder
	fmt.Fprintf(&b, "Answer the question from the passages below alone, citing each passage you use by its number, as [1]. "+
		"If they don't answer it, say so in a sentence; don't guess.\n\nQuestion: %s\n\nPassages:\n", in.Question)
	for i, row := range rows {
		h := knowledgeHit{}
		h.Source, _ = row[0].(string)
		h.Ref, _ = row[1].(string)
		h.Text, _ = row[2].(string)
		passages = append(passages, h)
		fmt.Fprintf(&b, "[%d] (%s %s) %s\n", i+1, h.Source, h.Ref, h.Text)
	}
	if len(passages) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"answer": "The bank has nothing to answer from yet.", "passages": passages, "cost": cost})
		return
	}
	answer, c2, err := asker.Ask(ctx, b.String())
	cost += c2
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "asking the model: "+err.Error())
		return
	}
	s.record(r, who, "app.asked", "project:"+p.Metadata.Name+"/app:"+a.Name, fmt.Sprintf("%d passages from %s, %.6f spent", len(passages), a.Bank, cost))
	writeJSON(w, http.StatusOK, map[string]any{"answer": strings.TrimSpace(answer), "passages": passages, "cost": cost})
}
