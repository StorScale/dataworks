package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/StorScale/storscale-platform/internal/gitlab"
	"github.com/StorScale/storscale-platform/internal/project"
)

// A notebook scheduled from JupyterLab (use case 7.7): JupyterLab's Schedule
// (the notebook image's extension) sends the notebook, as the person, with
// their Keycloak token; the platform commits it and a small DAG that runs it
// (storscale.notebook, papermill) on a branch of the project's repo, and
// opens a merge request, reviewed like any other change. Each run sends its
// executed notebook back, kept in the platform's bucket and linked from the
// run on the Pipelines tab.

var notebookNameRE = regexp.MustCompile(`[^a-z0-9]+`)

// notebookName is a DAG-friendly name for a notebook file: churn_features.
func notebookName(file string) string {
	n := strings.TrimSuffix(strings.ToLower(path.Base(file)), ".ipynb")
	n = strings.Trim(notebookNameRE.ReplaceAllString(n, "_"), "_")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "nb_" + n
	}
	return strings.TrimRight(n[:min(len(n), 50)], "_")
}

// notebookDAG is the DAG file that runs a scheduled notebook.
func notebookDAG(p, name, file, schedule, owner string) string {
	return fmt.Sprintf(`"""%s's notebook %s, run on a schedule: scheduled from JupyterLab.

Each run (storscale.notebook: papermill, as the project's pipelines) keeps
its executed notebook, linked from the run on the project's Pipelines tab.
The notebook is notebooks/%s.ipynb, beside this file.
"""
# An Airflow DAG (Airflow reads only files that say so: "airflow" and "dag").
from storscale import notebook

notebook.schedule(project=%q, name=%q, schedule=%q, owner=%q)
`, owner, path.Base(file), name, p, name, schedule, owner)
}

// handleNotebookProjects are the projects the token's person may schedule a
// notebook in: they're its editor, and it has a repo.
func (s *server) handleNotebookProjects(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "projects aren't set up"})
		return
	}
	who, err := s.bearer(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	sts, err := s.store.Statuses(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the project store isn't answering"})
		return
	}
	type option struct {
		Name string `json:"name"`
		Why  string `json:"why,omitempty"` // why it can't, if it can't
	}
	out := []option{}
	for _, st := range sts {
		if !editorIn(st.Members, who) {
			continue
		}
		p, err := s.store.Project(r.Context(), st.Name)
		if err != nil || p == nil {
			continue
		}
		o := option{Name: st.Name}
		if _, _, err := s.repoOf(r, p); err != nil {
			o.Why = err.Error()
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "user": who})
}

func editorIn(members []project.Person, who string) bool {
	for _, m := range members {
		if m.Username == who && m.Role == project.Editor {
			return true
		}
	}
	return false
}

// repoOf is GitLab's API for a project's repo, and the branch changes go to.
func (s *server) repoOf(r *http.Request, p *project.Project) (*gitlab.Client, string, error) {
	pl := p.Spec.Pipelines
	if pl == nil || pl.Repo == "" {
		return nil, "", errorString("the project has no repo")
	}
	if pl.Credentials == nil || !strings.HasPrefix(pl.Repo, "https://") && !strings.HasPrefix(pl.Repo, "http://") {
		return nil, "", errorString("the project's repo needs an https address and a token (its connection) for the platform to commit to it")
	}
	base, repoPath := "", gitlab.ProjectPath(pl.Repo)
	if rv := pl.Reviews; rv != nil {
		base = rv.GitLab
		if rv.Project != "" {
			repoPath = rv.Project
		}
	} else if u, err := url.Parse(pl.Repo); err == nil {
		base = u.Scheme + "://" + u.Host
	}
	sec, err := s.store.Connection(r.Context(), p.Metadata.Name, pl.Credentials.SecretRef)
	if err != nil || sec["password"] == "" {
		return nil, "", errorString("the repo's connection has no token")
	}
	return gitlab.New(base, sec["password"], repoPath), pl.Ref, nil
}

// handleScheduleNotebook commits a notebook and the DAG that runs it on a
// branch of the project's repo, and opens (or updates) a merge request.
func (s *server) handleScheduleNotebook(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "projects aren't set up"})
		return
	}
	who, err := s.bearer(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	name := r.PathValue("name")
	st, err := s.store.Status(r.Context(), name)
	if err != nil || st == nil || !editorIn(st.Members, who) {
		s.recordRefusal(r, who, "notebook.scheduled", "project:"+name, "not an editor")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the project's editors schedule notebooks in it"})
		return
	}
	var body struct {
		Path     string          `json:"path"`
		Schedule string          `json:"schedule"`
		Notebook json.RawMessage `json:"notebook"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var nb struct {
		Cells []json.RawMessage `json:"cells"`
	}
	switch {
	case !strings.HasSuffix(body.Path, ".ipynb"):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "a notebook (.ipynb)"})
		return
	case !project.ValidCron(body.Schedule):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "a schedule as cron's five fields (UTC): 0 5 * * *"})
		return
	case json.Unmarshal(body.Notebook, &nb) != nil || nb.Cells == nil:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the notebook isn't one (no cells)"})
		return
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such project"})
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
	nbName := notebookName(body.Path)
	dags := strings.Trim(p.Spec.Pipelines.Path, "/")
	files := []gitlab.FileChange{
		{FilePath: path.Join(dags, "notebooks", nbName+".ipynb"), Content: string(body.Notebook)},
		{FilePath: path.Join(dags, "notebook_"+nbName+".py"), Content: notebookDAG(name, nbName, body.Path, body.Schedule, who)},
	}
	branch := "storscale/schedule-" + strings.ReplaceAll(nbName, "_", "-")
	has, err := gl.HasBranch(ctx, branch)
	from := ""
	if err == nil && !has {
		from = target
	}
	ref := map[bool]string{true: branch, false: target}[has]
	for i := range files {
		if err != nil {
			break
		}
		var there bool
		there, err = gl.HasFile(ctx, files[i].FilePath, ref)
		files[i].Action = map[bool]string{true: "update", false: "create"}[there]
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	msg := fmt.Sprintf("Schedule %s (%s, UTC)\n\nScheduled by %s from JupyterLab, through the platform.", path.Base(body.Path), body.Schedule, who)
	commit, err := gl.Commit(ctx, branch, from, msg, "", "", files)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	mr, made, err := gl.MergeRequestFor(ctx, branch, target, "Schedule "+path.Base(body.Path),
		fmt.Sprintf("%s's notebook `%s`, run at `%s` (UTC) as the project's pipelines, by the DAG `notebook_%s`. Each run's executed notebook is linked from the run on the project's Pipelines tab.\n\nScheduled from JupyterLab through the platform.",
			who, path.Base(body.Path), body.Schedule, nbName))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.record(r, who, "notebook.scheduled", "project:"+name+"/pipeline:notebook_"+nbName,
		fmt.Sprintf("%s at %s; commit %s on %s, merge request !%d", body.Path, body.Schedule, short(commit), branch, mr.IID))
	writeJSON(w, http.StatusOK, map[string]any{"dag": "notebook_" + nbName, "branch": branch, "commit": commit,
		"mergeRequest": mr.WebURL, "iid": mr.IID, "new": made})
}

var runFileRE = regexp.MustCompile(`^[A-Za-z0-9_.:+-]{1,200}$`)

func notebookRunKey(p, dag, run, ext string) string {
	return "notebook-runs/" + p + "/" + dag + "/" + url.PathEscape(run) + "." + ext
}

// handleNotebookRun takes a run's executed notebook (and its HTML) from the
// project's pipelines.
func (s *server) handleNotebookRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	who, ok := s.projectPipelines(w, r, name)
	if !ok {
		return
	}
	dag, run := r.PathValue("dag"), r.PathValue("run")
	if !strings.HasPrefix(dag, "notebook_") || !runFileRE.MatchString(dag) || !runFileRE.MatchString(run) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a notebook's DAG and run"})
		return
	}
	var body struct {
		Notebook json.RawMessage `json:"notebook"`
		HTML     string          `json:"html"`
		Failed   string          `json:"failed"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&body); err != nil || len(body.Notebook) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the executed notebook, as JSON"})
		return
	}
	err := s.files.PutFile(r.Context(), notebookRunKey(name, dag, run, "ipynb"), body.Notebook, "application/x-ipynb+json")
	if err == nil && body.HTML != "" {
		err = s.files.PutFile(r.Context(), notebookRunKey(name, dag, run, "html"), []byte(body.HTML), "text/html")
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	detail := "ran"
	if body.Failed != "" {
		detail = "failed: " + body.Failed
	}
	s.record(r, who, "notebook.ran", "project:"+name+"/pipeline:"+dag, run+" "+detail)
	writeJSON(w, http.StatusOK, map[string]string{"kept": run})
}

// handleNotebookRunView is a run's executed notebook, to the project's
// members: its HTML (format=html, the default: no script runs, its outputs
// as they were) or the notebook itself (format=ipynb, to open in JupyterLab).
func (s *server) handleNotebookRunView(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	if s.files == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the platform keeps no files here"})
		return
	}
	dag, run, format := r.PathValue("dag"), r.PathValue("run"), r.URL.Query().Get("format")
	if format == "" {
		format = "html"
	}
	if !runFileRE.MatchString(dag) || !runFileRE.MatchString(run) || format != "html" && format != "ipynb" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a notebook's DAG and run, as html or ipynb"})
		return
	}
	data, err := s.files.GetFile(r.Context(), notebookRunKey(v.Name, dag, run, format))
	switch {
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	case data == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no notebook kept for that run"})
		return
	}
	if format == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The outputs as they were, and nothing run: no script, no request out.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:")
	} else {
		w.Header().Set("Content-Type", "application/x-ipynb+json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", dag+".ipynb"))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(data)
}
