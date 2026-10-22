package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

// MLflow (use case 15.1, R15.1, R15.2): one tracking server and model
// registry for the install, a workspace per project (MLflow's own
// isolation: every experiment, run, logged model, registered model and
// artifact is the workspace's). Only platformd reaches it, at
// /mlflow/<project>/...: it signs the caller in (the web app's session, or a
// Keycloak token, as MLFLOW_TRACKING_TOKEN), checks they're in the project
// (readers read; editors and the project's pipelines write), names the
// project's workspace on the request whatever the caller sent, and lets
// through MLflow's tracking, registry and artifact routes alone. Two things
// are the platform's: each run is stamped with who ran it and the data's
// Nessie commit, and a model version's approval (its storscale.* tags and the
// alias "approved") is set only by an editor other than who registered it,
// through the platform.

const mlflowWorkspaceHeader = "X-MLFLOW-WORKSPACE"

var (
	// The routes a project's people may use: MLflow's tracking, registry and (proxied) artifacts.
	mlflowAllowed = regexp.MustCompile(`^/(api|ajax-api)/(2\.0|3\.0)/(mlflow/(experiments|runs|metrics|registered-models|model-versions|logged-models|artifacts|datasets|traces)|mlflow-artifacts/artifacts)(/|$)`)
	// The reads among them a reader may make (searches and gets are POSTs too).
	mlflowRead = regexp.MustCompile(`/(search|get|get-by-name|get-history|get-history-bulk-interval|get-latest-versions|get-download-uri|list)$`)
	mlflowJSON = regexp.MustCompile(`^/(api|ajax-api)/(2\.0|3\.0)/mlflow/`)
)

type mlflowProxy struct {
	base    *url.URL
	proxy   *httputil.ReverseProxy
	http    *http.Client
	mu      sync.Mutex
	created map[string]bool // the workspaces known to exist
}

func (s *server) mlflow() *mlflowProxy {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mlflowProxy == nil && s.cfg.MLflowURL != "" {
		base, err := url.Parse(s.cfg.MLflowURL)
		if err != nil {
			return nil
		}
		p := &mlflowProxy{base: base, http: &http.Client{Timeout: time.Minute}, created: map[string]bool{}}
		p.proxy = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(base)
				pr.Out.URL.Path = strings.TrimSuffix(base.Path, "/") + pr.In.URL.Path
				pr.Out.URL.RawPath = ""
				pr.Out.Host = base.Host
			},
			FlushInterval: -1,
		}
		s.mlflowProxy = p
	}
	return s.mlflowProxy
}

// call is a request of the platform's own to MLflow, in a project's workspace.
func (m *mlflowProxy) call(ctx context.Context, workspace, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	u := *m.base
	u.Path = strings.TrimSuffix(m.base.Path, "/") + path
	if i := strings.Index(path, "?"); i >= 0 {
		u.Path, u.RawQuery = strings.TrimSuffix(m.base.Path, "/")+path[:i], path[i+1:]
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if workspace != "" {
		req.Header.Set(mlflowWorkspaceHeader, workspace)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("MLflow: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Code    string `json:"error_code"`
			Message string `json:"message"`
		}
		json.Unmarshal(data, &e)
		return &mlflowError{status: resp.StatusCode, code: e.Code, message: firstOf(e.Message, strings.TrimSpace(string(data)))}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

type mlflowError struct {
	status        int
	code, message string
}

func (e *mlflowError) Error() string { return fmt.Sprintf("MLflow: %s (%s)", e.message, e.code) }

// workspace makes the project's workspace, once.
func (m *mlflowProxy) workspace(ctx context.Context, name string) error {
	m.mu.Lock()
	done := m.created[name]
	m.mu.Unlock()
	if done {
		return nil
	}
	err := m.call(ctx, "", "POST", "/api/3.0/mlflow/workspaces", map[string]any{"name": name, "description": "The project " + name + "'s, made by the platform"}, nil)
	var me *mlflowError
	if err != nil && !(asMLflow(err, &me) && (me.code == "RESOURCE_ALREADY_EXISTS" || me.status == 409)) {
		return err
	}
	m.mu.Lock()
	m.created[name] = true
	m.mu.Unlock()
	return nil
}

func asMLflow(err error, out **mlflowError) bool {
	e, ok := err.(*mlflowError)
	if ok {
		*out = e
	}
	return ok
}

// mlflowRole is the caller's role in the project: a member's, the project's pipelines', an administrator's (an editor's); "" for no one.
func (s *server) mlflowRole(r *http.Request, name, who string) string {
	st, err := s.store.Status(r.Context(), name)
	if err != nil || st == nil {
		return ""
	}
	role := ""
	for _, m := range st.Members {
		if m.Username == who {
			role = m.Role
		}
	}
	if sess := s.session(r); sess != nil && s.isAdmin(sess.User) {
		role = project.Editor
	}
	return role
}

// handleMLflow is MLflow, for a project's people, in its workspace.
func (s *server) handleMLflow(w http.ResponseWriter, r *http.Request) {
	m := s.mlflow()
	if m == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "MLflow isn't set up here (no MLFLOW_URL)")
		return
	}
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	name, rest := r.PathValue("name"), "/"+r.PathValue("rest")
	role := s.mlflowRole(r, name, who)
	if role == "" {
		s.fail(w, r, http.StatusForbidden, "not a member of the project")
		return
	}
	if !mlflowAllowed.MatchString(rest) {
		s.fail(w, r, http.StatusNotFound, "not one of MLflow's routes the platform serves (tracking, the registry, artifacts)")
		return
	}
	reading := r.Method == http.MethodGet || r.Method == http.MethodHead || (r.Method == http.MethodPost && mlflowRead.MatchString(rest))
	if !reading && role != project.Editor && role != "pipelines" {
		s.fail(w, r, http.StatusForbidden, "the project's readers read its experiments and models; its editors and pipelines write them")
		return
	}
	if err := m.workspace(r.Context(), name); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	// The platform's say on what's written: its stamps on runs and versions, and approval.
	if !reading && mlflowJSON.MatchString(rest) && r.Body != nil {
		data, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		data, why := s.mlflowStamp(r.Context(), name, who, rest, data)
		if why != "" {
			s.recordRefusal(r, who, "mlflow.write", "project:"+name, why)
			s.fail(w, r, http.StatusForbidden, why)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		r.Header.Set("Content-Length", strconv.Itoa(len(data)))
		switch {
		case strings.HasSuffix(rest, "/runs/create"):
			s.record(r, who, "mlflow.run", "project:"+name, "a run started")
		case strings.HasSuffix(rest, "/registered-models/create"), strings.HasSuffix(rest, "/model-versions/create"):
			s.record(r, who, "model.registered", "project:"+name, string(data[:min(len(data), 300)]))
		}
	}
	r.URL.Path, r.URL.RawPath = rest, ""
	r.Header.Del("Authorization")
	r.Header.Del("Cookie")
	r.Header.Set(mlflowWorkspaceHeader, name)
	m.proxy.ServeHTTP(w, r)
}

// mlflowStamp is a write's body as the platform lets it through: the platform's tags kept the
// platform's (a run's who and data commit, a version's registrant), approval refused; or why not.
func (s *server) mlflowStamp(ctx context.Context, projectName, who, path string, body []byte) ([]byte, string) {
	var in map[string]any
	if len(bytes.TrimSpace(body)) == 0 || json.Unmarshal(body, &in) != nil {
		return body, ""
	}
	ours := func(k any) bool { key, _ := k.(string); return strings.HasPrefix(key, "storscale.") }
	if strings.HasSuffix(path, "/set-tag") || strings.HasSuffix(path, "/delete-tag") {
		if ours(in["key"]) {
			return nil, "the storscale.* tags are the platform's: a version is approved on the project's Models tab"
		}
	}
	if strings.Contains(path, "/registered-models/alias") {
		if a, _ := in["alias"].(string); a == "approved" {
			return nil, `the alias "approved" is the platform's: a version is approved on the project's Models tab, by an editor other than who registered it`
		}
	}
	keep := func(field string) []any { // the caller's tags, less the platform's
		var out []any
		if tags, ok := in[field].([]any); ok {
			for _, t := range tags {
				if tm, ok := t.(map[string]any); ok && !ours(tm["key"]) {
					out = append(out, t)
				}
			}
		}
		return out
	}
	switch {
	case strings.HasSuffix(path, "/runs/log-batch"):
		for _, t := range keepAll(in["tags"]) {
			if tm, ok := t.(map[string]any); ok && ours(tm["key"]) {
				return nil, "the storscale.* tags are the platform's"
			}
		}
	case strings.HasSuffix(path, "/runs/create"):
		tags := append(keep("tags"), map[string]any{"key": "storscale.user", "value": who})
		if commit := s.dataCommit(ctx); commit != "" { // the data the run read: the catalog's main branch now
			tags = append(tags, map[string]any{"key": "storscale.data_commit", "value": commit})
		}
		in["tags"] = tags
	case strings.HasSuffix(path, "/model-versions/create"):
		in["tags"] = append(keep("tags"), map[string]any{"key": "storscale.registered_by", "value": who})
	default:
		return body, ""
	}
	out, _ := json.Marshal(in)
	return out, ""
}

func keepAll(v any) []any {
	out, _ := v.([]any)
	return out
}

// dataCommit is the catalog's main branch's commit now, or "".
func (s *server) dataCommit(ctx context.Context) string {
	if s.history == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ref, err := s.history.nessie.Ref(ctx, "main"); err == nil && ref != nil {
		return ref.Hash
	}
	return ""
}

// --- The project's models, as its Models tab shows them --------------------------------------

type modelVersion struct {
	Version      string `json:"version"`
	RunID        string `json:"runId,omitempty"`
	Source       string `json:"source,omitempty"`
	Status       string `json:"status,omitempty"`
	Created      int64  `json:"created"`
	RegisteredBy string `json:"registeredBy,omitempty"`
	ApprovedBy   string `json:"approvedBy,omitempty"`
	ApprovedAt   string `json:"approvedAt,omitempty"`
}

type registeredModel struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"` // its card (R15.5)
	Updated     int64          `json:"updated"`
	Approved    string         `json:"approved,omitempty"` // the version the alias "approved" names
	Versions    []modelVersion `json:"versions"`
}

// handleModels are the project's registered models, their versions and approvals, and its experiments: its members.
func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, role, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	m := s.mlflow()
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "models": []any{}, "experiments": []any{}})
		return
	}
	name := p.Metadata.Name
	if err := m.workspace(r.Context(), name); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	var rm struct {
		Models []struct {
			Name        string                            `json:"name"`
			Description string                            `json:"description"`
			Updated     int64                             `json:"last_updated_timestamp"`
			Aliases     []struct{ Alias, Version string } `json:"aliases"`
		} `json:"registered_models"`
	}
	if err := m.call(r.Context(), name, "GET", "/api/2.0/mlflow/registered-models/search?max_results=200", nil, &rm); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	models := []registeredModel{}
	for _, x := range rm.Models {
		out := registeredModel{Name: x.Name, Description: x.Description, Updated: x.Updated, Versions: []modelVersion{}}
		for _, a := range x.Aliases {
			if a.Alias == "approved" {
				out.Approved = a.Version
			}
		}
		var mv struct {
			Versions []struct {
				Version string                        `json:"version"`
				RunID   string                        `json:"run_id"`
				Source  string                        `json:"source"`
				Status  string                        `json:"status"`
				Created int64                         `json:"creation_timestamp"`
				Tags    []struct{ Key, Value string } `json:"tags"`
			} `json:"model_versions"`
		}
		filter := url.QueryEscape("name='" + strings.ReplaceAll(x.Name, "'", "''") + "'")
		if err := m.call(r.Context(), name, "GET", "/api/2.0/mlflow/model-versions/search?max_results=200&filter="+filter, nil, &mv); err == nil {
			for _, v := range mv.Versions {
				ver := modelVersion{Version: v.Version, RunID: v.RunID, Source: v.Source, Status: v.Status, Created: v.Created}
				for _, t := range v.Tags {
					switch t.Key {
					case "storscale.registered_by":
						ver.RegisteredBy = t.Value
					case "storscale.approved_by":
						ver.ApprovedBy = t.Value
					case "storscale.approved_at":
						ver.ApprovedAt = t.Value
					}
				}
				out.Versions = append(out.Versions, ver)
			}
		}
		slices.SortFunc(out.Versions, func(a, b modelVersion) int { return int(b.Created - a.Created) })
		models = append(models, out)
	}
	var ex struct {
		Experiments []struct {
			ID      string `json:"experiment_id"`
			Name    string `json:"name"`
			Updated int64  `json:"last_update_time"`
		} `json:"experiments"`
	}
	m.call(r.Context(), name, "GET", "/api/2.0/mlflow/experiments/search?max_results=100", nil, &ex)
	experiments := []map[string]any{}
	for _, e := range ex.Experiments {
		if e.Name == "Default" {
			continue
		}
		experiments = append(experiments, map[string]any{"id": e.ID, "name": e.Name, "updated": e.Updated})
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "models": models, "experiments": experiments, "canApprove": role == project.Editor,
		"trackingUri": s.cfg.PlatformURL + "/mlflow/" + name})
}

// handleApproveModel approves a model version for use (R15.2): an editor other than who registered it.
// The version's storscale.approved_* tags and the model's alias "approved" are the platform's alone.
func (s *server) handleApproveModel(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, "approve its models")
	if sess == nil {
		return
	}
	m := s.mlflow()
	if m == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "MLflow isn't set up here")
		return
	}
	name, model, version := v.Name, r.PathValue("model"), r.PathValue("version")
	resource := "project:" + name + "/model:" + model + "/" + version
	var got struct {
		Version struct {
			Tags []struct{ Key, Value string } `json:"tags"`
		} `json:"model_version"`
	}
	if err := m.call(r.Context(), name, "GET", "/api/2.0/mlflow/model-versions/get?name="+url.QueryEscape(model)+"&version="+url.QueryEscape(version), nil, &got); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such model version: "+err.Error())
		return
	}
	registeredBy := ""
	for _, t := range got.Version.Tags {
		if t.Key == "storscale.registered_by" {
			registeredBy = t.Value
		}
	}
	if registeredBy == sess.User.Username {
		s.recordRefusal(r, sess.User.Username, "model.approved", resource, "approving their own")
		s.fail(w, r, http.StatusForbidden, "a model version is approved by an editor other than the one who registered it")
		return
	}
	at := s.now().UTC().Format(time.RFC3339)
	for _, t := range [][2]string{{"storscale.approved_by", sess.User.Username}, {"storscale.approved_at", at}} {
		if err := m.call(r.Context(), name, "POST", "/api/2.0/mlflow/model-versions/set-tag", map[string]string{"name": model, "version": version, "key": t[0], "value": t[1]}, nil); err != nil {
			s.fail(w, r, http.StatusBadGateway, err.Error())
			return
		}
	}
	if err := m.call(r.Context(), name, "POST", "/api/2.0/mlflow/registered-models/alias", map[string]string{"name": model, "alias": "approved", "version": version}, nil); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, sess.User.Username, "model.approved", resource, "registered by "+firstOf(registeredBy, "someone"))
	writeJSON(w, http.StatusOK, map[string]any{"approved": true, "by": sess.User.Username, "at": at})
}
