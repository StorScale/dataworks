package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/gitlab"
	"github.com/StorScale/storscale-platform/internal/project"
)

// Flows are pipelines built on the canvas (use cases 11.1 and 11.2): dbt
// models in the project's repo under <dags>/dbt/models/flows/<flow>/, which
// the canvas reads and writes. Opening one reads its folder (from its own
// branch while a save awaits review, else the branch the pipelines follow)
// and compiles it: each step's columns and SQL, or why it can't be. A
// preview runs a step's query, on a sample of each input, in Trino as the
// person (through semanticd, as the SQL page runs). Saving compiles the
// flow, commits its files and the DAG that builds it on a branch of its own
// (storscale/flow-<name>), and opens a merge request, as a scheduled
// notebook is (7.7).

const previewRows = 50_000 // a preview reads about this many rows of each input

// flowSQL is what semanticd answers a SQL statement with.
type flowSQL struct {
	Columns []string `json:"columns"`
	Types   []string `json:"types"`
	Rows    [][]any  `json:"rows"`
	Seconds float64  `json:"seconds"`
}

// flowPerson is who asks, with the Keycloak token the preview runs with:
// the session's (refreshed) or the request's own Bearer token.
func (s *server) flowPerson(w http.ResponseWriter, r *http.Request) (who, token string, ok bool) {
	if raw, has := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); has && raw != "" {
		who, err := s.bearer(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return "", "", false
		}
		return who, raw, true
	}
	sess := s.member(w, r)
	if sess == nil {
		return "", "", false
	}
	if r.Method != http.MethodGet && r.Header.Get("X-Platform-Request") != "1" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-Platform-Request"})
		return "", "", false
	}
	if sess.Token != nil {
		tok, err := s.oauth.TokenSource(r.Context(), sess.Token).Token()
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
			return "", "", false
		}
		if tok.AccessToken != sess.Token.AccessToken {
			s.mu.Lock()
			sess.Token = tok
			s.mu.Unlock()
		}
		token = tok.AccessToken
	}
	return sess.User.Username, token, true
}

// flowProject is the project a flow request is about, and the person's role
// in it (an administrator's session counts as an editor's); it answers when
// they're not a member.
func (s *server) flowProject(w http.ResponseWriter, r *http.Request, who string) (*project.Project, string, bool) {
	if s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "projects aren't set up"})
		return nil, "", false
	}
	name := r.PathValue("name")
	st, err := s.store.Status(r.Context(), name)
	if err != nil || st == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such project"})
		return nil, "", false
	}
	role := ""
	for _, m := range st.Members {
		if m.Username == who && m.Role != "pipelines" {
			role = m.Role
		}
	}
	if sess := s.session(r); sess != nil && s.isAdmin(sess.User) {
		role = project.Editor
	}
	if role == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of the project"})
		return nil, "", false
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such project"})
		return nil, "", false
	}
	return p, role, true
}

// flowEnv is what the compiler knows of the project: its namespace, and the
// columns of the tables a flow may read (its own, and the products it
// consumes: a table it can't read isn't described either).
func (s *server) flowEnv(ctx context.Context, token string, p *project.Project) flow.Env {
	cat, ns := "iceberg", strings.ReplaceAll(p.Metadata.Name, "-", "_")
	if t := p.Spec.Tables; t != nil {
		if t.Catalog != "" {
			cat = t.Catalog
		}
		if t.Namespace != "" {
			ns = t.Namespace
		}
	}
	return flow.Env{Catalog: cat, Namespace: ns, MetricSQL: s.metricSQL(ctx, token, p.Metadata.Name), Columns: func(catalog, namespace, table string) ([]flow.Column, error) {
		if catalog != cat {
			return nil, fmt.Errorf("an input is a table of the lakehouse (%s), not of %s", cat, catalog)
		}
		if namespace != ns && !s.consumes(ctx, p.Metadata.Name, namespace, table) {
			return nil, fmt.Errorf("%s.%s isn't the project's, nor a product it consumes", namespace, table)
		}
		if s.tableColumns != nil {
			return s.tableColumns(ctx, namespace, table)
		}
		if s.history == nil {
			return nil, errors.New("the catalog isn't set up (no NESSIE_URL), so the table's columns can't be read")
		}
		cols, err := s.history.iceberg.TypedColumns(ctx, namespace, table)
		if err != nil {
			return nil, err
		}
		out := make([]flow.Column, len(cols))
		for i, c := range cols {
			out[i] = flow.Column{Name: c.Name, Type: c.Type}
		}
		return out, nil
	}}
}

// consumes says whether a project reads a table of another's namespace as
// a product it consumes (10.3).
func (s *server) consumes(ctx context.Context, consumer, namespace, table string) bool {
	specs, err := s.store.Projects(ctx)
	if err != nil {
		return false
	}
	for _, p := range specs {
		t := p.Spec.Tables
		if t == nil || t.Namespace != namespace {
			continue
		}
		for _, pr := range p.Spec.Products {
			if pr.Table == table && contains(pr.Consumers, consumer) {
				return true
			}
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// flowSampler says how much of a table a preview reads: all of a small
// one, about previewRows rows of a large one.
func (s *server) flowSampler(ctx context.Context) flow.Sampler {
	return func(_, namespace, table string) float64 {
		if s.history == nil {
			return 100
		}
		st, err := s.history.iceberg.Stats(ctx, namespace, table)
		if err != nil || st.Rows <= previewRows {
			return 100
		}
		p := float64(previewRows) / float64(st.Rows) * 100
		if p < 0.01 {
			p = 0.01
		}
		return float64(int(p*100)) / 100
	}
}

// semanticJSON calls semanticd as the person, with their token.
func (s *server) semanticJSON(ctx context.Context, token, path string, in any, out any) (int, error) {
	if s.cfg.SemanticURL == "" {
		return http.StatusServiceUnavailable, errors.New("the semantic layer isn't set up (no SEMANTIC_URL), which runs the previews")
	}
	if token == "" {
		return http.StatusUnauthorized, errors.New("no token to run it as you: sign in again")
	}
	body, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, "POST", s.cfg.SemanticURL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := semanticClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, errors.New("the semantic layer isn't answering")
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		return res.StatusCode, errors.New(e.Error)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return http.StatusBadGateway, err
	}
	return 200, nil
}

// runSQL runs a statement in Trino as the person, through semanticd.
func (s *server) runSQL(ctx context.Context, token, projectName, sql string, limit int) (*flowSQL, int, error) {
	var out flowSQL
	code, err := s.semanticJSON(ctx, token, "/api/projects/"+projectName+"/sql", map[string]any{"sql": sql, "limit": limit}, &out)
	if err != nil {
		return nil, code, err
	}
	return &out, 200, nil
}

// metricSQL gives a metric step its SQL: the semantic layer's for the
// query, as the person (its whole answer, uncapped: dbt materialises it),
// and its columns as Trino describes them. Without a token it can't be
// told yet.
func (s *server) metricSQL(ctx context.Context, token, projectName string) func(metrics, groupBy []string, where string) (string, []flow.Column, error) {
	return func(metrics, groupBy []string, where string) (string, []flow.Column, error) {
		if token == "" {
			return "", nil, nil
		}
		in := map[string]any{"metrics": metrics, "group_by": groupBy, "explain": true, "limit": 0}
		if strings.TrimSpace(where) != "" {
			in["where"] = []string{where}
		}
		var out struct {
			SQL string `json:"sql"`
		}
		if _, err := s.semanticJSON(ctx, token, "/api/projects/"+projectName+"/semantic/query", in, &out); err != nil {
			return "", nil, fmt.Errorf("the semantic layer: %v", err)
		}
		sql := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out.SQL), ";"))
		if sql == "" {
			return "", nil, errors.New("the semantic layer gave no SQL")
		}
		ans, _, err := s.runSQL(ctx, token, projectName, "select * from (\n"+sql+"\n) as metric_step where false", 0)
		if err != nil {
			return "", nil, fmt.Errorf("Trino: %v", err)
		}
		cols := make([]flow.Column, len(ans.Columns))
		for i, name := range ans.Columns {
			cols[i] = flow.Column{Name: name}
			if i < len(ans.Types) {
				cols[i].Type = ans.Types[i]
			}
		}
		return sql, cols, nil
	}
}

// compileFlow compiles a flow for a project, asking Trino (as the person,
// when a token is given) what each code step returns, so the steps after
// it know their columns.
func (s *server) compileFlow(ctx context.Context, token string, p *project.Project, f *flow.Flow) (*flow.Compiled, error) {
	env := s.flowEnv(ctx, token, p)
	c, err := flow.Compile(f, env)
	if err != nil || token == "" {
		return c, err
	}
	described := false
	for _, id := range c.Order {
		st := f.Step(id)
		if st.Kind != flow.Code || c.Steps[id].Error != "" {
			continue
		}
		sql, err := c.Preview(id, s.flowSampler(ctx), 0)
		if err != nil {
			continue
		}
		ans, _, err := s.runSQL(ctx, token, p.Metadata.Name, sql, 0)
		if err != nil {
			c.Steps[id].Error = "Trino: " + err.Error()
			continue
		}
		if env.CodeColumns == nil {
			env.CodeColumns = map[string][]flow.Column{}
		}
		for i, name := range ans.Columns {
			typ := ""
			if i < len(ans.Types) {
				typ = ans.Types[i]
			}
			env.CodeColumns[id] = append(env.CodeColumns[id], flow.Column{Name: name, Type: typ})
		}
		described = true
	}
	if !described {
		return c, nil
	}
	errs := map[string]string{}
	for id, r := range c.Steps {
		errs[id] = r.Error
	}
	c, err = flow.Compile(f, env)
	if err == nil {
		for id, e := range errs { // Trino's word on a code step stays
			if e != "" && c.Steps[id].Error == "" {
				c.Steps[id].Error = e
			}
		}
	}
	return c, err
}

// flowView is a flow as the canvas shows it: its steps, what the compiler
// made of each, and where it was read from.
type flowView struct {
	Flow         *flow.Flow                  `json:"flow"`
	Order        []string                    `json:"order"`
	Steps        map[string]*flow.StepResult `json:"steps"`
	Errors       []string                    `json:"errors"`
	Branch       string                      `json:"branch,omitempty"`       // the flow's own, while a save awaits review
	MergeRequest string                      `json:"mergeRequest,omitempty"` // its merge request
	DbtPath      string                      `json:"dbtPath,omitempty"`
}

func viewOf(c *flow.Compiled) flowView {
	v := flowView{Flow: c.Flow, Order: c.Order, Steps: c.Steps, Errors: c.Errors()}
	if v.Errors == nil {
		v.Errors = []string{}
	}
	return v
}

// dbtDir is the project's dbt project in its repo: <dags>/dbt.
func dbtDir(p *project.Project) string {
	return path.Join(strings.Trim(p.Spec.Pipelines.Path, "/"), "dbt")
}

func flowBranch(name string) string { return "storscale/flow-" + strings.ReplaceAll(name, "_", "-") }

// handleFlows lists the project's flows: the folders under models/flows on
// the branch the pipelines follow, and those saved on branches of their own
// and not merged yet.
func (s *server) handleFlows(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.flowPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.flowProject(w, r, who)
	if !ok {
		return
	}
	gl, target, err := s.repoOf(r, p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"flows": []any{}, "why": err.Error()})
		return
	}
	ctx := r.Context()
	if target == "" {
		if target, err = gl.DefaultBranch(ctx); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	}
	type item struct {
		Name         string `json:"name"`
		Branch       string `json:"branch,omitempty"`
		MergeRequest string `json:"mergeRequest,omitempty"`
	}
	byName := map[string]*item{}
	tree, err := gl.Tree(ctx, path.Join(dbtDir(p), "models", "flows"), target)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	prefix := path.Join(dbtDir(p), "models", "flows") + "/"
	for _, e := range tree {
		if e.Type == "tree" && !strings.Contains(strings.TrimPrefix(e.Path, prefix), "/") {
			n := strings.TrimPrefix(e.Path, prefix)
			byName[n] = &item{Name: n}
		}
	}
	branches, _ := gl.Branches(ctx, "storscale/flow-")
	for _, b := range branches {
		n := strings.ReplaceAll(strings.TrimPrefix(b, "storscale/flow-"), "-", "_")
		if !strings.HasPrefix(b, "storscale/flow-") || !flow.NameOK(n) {
			continue
		}
		it := byName[n]
		if it == nil {
			it = &item{Name: n}
			byName[n] = it
		}
		it.Branch = b
		if mrs, err := gl.OpenMergeRequests(ctx, target); err == nil {
			for _, mr := range mrs {
				if mr.SourceBranch == b {
					it.MergeRequest = mr.WebURL
				}
			}
		}
	}
	out := []item{}
	for _, it := range byName {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"flows": out, "dbtPath": dbtDir(p), "target": target})
}

// readFlow reads a flow's folder from the repo: its own branch when it has
// one, else the target.
func (s *server) readFlow(ctx context.Context, gl *gitlab.Client, p *project.Project, target, name string) (*flow.Flow, string, error) {
	ref := target
	branch := ""
	if has, err := gl.HasBranch(ctx, flowBranch(name)); err == nil && has {
		ref, branch = flowBranch(name), flowBranch(name)
	}
	dir := path.Join(dbtDir(p), "models", "flows", name)
	tree, err := gl.Tree(ctx, dir, ref)
	if err != nil {
		return nil, "", err
	}
	files := map[string]string{}
	for _, e := range tree {
		if e.Type != "blob" || !strings.HasPrefix(e.Path, dir+"/") {
			continue
		}
		rel := strings.TrimPrefix(e.Path, dir+"/")
		if strings.Contains(rel, "/") || !strings.HasSuffix(rel, ".sql") && rel != "sources.yml" && rel != "schema.yml" {
			continue
		}
		text, err := gl.File(ctx, e.Path, ref)
		if err != nil {
			return nil, "", err
		}
		files[rel] = text
	}
	if len(files) == 0 {
		return nil, "", nil
	}
	f, err := flow.Read(name, files)
	if err != nil {
		return nil, "", err
	}
	return f, branch, nil
}

// handleFlowView is one flow, read from the repo and compiled.
func (s *server) handleFlowView(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.flowPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.flowProject(w, r, who)
	if !ok {
		return
	}
	name := r.PathValue("flow")
	if !flow.NameOK(name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such flow"})
		return
	}
	gl, target, err := s.repoOf(r, p)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	ctx := r.Context()
	if target == "" {
		if target, err = gl.DefaultBranch(ctx); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	}
	f, branch, err := s.readFlow(ctx, gl, p, target, name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if f == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no flow " + name + " in the repo"})
		return
	}
	c, err := s.compileFlow(ctx, token, p, f)
	if err != nil {
		writeJSON(w, http.StatusOK, flowView{Flow: f, Steps: map[string]*flow.StepResult{}, Errors: []string{err.Error()}, Branch: branch, DbtPath: dbtDir(p)})
		return
	}
	v := viewOf(c)
	v.Branch, v.DbtPath = branch, dbtDir(p)
	if branch != "" {
		if mrs, err := gl.OpenMergeRequests(ctx, target); err == nil {
			for _, mr := range mrs {
				if mr.SourceBranch == branch {
					v.MergeRequest = mr.WebURL
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, v)
}

func readFlowBody(w http.ResponseWriter, r *http.Request) (*flow.Flow, string, int, bool) {
	var body struct {
		Flow  *flow.Flow `json:"flow"`
		Step  string     `json:"step"`
		Limit int        `json:"limit"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil || body.Flow == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a flow, as JSON"})
		return nil, "", 0, false
	}
	return body.Flow, body.Step, body.Limit, true
}

// handleFlowCompile compiles a flow as the canvas holds it: each step's
// columns and SQL, or why not. Nothing runs.
func (s *server) handleFlowCompile(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.flowPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.flowProject(w, r, who)
	if !ok {
		return
	}
	f, _, _, ok := readFlowBody(w, r)
	if !ok {
		return
	}
	c, err := s.compileFlow(r.Context(), token, p, f)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	v := viewOf(c)
	v.DbtPath = dbtDir(p)
	writeJSON(w, http.StatusOK, v)
}

// handleFlowPreview runs a step's query, on a sample of each input, as the person.
func (s *server) handleFlowPreview(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.flowPerson(w, r)
	if !ok {
		return
	}
	p, _, ok := s.flowProject(w, r, who)
	if !ok {
		return
	}
	f, step, limit, ok := readFlowBody(w, r)
	if !ok {
		return
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	ctx := r.Context()
	c, err := s.compileFlow(ctx, token, p, f)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	sql, err := c.Preview(step, s.flowSampler(ctx), limit)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	started := time.Now()
	ans, code, err := s.runSQL(ctx, token, p.Metadata.Name, sql, limit)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error(), "sql": sql})
		return
	}
	s.log.Info("flow preview", "project", p.Metadata.Name, "flow", f.Name, "step", step, "user", who, "rows", len(ans.Rows), "took", time.Since(started).Round(time.Millisecond))
	writeJSON(w, http.StatusOK, map[string]any{"sql": sql, "columns": ans.Columns, "types": ans.Types, "rows": ans.Rows, "seconds": ans.Seconds, "sampled": sampled(c, step, s.flowSampler(ctx))})
}

// sampled are the inputs a preview read a sample of, not all of.
func sampled(c *flow.Compiled, step string, sample flow.Sampler) []string {
	var out []string
	for _, id := range c.Order {
		st := c.Flow.Step(id)
		if st.Kind == flow.Input && (id == step || feeds(c.Flow, step, id)) && sample(st.Catalog, st.Namespace, st.Table) < 100 {
			out = append(out, st.Table)
		}
	}
	return out
}

func feeds(f *flow.Flow, to, from string) bool {
	for _, in := range f.Step(to).Inputs {
		if in == from || feeds(f, in, from) {
			return true
		}
	}
	return false
}

// handleSaveFlow writes the flow to the project's repo on a branch of its
// own, with a merge request into the branch the pipelines follow: its
// models (with their headers), sources and schema, the DAG that builds it,
// and a dbt project if the repo has none yet. Steps no longer in the flow
// lose their models. Editors only.
func (s *server) handleSaveFlow(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.flowPerson(w, r)
	if !ok {
		return
	}
	p, role, ok := s.flowProject(w, r, who)
	if !ok {
		return
	}
	name := r.PathValue("flow")
	if role != project.Editor {
		s.recordRefusal(r, who, "flow.saved", "project:"+p.Metadata.Name, "not an editor")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the project's editors save flows in it"})
		return
	}
	f, _, _, ok := readFlowBody(w, r)
	if !ok {
		return
	}
	switch {
	case !flow.NameOK(name) || f.Name != name:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the flow's name is lowercase letters, digits and underscores, and the one in the address"})
		return
	case f.Schedule != "" && !project.ValidCron(f.Schedule):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "a schedule as cron's five fields (UTC): 0 6 * * 1, or none"})
		return
	}
	ctx := r.Context()
	c, err := s.compileFlow(ctx, token, p, f)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if errs := c.Errors(); len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "a step can't be compiled: " + errs[0], "errors": errs, "steps": c.Steps})
		return
	}
	gl, target, err := s.repoOf(r, p)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if target == "" {
		if target, err = gl.DefaultBranch(ctx); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	}
	dbt := dbtDir(p)
	dags := strings.Trim(p.Spec.Pipelines.Path, "/")
	files := map[string]string{}
	for rel, text := range flow.Files(c) {
		files[path.Join(dbt, rel)] = text
	}
	files[path.Join(dags, "flow_"+name+".py")] = flow.DAG(p.Metadata.Name, name, f.Schedule, who, "dbt")
	branch := flowBranch(name)
	has, err := gl.HasBranch(ctx, branch)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	ref, from := target, target
	if has {
		ref, from = branch, ""
	}
	if there, err := gl.HasFile(ctx, path.Join(dbt, "dbt_project.yml"), ref); err == nil && !there {
		files[path.Join(dbt, "dbt_project.yml")] = fmt.Sprintf("# The project's dbt models, built by the platform's helper (storscale.dbt).\nname: %s\nversion: '1.0'\nconfig-version: 2\nprofile: storscale\nmodel-paths: [models]\n",
			strings.ReplaceAll(p.Metadata.Name, "-", "_"))
	}
	var changes []gitlab.FileChange
	paths := make([]string, 0, len(files))
	for fp := range files {
		paths = append(paths, fp)
	}
	sort.Strings(paths)
	for _, fp := range paths {
		there, err := gl.HasFile(ctx, fp, ref)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		changes = append(changes, gitlab.FileChange{Action: map[bool]string{true: "update", false: "create"}[there], FilePath: fp, Content: files[fp]})
	}
	// Models of steps that are gone.
	dir := path.Join(dbt, "models", "flows", name)
	if tree, err := gl.Tree(ctx, dir, ref); err == nil {
		var gone []string
		for _, e := range tree {
			if e.Type == "blob" && strings.HasSuffix(e.Path, ".sql") && files[e.Path] == "" {
				gone = append(gone, e.Path)
			}
		}
		sort.Strings(gone)
		for _, fp := range gone {
			changes = append(changes, gitlab.FileChange{Action: "delete", FilePath: fp})
		}
	}
	steps := len(f.Steps)
	msg := fmt.Sprintf("Flow %s: %d steps into %s\n\nBuilt on the platform's canvas by %s.", name, steps, f.OutputStep().Table, who)
	if _, err := gl.Commit(ctx, branch, from, msg, "", "", changes); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "GitLab refused the commit: " + err.Error()})
		return
	}
	when := "by hand"
	if f.Schedule != "" {
		when = "at `" + f.Schedule + "` (UTC)"
	}
	mr, made, err := gl.MergeRequestFor(ctx, branch, target, "Flow "+name,
		fmt.Sprintf("%s's flow `%s`, built on the platform's canvas: %d steps into the table `%s`, as dbt models in `%s/models/flows/%s/`, built %s by the DAG `flow_%s` with its checks.\n\nSaving the flow again updates this branch.",
			who, name, steps, f.OutputStep().Table, dbt, name, when, name))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the merge request: " + err.Error()})
		return
	}
	s.record(r, who, "flow.saved", "project:"+p.Metadata.Name, fmt.Sprintf("%s: %d steps into %s, on %s", name, steps, f.OutputStep().Table, branch))
	writeJSON(w, http.StatusOK, map[string]any{"branch": branch, "mergeRequest": mr.WebURL, "new": made, "files": paths, "dag": "flow_" + name})
}
