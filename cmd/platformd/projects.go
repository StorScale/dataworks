package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// A projectView is a project as the web app shows it: its spec, what the
// operator last did with it, and the viewer's own role in it.
type projectView struct {
	Name   string           `json:"name"`
	Spec   *project.Project `json:"spec,omitempty"` // nil while it's being deleted
	Status *project.Status  `json:"status,omitempty"`
	Role   string           `json:"role,omitempty"` // the viewer's: reader, editor, or ""
}

func (s *server) isAdmin(u user) bool {
	return slices.ContainsFunc(u.Groups, func(g string) bool { return slices.Contains(s.cfg.AdminGroups, g) })
}

// signedIn is the session of someone allowed in, or writes the refusal.
func (s *server) signedIn(w http.ResponseWriter, r *http.Request) *session {
	sess := s.session(r)
	switch {
	case sess == nil:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
	case !s.allowed(sess.User.Groups):
		s.fail(w, r, http.StatusForbidden, "no access")
	case s.store == nil:
		s.fail(w, r, http.StatusServiceUnavailable, "projects aren't set up (no STORE_URL)")
	default:
		return sess
	}
	return nil
}

// admin is signedIn, for administrators' changes (with the request header that
// a cross-site form can't send).
func (s *server) admin(w http.ResponseWriter, r *http.Request) *session {
	sess := s.signedIn(w, r)
	if sess == nil {
		return nil
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return nil
	}
	if !s.isAdmin(sess.User) {
		s.fail(w, r, http.StatusForbidden, "only administrators change projects")
		return nil
	}
	return sess
}

// views are the projects the user may see: all of them for administrators,
// otherwise the ones they're a member of.
func (s *server) views(r *http.Request, u user) ([]projectView, error) {
	specs, err := s.store.Projects(r.Context())
	if err != nil {
		return nil, err
	}
	statuses, err := s.store.Statuses(r.Context())
	if err != nil {
		return nil, err
	}
	byName := map[string]*projectView{}
	for _, p := range specs {
		byName[p.Metadata.Name] = &projectView{Name: p.Metadata.Name, Spec: p}
	}
	for _, st := range statuses {
		v := byName[st.Name]
		if v == nil {
			v = &projectView{Name: st.Name}
			byName[st.Name] = v
		}
		v.Status = st
		for _, m := range st.Members {
			if m.Username == u.Username && m.Role != "pipelines" {
				v.Role = m.Role
			}
		}
	}
	admin := s.isAdmin(u)
	var out []projectView
	for _, v := range byName {
		if admin || v.Role != "" {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *server) handleProjects(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	views, err := s.views(r, sess.User)
	if err != nil {
		s.log.Error("projects", "error", err)
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": views, "admin": s.isAdmin(sess.User)})
}

func (s *server) handleProject(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	views, err := s.views(r, sess.User)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	for _, v := range views {
		if v.Name == r.PathValue("name") {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	s.fail(w, r, http.StatusNotFound, "no such project")
}

// handlePutProject creates or changes a project. The operator applies it.
func (s *server) handlePutProject(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	p, err := project.Parse(body)
	if err == nil && p.Metadata.Name != r.PathValue("name") {
		err = errorString("the project's name must be " + r.PathValue("name"))
	}
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	before, _ := s.store.Project(r.Context(), p.Metadata.Name)
	keepDeliveryApprovals(before, p, sess.User.Username)
	project.KeepProductState(before, p)
	if err := s.store.PutProject(r.Context(), p); err != nil {
		var refused *store.APIError
		if errors.As(err, &refused) && refused.Code >= 400 && refused.Code < 500 && refused.Code != http.StatusForbidden {
			// Kubernetes checked the project against its schema (deploy/crds/project.yaml).
			s.fail(w, r, http.StatusUnprocessableEntity, refused.Message)
			return
		}
		s.log.Error("saving a project", "project", p.Metadata.Name, "error", err)
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	s.log.Info("project saved", "project", p.Metadata.Name, "by", sess.User.Username)
	s.record(r, sess.User.Username, "project.saved", "project:"+p.Metadata.Name, changes(before, p))
	writeJSON(w, http.StatusOK, projectView{Name: p.Metadata.Name, Spec: p})
}

func (s *server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	name := r.PathValue("name")
	p, err := s.store.Project(r.Context(), name)
	if err == nil && p == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return
	}
	if err == nil {
		err = s.store.DeleteProject(r.Context(), name)
	}
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	s.log.Info("project deleted", "project", name, "by", sess.User.Username)
	s.record(r, sess.User.Username, "project.deleted", "project:"+name, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleAccess is who has which role in which project (administrators only).
func (s *server) handleAccess(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if !s.isAdmin(sess.User) {
		s.fail(w, r, http.StatusForbidden, "only administrators review access")
		return
	}
	views, err := s.views(r, sess.User)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	type person struct {
		Username string            `json:"username"`
		Roles    map[string]string `json:"roles"` // project -> role
		Via      map[string]string `json:"via"`   // project -> group
	}
	byName := map[string]*person{}
	var projects []string
	for _, v := range views {
		projects = append(projects, v.Name)
		if v.Status == nil {
			continue
		}
		for _, m := range v.Status.Members {
			p := byName[m.Username]
			if p == nil {
				p = &person{Username: m.Username, Roles: map[string]string{}, Via: map[string]string{}}
				byName[m.Username] = p
			}
			p.Roles[v.Name], p.Via[v.Name] = m.Role, m.Via
		}
	}
	people := make([]*person, 0, len(byName))
	for _, p := range byName {
		people = append(people, p)
	}
	sort.Slice(people, func(i, j int) bool { return people[i].Username < people[j].Username })
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects, "people": people})
}

// handleFlow is a project's tables, the lineage between them, and their
// checks, from the catalog: for the project's members and administrators.
func (s *server) handleFlow(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if s.catalog == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the catalog isn't set up (no CATALOG_URL)")
		return
	}
	views, err := s.views(r, sess.User)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	for _, v := range views {
		if v.Name != r.PathValue("name") || v.Spec == nil {
			continue
		}
		t := v.Spec.Spec.Tables
		if t == nil {
			writeJSON(w, http.StatusOK, map[string]any{"tables": []any{}, "edges": []any{}})
			return
		}
		flow, err := s.catalog.Flow(r.Context(), t.Catalog, t.Namespace)
		if err != nil {
			s.log.Error("flow", "project", v.Name, "error", err)
			s.fail(w, r, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, flow)
		return
	}
	s.fail(w, r, http.StatusNotFound, "no such project")
}

type errorString string

func (e errorString) Error() string { return string(e) }

// project is the signed-in person's view of one project, or writes why not.
func (s *server) project(w http.ResponseWriter, r *http.Request, sess *session) *projectView {
	views, err := s.views(r, sess.User)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return nil
	}
	for i := range views {
		if views[i].Name == r.PathValue("name") && views[i].Spec != nil {
			return &views[i]
		}
	}
	s.fail(w, r, http.StatusNotFound, "no such project")
	return nil
}

// projectAirflow is a project's own Airflow, as the platform's account.
type projectAirflow struct {
	password string
	client   *pipelines.Client
}

// pipelinesOf is the Airflow a project's pipelines run in, and where people
// open its UI: the project's own (on Kubernetes, with a repo), or the shared one.
func (s *server) pipelinesOf(ctx context.Context, v *projectView) (*pipelines.Client, string, error) {
	pl := v.Spec.Spec.Pipelines
	if !s.cfg.ProjectAirflows || s.store == nil || pl == nil || pl.Repo == "" {
		return s.airflow, "", nil
	}
	sec, err := s.store.Secret(ctx, v.Spec.AirflowName())
	if err != nil {
		return nil, "", err
	}
	pw := sec[project.AirflowPlatformPasswordKey]
	if pw == "" {
		return nil, "", errorString("the project's Airflow isn't made yet")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.projectAirflows[v.Name]
	if !ok || a.password != pw { // made again: a new password, and a new token
		if s.projectAirflows == nil {
			s.projectAirflows = map[string]projectAirflow{}
		}
		a = projectAirflow{password: pw, client: pipelines.New(v.Spec.AirflowURL(), "storscale-platform", pw)}
		s.projectAirflows[v.Name] = a
	}
	return a.client, "/airflow/" + v.Name + "/", nil
}

// handlePipelines is a project's pipelines, their recent runs, and the newest
// run's tasks, from Airflow.
func (s *server) handlePipelines(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if s.airflow == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "pipelines aren't set up (no AIRFLOW_URL)")
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	af, ui, err := s.pipelinesOf(r.Context(), v)
	if err != nil {
		s.log.Error("pipelines", "project", v.Name, "error", err)
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	ps, err := af.Of(r.Context(), v.Name, 12)
	if err != nil {
		s.log.Error("pipelines", "project", v.Name, "error", err)
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	// A project with an Airflow of its own: its imports (8.2) run in the
	// platform's, tagged for it; listed here beside its own.
	if af != s.airflow {
		shared, err := s.airflow.Of(r.Context(), v.Name, 12)
		if err != nil {
			s.log.Error("pipelines (the platform's Airflow)", "project", v.Name, "error", err)
		}
		ps = append(ps, shared...)
	}
	out := map[string]any{"pipelines": ps, "canRun": v.Role == project.Editor || s.isAdmin(sess.User)}
	if ui != "" {
		out["airflowPath"] = ui // the project's own Airflow's UI
	}
	// The project's pipelines repo, and the files in it Airflow couldn't load.
	if pl := v.Spec.Spec.Pipelines; pl != nil && pl.Repo != "" {
		out["repo"] = map[string]string{"url": pl.Repo, "path": pl.Path, "ref": pl.Ref}
		errs, err := af.ImportErrors(r.Context(), v.Name)
		if err != nil {
			s.log.Error("import errors", "project", v.Name, "error", err)
		}
		out["importErrors"] = errs
	}
	writeJSON(w, http.StatusOK, out)
}

// airflowFor is the Airflow a pipeline of the project's runs in: the
// platform's for an import (8.2: they run there, whatever Airflow the
// project has), else the project's own or the shared one.
func (s *server) airflowFor(ctx context.Context, v *projectView, dag string) (*pipelines.Client, error) {
	if strings.HasPrefix(dag, "import_") {
		return s.airflow, nil
	}
	af, _, err := s.pipelinesOf(ctx, v)
	return af, err
}

// handleStartRun starts a run of one of the project's pipelines, for its
// editors (and administrators). The run itself goes as the pipelines'
// service account, whoever starts it.
// handleRunLog is what a run's tasks logged, to the project's members: why
// it failed, in the words of what ran.
func (s *server) handleRunLog(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if s.airflow == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "pipelines aren't set up (no AIRFLOW_URL)")
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	af, err := s.airflowFor(r.Context(), v, r.PathValue("pipeline"))
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	text, err := af.RunLog(r.Context(), r.PathValue("pipeline"), r.PathValue("run"))
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"log": text})
}

func (s *server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	if s.airflow == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "pipelines aren't set up (no AIRFLOW_URL)")
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	if v.Role != project.Editor && !s.isAdmin(sess.User) {
		s.fail(w, r, http.StatusForbidden, "only the project's editors run its pipelines")
		return
	}
	af, err := s.airflowFor(r.Context(), v, r.PathValue("pipeline"))
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	run, err := af.Start(r.Context(), v.Name, r.PathValue("pipeline"), sess.User.Username)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.log.Info("pipeline run started", "project", v.Name, "pipeline", r.PathValue("pipeline"), "run", run, "by", sess.User.Username)
	s.record(r, sess.User.Username, "pipeline.started", "project:"+v.Name+"/pipeline:"+r.PathValue("pipeline"), "run "+run)
	writeJSON(w, http.StatusOK, map[string]string{"run": run})
}
