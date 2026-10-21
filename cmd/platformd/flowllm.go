package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// The llm step (R11.8): the platform asks the model about a column's rows
// through the gateway, on the project's key (its connection <project>-llm),
// and writes the answers as a table in the project's namespace, which the
// step's dbt model reads. A preview asks about a sample as the person; a
// run (the flow's DAG, as the project's pipelines, or an editor) asks about
// every row the input gives, up to the step's limit, and writes the table
// as the caller, signed in to Trino with their token (the pipelines own the
// project's tables; an editor may make them), under their rules.

const llmPreviewRows = 50

// llmAnswered is a preview's or a run's answer: the rows with the answer's column.
type llmAnswered struct {
	Columns []string
	Types   []string
	Result  *flow.LLMResult
	SQL     string
	Seconds float64
	Sampled []string
}

// askLLMStep reads the step's input (the steps before it, as the token's
// owner) and asks the model about each row.
func (s *server) askLLMStep(ctx context.Context, token, who string, p *project.Project, c *flow.Compiled, st *flow.Step, limit int, sample flow.Sampler) (*llmAnswered, int, error) {
	if s.store == nil {
		return nil, http.StatusServiceUnavailable, errorString("the platform keeps no connections here")
	}
	conn, err := s.store.Connection(ctx, p.Metadata.Name, p.Metadata.Name+"-llm")
	if err != nil || conn["llmKey"] == "" || conn["llmUrl"] == "" {
		return nil, http.StatusUnprocessableEntity, errorString("the project has no gateway key: give it an LLM budget (llm.budget in its spec) and the operator makes one")
	}
	sql, err := c.Preview(st.Inputs[0], sample, limit)
	if err != nil {
		return nil, http.StatusUnprocessableEntity, err
	}
	started := time.Now()
	ans, code, err := s.runSQL(ctx, token, p.Metadata.Name, sql, limit)
	if err != nil {
		return nil, code, err
	}
	asker := &flow.GatewayAsker{URL: conn["llmUrl"], Key: conn["llmKey"], User: who}
	if st.Mode == "nearest" {
		res, err := s.nearestPassages(ctx, token, p, asker, st, ans.Columns, ans.Rows)
		if err != nil {
			return nil, http.StatusUnprocessableEntity, err
		}
		return &llmAnswered{Columns: append(append([]string{}, ans.Columns...), st.Name, st.Name+"_source"),
			Types:  append(append([]string{}, ans.Types...), "varchar", "varchar"),
			Result: res, SQL: sql, Seconds: time.Since(started).Seconds(), Sampled: sampled(c, st.Inputs[0], sample)}, 200, nil
	}
	res, err := flow.LLMApply(ctx, st, ans.Columns, ans.Rows, asker)
	if err != nil {
		why := err.Error()
		if strings.Contains(why, "guardrail") {
			why += " (mask such values with a prepare step first)"
		}
		return nil, http.StatusUnprocessableEntity, errorString("asking the model: " + why)
	}
	return &llmAnswered{Columns: append(append([]string{}, ans.Columns...), st.Name), Types: append(append([]string{}, ans.Types...), "varchar"),
		Result: res, SQL: sql, Seconds: time.Since(started).Seconds(), Sampled: sampled(c, st.Inputs[0], sample)}, 200, nil
}

// handleFlowLLMRun asks the model about the step's input, every row up to
// the step's limit, and writes the answers as the step's table: the flow's
// DAG calls it as the project's pipelines before dbt builds; an editor may too.
func (s *server) handleFlowLLMRun(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.person(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := r.PathValue("name")
	st, err := s.store.Status(ctx, name)
	if err != nil || st == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return
	}
	allowed := false
	for _, m := range st.Members {
		if m.Username == who && (m.Role == "editor" || m.Role == "pipelines") {
			allowed = true
		}
	}
	if sess := s.session(r); sess != nil && s.isAdmin(sess.User) {
		allowed = true
	}
	if !allowed {
		s.recordRefusal(r, who, "flow.llm", "project:"+name, "not an editor or the project's pipelines")
		s.fail(w, r, http.StatusForbidden, "the project's editors and its pipelines run a step's model")
		return
	}
	p, err := s.store.Project(ctx, name)
	if err != nil || p == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return
	}
	tc, err := trino.New(envOr("TRINO_URL", "https://trino:8443"), "", "", os.Getenv("TRINO_CA"))
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "Trino: "+err.Error())
		return
	}
	flowName, stepID := r.PathValue("flow"), r.PathValue("step")
	if !flow.NameOK(flowName) || !flow.NameOK(stepID) {
		s.fail(w, r, http.StatusNotFound, "no such flow or step")
		return
	}
	gl, target, err := s.repoOf(r, p)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if target == "" {
		if target, err = gl.DefaultBranch(ctx); err != nil {
			s.fail(w, r, http.StatusBadGateway, err.Error())
			return
		}
	}
	f, _, err := s.readFlow(ctx, gl, p, target, flowName)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if f == nil {
		s.fail(w, r, http.StatusNotFound, "no flow "+flowName+" in the repo")
		return
	}
	step := f.Step(stepID)
	if step == nil || step.Kind != flow.LLM {
		s.fail(w, r, http.StatusUnprocessableEntity, "the step isn't the flow's llm step")
		return
	}
	c, err := s.compileFlow(ctx, token, p, f)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if res := c.Steps[stepID]; res != nil && res.Error != "" {
		s.fail(w, r, http.StatusUnprocessableEntity, "the step can't be compiled: "+res.Error)
		return
	}
	limit := step.Limit
	if limit <= 0 {
		limit = 1000
	}
	a, code, err := s.askLLMStep(ctx, token, who, p, c, step, limit, func(_, _, _ string) float64 { return 100 })
	if err != nil {
		s.fail(w, r, code, err.Error())
		return
	}
	table := trino.Quote(step.Catalog) + "." + trino.Quote(step.Namespace) + "." + trino.Quote(step.Table)
	if err := writeTable(ctx, tc.As(token), table, a.Columns, a.Types, a.Result.Rows); err != nil {
		s.fail(w, r, http.StatusBadGateway, "writing "+table+": "+err.Error())
		return
	}
	if s.catalog != nil { // its lineage: the flow's tables upstream of the step, once the catalog has described the new table
		var from []string
		for _, id := range c.Order {
			if in := f.Step(id); in.Kind == flow.Input && (in.ID == step.Inputs[0] || feeds(f, step.Inputs[0], in.ID)) {
				from = append(from, s.catalog.Service+"."+in.Catalog+"."+in.Namespace+"."+in.Table)
			}
		}
		to := s.catalog.Service + "." + step.Catalog + "." + step.Namespace + "." + step.Table
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			for {
				err := s.catalog.AddLineage(ctx, from, to, "Asked of a model by the flow "+flowName+"'s step "+stepID)
				if err == nil {
					return
				}
				select {
				case <-ctx.Done():
					s.log.Warn("llm step's lineage", "table", to, "error", err)
					return
				case <-time.After(20 * time.Second):
				}
			}
		}()
	}
	s.record(r, who, "flow.llm", "project:"+name+"/flow:"+flowName+"/step:"+stepID,
		fmt.Sprintf("%d rows asked in %d questions, %.4f spent, %d answers unread", len(a.Result.Rows), a.Result.Batches, a.Result.Cost, a.Result.Unread))
	s.log.Info("llm step", "project", name, "flow", flowName, "step", stepID, "user", who, "rows", len(a.Result.Rows), "batches", a.Result.Batches, "cost", a.Result.Cost, "unread", a.Result.Unread)
	writeJSON(w, http.StatusOK, map[string]any{"table": step.Catalog + "." + step.Namespace + "." + step.Table, "rows": len(a.Result.Rows), "batches": a.Result.Batches,
		"cost": a.Result.Cost, "unread": a.Result.Unread, "warnings": a.Result.Warnings})
}

// writeTable makes the table from the rows, replacing what was there: the
// first rows make it, the rest are inserted, two hundred a statement.
func writeTable(ctx context.Context, t *trino.Client, table string, columns, types []string, rows [][]any) error {
	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = trino.Quote(c)
	}
	as := " as v (" + strings.Join(cols, ", ") + ")"
	if len(rows) == 0 {
		nulls := make([]any, len(columns))
		_, err := t.Exec(ctx, "create or replace table "+table+" as select * from (values "+valuesRow(nulls, types)+")"+as+" where false")
		return err
	}
	const batch = 200
	for start := 0; start < len(rows); start += batch {
		end := min(start+batch, len(rows))
		var vals []string
		for _, row := range rows[start:end] {
			vals = append(vals, valuesRow(row, types))
		}
		stmt := "insert into " + table + " select * from (values " + strings.Join(vals, ", ") + ")" + as
		if start == 0 {
			stmt = "create or replace table " + table + " as select * from (values " + strings.Join(vals, ", ") + ")" + as
		}
		if _, err := t.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// valuesRow is one row of a VALUES list, each value cast to its column's type.
func valuesRow(row []any, types []string) string {
	parts := make([]string, len(row))
	for i, v := range row {
		typ := "varchar"
		if i < len(types) && types[i] != "" {
			typ = types[i]
		}
		var lit string
		switch x := v.(type) {
		case nil:
			lit = "null"
		case bool:
			lit = strconv.FormatBool(x)
		case float64:
			lit = strconv.FormatFloat(x, 'f', -1, 64)
		case int, int64:
			lit = fmt.Sprint(x)
		case string:
			lit = "'" + strings.ReplaceAll(x, "'", "''") + "'"
		default:
			lit = "'" + strings.ReplaceAll(fmt.Sprint(x), "'", "''") + "'"
		}
		parts[i] = "cast(" + lit + " as " + typ + ")"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
