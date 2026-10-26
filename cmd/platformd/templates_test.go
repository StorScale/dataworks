package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/gitlab"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/store"
)

// connectionsKept is projectsKept with connections, and a save that can fail.
type connectionsKept struct {
	projectsKept
	conns   map[string]store.StoredConnection
	refuse  bool
	removed []string
}

func (c *connectionsKept) PutConnection(_ context.Context, p string, conn store.StoredConnection) error {
	c.conns[p+"/"+conn.Name] = conn
	return nil
}

func (c *connectionsKept) DeleteConnection(_ context.Context, p, name string) error {
	delete(c.conns, p+"/"+name)
	c.removed = append(c.removed, p+"/"+name)
	return nil
}

func (c *connectionsKept) PutProject(ctx context.Context, p *project.Project) error {
	if c.refuse {
		return errors.New("the store is down")
	}
	return c.projectsKept.PutProject(ctx, p)
}

// groupGitLab plays the little of GitLab templates and libraries use.
type groupGitLab struct {
	mu      sync.Mutex
	calls   []string
	repos   map[string]bool // path: there
	polls   int
	commits []string
}

func (g *groupGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("PRIVATE-TOKEN") != "install-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/")
	g.calls = append(g.calls, r.Method+" "+path)
	repo, rest, _ := strings.Cut(path, "/")
	repo = strings.ReplaceAll(repo, "%2F", "/")
	if !g.repos[repo] {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "404 Project Not Found"})
		return
	}
	switch r.Method + " " + rest {
	case "GET ":
		status := "finished"
		if repo != "templates/starter" && g.polls < 1 { // a fork: copied on the second look
			g.polls++
			status = "started"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 9, "path_with_namespace": repo, "http_url_to_repo": "https://gitlab.test/" + repo + ".git",
			"web_url": "https://gitlab.test/" + repo, "import_status": status})
	case "POST fork":
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		made := in["namespace_path"] + "/" + in["path"]
		g.repos[made] = true
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": 9, "path_with_namespace": made, "import_status": "scheduled"})
	case "DELETE fork", "DELETE ":
		if rest == "" {
			delete(g.repos, repo)
		}
		w.WriteHeader(http.StatusNoContent)
	case "POST access_tokens":
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["access_level"] != float64(30) || in["expires_at"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": "glpat-repo-token"})
	case "GET repository/tree":
		if repo == "teams/growth" || repo == "teams/churn" {
			json.NewEncoder(w).Encode([]map[string]string{{"path": "dags", "type": "tree"}, {"path": "dags/starter.py", "type": "blob"}, {"path": "README.md", "type": "blob"}})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case "GET repository/files/dags%2Fstarter.py/raw":
		w.Write([]byte(`dbt.build(project="{project}")`))
	case "GET repository/files/README.md/raw":
		w.Write([]byte("# A team's project"))
	case "POST repository/commits":
		var in struct {
			Branch  string              `json:"branch"`
			Actions []gitlab.FileChange `json:"actions"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		g.commits = append(g.commits, fmt.Sprintf("%s %s:%s %s", in.Branch, in.Actions[0].Action, in.Actions[0].FilePath, in.Actions[0].Content))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "abc"})
	case "GET repository/commits/v1.2":
		json.NewEncoder(w).Encode(map[string]string{"id": "0123456789abcdef0123"})
	case "GET repository/archive.tar.gz":
		w.Write([]byte("the archive at " + r.URL.Query().Get("sha")))
	default:
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "404 Not Found"})
	}
}

func TestAProjectIsMadeFromATemplate(t *testing.T) {
	gl := &groupGitLab{repos: map[string]bool{"templates/starter": true, "libraries/checks": true}}
	gts := httptest.NewServer(gl)
	defer gts.Close()
	st := &connectionsKept{projectsKept: projectsKept{specs: map[string]*project.Project{}}, conns: map[string]store.StoredConnection{}}
	hour := time.Now().Add(time.Hour)
	files := kept{}
	s := &server{cfg: config{Groups: []string{"analysts", "engineers"}, AdminGroups: []string{"engineers"},
		GitLabURL: gts.URL, GitLabGroup: "teams", GitLabToken: "install-token"},
		store: st, files: files, now: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*session{
			"bob":   {User: user{Username: "bob", Groups: []string{"engineers"}}, Expires: hour},
			"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: hour},
		},
		tokenUser: func(raw string) (string, error) {
			if raw == "pipelines-token" {
				return "service-account-airflow-pipelines", nil
			}
			return "", errors.New("not the realm's")
		}}
	h := s.routes()
	call := func(who, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Platform-Request", "1")
		if strings.HasPrefix(who, "Bearer ") {
			r.Header.Set("Authorization", who)
		} else if who != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: who})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	starter := `{"description":"A team's first project: a sample pipeline and dbt model, a check and a metric",
		"repo":"templates/starter",
		"spec":{"description":"{project}: a new team's","tables":{"checks":[{"table":"orders","column":"order_id","check":"unique"}]},
			"pipelines":{"serviceAccount":"airflow-pipelines","path":"dags","reviews":{"gitlab":"x","dbt":"dags/dbt"}},
			"notify":{"email":["data@example.com"]},"files":{},"limits":{"storage":"100Gi"}}}`
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"alice", "PUT", "/api/templates/starter", starter, http.StatusForbidden}, // not an administrator
		{"bob", "PUT", "/api/templates/starter", strings.Replace(starter, `"spec":{`, `"spec":{"members":[{"user":"bob","role":"editor"}],`, 1), http.StatusUnprocessableEntity},
		{"bob", "PUT", "/api/templates/starter", strings.Replace(starter, "templates/starter", "templates/gone", 1), http.StatusUnprocessableEntity},
		{"bob", "PUT", "/api/templates/Bad_Name", starter, http.StatusUnprocessableEntity},
		{"bob", "PUT", "/api/templates/starter", starter, http.StatusOK},
		{"alice", "POST", "/api/templates/starter/projects", `{"name":"growth","members":[{"group":"growth","role":"editor"}]}`, http.StatusForbidden},
		{"bob", "POST", "/api/templates/none/projects", `{"name":"growth","members":[{"group":"growth","role":"editor"}]}`, http.StatusNotFound},
		{"bob", "POST", "/api/templates/starter/projects", `{"name":"Growth!","members":[{"group":"growth","role":"editor"}]}`, http.StatusUnprocessableEntity},
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.path, code, body, c.want)
		}
	}
	if len(gl.repos) != 2 {
		t.Fatalf("a refused project made a repo: %v", gl.repos)
	}
	// Anyone signed in sees the templates; only administrators may change them.
	code, body := call("alice", "GET", "/api/templates", "")
	if code != 200 || !strings.Contains(body, `"starter"`) || !strings.Contains(body, `"canEdit":false`) || !strings.Contains(body, `"repos":true`) {
		t.Errorf("alice's list: %d %s", code, body)
	}

	code, body = call("bob", "POST", "/api/templates/starter/projects", `{"name":"growth","members":[{"group":"growth","role":"editor"}],"notify":{"email":["growth@example.com"]}}`)
	if code != http.StatusCreated {
		t.Fatalf("made: %d %s", code, body)
	}
	p := st.specs["growth"]
	if p == nil || p.Spec.Description != "growth: a new team's" || p.Spec.Members[0].Group != "growth" || p.Spec.Notify.Email[0] != "growth@example.com" ||
		p.Spec.Limits.Storage != "100Gi" || len(p.Spec.Tables.Checks) != 1 || p.Spec.Tables.Namespace != "growth" {
		t.Fatalf("the project: %+v", p)
	}
	pl := p.Spec.Pipelines
	if pl.Repo != "https://gitlab.test/teams/growth.git" || pl.Credentials.SecretRef != "growth-repo" || pl.Path != "dags" ||
		pl.Reviews.GitLab != gts.URL || pl.Reviews.Project != "teams/growth" {
		t.Errorf("its pipelines: %+v %+v", pl, pl.Reviews)
	}
	if c := st.conns["growth/growth-repo"]; c.Kind != "git" || c.Data["password"] != "glpat-repo-token" || c.AddedBy != "bob" {
		t.Errorf("its repo's connection: %+v", c)
	}
	if strings.Contains(body, "glpat") || !strings.Contains(body, `"repo":"https://gitlab.test/teams/growth"`) {
		t.Errorf("the answer: %s", body)
	}
	want := []string{"POST templates%2Fstarter/fork", "GET teams%2Fgrowth", "GET teams%2Fgrowth", "DELETE teams%2Fgrowth/fork",
		"GET teams%2Fgrowth/repository/tree", "GET teams%2Fgrowth/repository/files/dags%2Fstarter.py/raw", "GET teams%2Fgrowth/repository/files/README.md/raw",
		"POST teams%2Fgrowth/repository/commits", "POST teams%2Fgrowth/access_tokens"}
	if got := gl.calls[len(gl.calls)-len(want):]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("GitLab was asked %v, want %v", got, want)
	}
	if len(gl.commits) != 1 || gl.commits[0] != `main update:dags/starter.py dbt.build(project="growth")` {
		t.Errorf("the repo's files filled in: %v", gl.commits)
	}
	if code, _ := call("bob", "POST", "/api/templates/starter/projects", `{"name":"growth","members":[{"group":"growth","role":"editor"}]}`); code != http.StatusConflict {
		t.Errorf("made again: %d", code)
	}

	// A project the store can't save: its repo and connection are taken back.
	st.refuse = true
	if code, body := call("bob", "POST", "/api/templates/starter/projects", `{"name":"churn","members":[{"group":"churn","role":"editor"}]}`); code != http.StatusBadGateway {
		t.Errorf("a failed save: %d %s", code, body)
	}
	if gl.repos["teams/churn"] || len(st.conns) != 1 || st.removed[0] != "churn/churn-repo" {
		t.Errorf("not taken back: %v %v", gl.repos, st.conns)
	}
	st.refuse = false

	// Libraries: defined at a ref that's there, read by anyone the realm signed in.
	for _, c := range []struct {
		who, method, path, body string
		want                    int
	}{
		{"alice", "PUT", "/api/libraries/checks", `{"description":"d","repo":"libraries/checks","ref":"v1.2"}`, http.StatusForbidden},
		{"bob", "PUT", "/api/libraries/checks", `{"description":"The company's dbt tests","repo":"libraries/checks","ref":"v9"}`, http.StatusUnprocessableEntity},
		{"bob", "PUT", "/api/libraries/checks", `{"description":"The company's dbt tests","repo":"libraries/checks","ref":"v1.2"}`, http.StatusOK},
		{"", "GET", "/api/libraries/checks", "", http.StatusUnauthorized},
		{"Bearer forged", "GET", "/api/libraries/checks", "", http.StatusUnauthorized},
		{"Bearer pipelines-token", "GET", "/api/libraries/none", "", http.StatusNotFound},
		{"Bearer pipelines-token", "GET", "/api/libraries/checks/archive?commit=HEAD~1", "", http.StatusBadRequest},
	} {
		if code, body := call(c.who, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.method, c.path, code, body, c.want)
		}
	}
	code, body = call("Bearer pipelines-token", "GET", "/api/libraries/checks", "")
	if code != 200 || !strings.Contains(body, `"commit":"0123456789abcdef0123"`) || !strings.Contains(body, `"url":"`+gts.URL+`/libraries/checks.git"`) {
		t.Errorf("the library: %d %s", code, body)
	}
	if code, body = call("Bearer pipelines-token", "GET", "/api/libraries", ""); code != 200 || !strings.Contains(body, `"name":"checks"`) {
		t.Errorf("the libraries: %d %s", code, body)
	}
	if code, body = call("alice", "GET", "/api/libraries/checks/archive", ""); code != 200 || body != "the archive at 0123456789abcdef0123" {
		t.Errorf("its files: %d %s", code, body)
	}
	if code, _ := call("bob", "DELETE", "/api/templates/starter", ""); code != http.StatusNoContent || strings.Contains(files[project.TemplatesKey], "starter") {
		t.Errorf("removed: %d %s", code, files[project.TemplatesKey])
	}
	if st.specs["growth"] == nil {
		t.Error("a template's removal took its project")
	}
}
