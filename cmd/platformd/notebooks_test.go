package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// fakeGitLab is a repo's branches and files, its commits and merge requests.
type fakeGitLab struct {
	mu       sync.Mutex
	branches map[string]bool
	files    map[string]bool   // branch:path
	contents map[string]string // branch:path -> content (what commits wrote, and what a test seeds)
	commits  []map[string]any
	mrs      []map[string]any
	token    bool // every call signed in with the repo's token
}

func (g *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.token = g.token && r.Header.Get("PRIVATE-TOKEN") == "repo-token"
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/sales%2Fpipelines")
	reply := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	var body map[string]any
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		json.Unmarshal(data, &body)
	}
	switch {
	case p == "" && r.Method == "GET":
		reply(200, map[string]string{"default_branch": "main"})
	case p == "/repository/branches" && r.Method == "GET":
		var names []map[string]string
		for b := range g.branches {
			if strings.Contains(b, r.URL.Query().Get("search")) {
				names = append(names, map[string]string{"name": b})
			}
		}
		reply(200, names)
	case p == "/repository/tree":
		var entries []map[string]string
		dirs := map[string]bool{}
		prefix := r.URL.Query().Get("path") + "/"
		for key := range g.files {
			b, fp, _ := strings.Cut(key, ":")
			if b != r.URL.Query().Get("ref") || !strings.HasPrefix(fp, prefix) {
				continue
			}
			entries = append(entries, map[string]string{"path": fp, "type": "blob"})
			for d := path.Dir(fp); strings.HasPrefix(d, prefix) && !dirs[d]; d = path.Dir(d) {
				dirs[d] = true
				entries = append(entries, map[string]string{"path": d, "type": "tree"})
			}
		}
		reply(200, entries)
	case strings.HasPrefix(p, "/repository/files/") && strings.HasSuffix(p, "/raw"):
		fp := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(p, "/repository/files/"), "/raw"), "%2F", "/")
		if text, ok := g.contents[r.URL.Query().Get("ref")+":"+fp]; ok {
			w.WriteHeader(200)
			w.Write([]byte(text))
		} else {
			reply(404, map[string]string{"message": "404 File Not Found"})
		}
	case strings.HasPrefix(p, "/repository/branches/"):
		if g.branches[strings.ReplaceAll(strings.TrimPrefix(p, "/repository/branches/"), "%2F", "/")] {
			reply(200, map[string]string{})
		} else {
			reply(404, map[string]string{"message": "404 Branch Not Found"})
		}
	case strings.HasPrefix(p, "/repository/files/"):
		path := strings.ReplaceAll(strings.TrimPrefix(p, "/repository/files/"), "%2F", "/")
		if g.files[r.URL.Query().Get("ref")+":"+path] {
			reply(200, map[string]string{})
		} else {
			reply(404, map[string]string{"message": "404 File Not Found"})
		}
	case p == "/repository/commits":
		g.commits = append(g.commits, body)
		b := body["branch"].(string)
		if from, _ := body["start_branch"].(string); from != "" && !g.branches[b] {
			for key, text := range g.contents { // the new branch starts as the one it's from
				if fb, fp, _ := strings.Cut(key, ":"); fb == from {
					g.files[b+":"+fp], g.contents[b+":"+fp] = true, text
				}
			}
		}
		g.branches[b] = true
		for _, a := range body["actions"].([]any) {
			act := a.(map[string]any)
			fp := b + ":" + act["file_path"].(string)
			if act["action"] == "delete" {
				delete(g.files, fp)
				delete(g.contents, fp)
				continue
			}
			g.files[fp] = true
			if g.contents != nil {
				g.contents[fp], _ = act["content"].(string)
			}
		}
		reply(201, map[string]string{"id": "0123456789abcdef0123"})
	case p == "/merge_requests" && r.Method == "GET":
		var open []map[string]any
		for _, mr := range g.mrs {
			if src := r.URL.Query().Get("source_branch"); src == "" || mr["source_branch"] == src {
				open = append(open, mr)
			}
		}
		reply(200, open)
	case p == "/merge_requests":
		body["iid"], body["web_url"] = len(g.mrs)+1, "https://gitlab.example/sales/pipelines/-/merge_requests/1"
		g.mrs = append(g.mrs, body)
		reply(201, body)
	default:
		reply(404, map[string]string{"message": "404 " + p})
	}
}

func TestANotebookIsScheduledByAMergeRequest(t *testing.T) {
	gl := &fakeGitLab{branches: map[string]bool{"main": true}, files: map[string]bool{}, token: true}
	ts := httptest.NewServer(gl)
	defer ts.Close()
	sales, err := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"dana","role":"editor"},{"user":"alice","role":"reader"}],
		"tables":{},"pipelines":{"serviceAccount":"sales-pipelines","repo":"` + ts.URL + `/sales/pipelines.git","path":"dags","credentials":{"secretRef":"sales-git"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	const pipelinesAccount = "service-account-project-sales-pipelines"
	st := &connsKept{projectsKept: projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Members: []project.Person{
		{Username: "dana", Role: "editor"}, {Username: "alice", Role: "reader"}, {Username: pipelinesAccount, Role: "pipelines"}}}}},
		specs: map[string]*project.Project{"sales": sales}},
		conns: map[string]store.StoredConnection{"sales/sales-git": {ConnectionMeta: store.ConnectionMeta{Name: "sales-git"},
			Data: map[string]string{"username": "bot", "password": "repo-token"}}}}
	files := kept{}
	s := &server{cfg: config{Groups: []string{"analysts"}}, store: st, files: files, now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), tokenUser: func(raw string) (string, error) { return raw, nil },
		sessions: map[string]*session{"alice": {User: user{Username: "alice", Groups: []string{"analysts"}}, Expires: time.Now().Add(time.Hour)}}}
	h := s.routes()
	call := func(token, method, path, body string) (int, map[string]any, http.Header) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token == "alice" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Header()
	}

	// dana may schedule in sales; alice (a reader) may not.
	if code, out, _ := call("dana", "GET", "/api/notebooks/projects", ""); code != 200 || !strings.Contains(mustJSON(out), `{"name":"sales"}`) {
		t.Errorf("dana's projects: %d %v", code, out)
	}
	nb := `{"cells": [{"cell_type": "code", "source": "1 + 1", "outputs": []}], "metadata": {}, "nbformat": 4, "nbformat_minor": 5}`
	schedule := `{"path": "work/Churn Features.ipynb", "schedule": "0 5 * * *", "notebook": ` + nb + `}`
	if code, _, _ := call("alice", "POST", "/api/projects/sales/notebooks/schedules", schedule); code != http.StatusUnauthorized {
		t.Errorf("a session, not a token: %d", code)
	}
	if code, _, _ := call("bob", "POST", "/api/projects/sales/notebooks/schedules", schedule); code != http.StatusForbidden {
		t.Errorf("not a member: %d", code)
	}
	if code, _, _ := call("dana", "POST", "/api/projects/sales/notebooks/schedules", strings.Replace(schedule, "0 5 * * *", "daily", 1)); code != http.StatusUnprocessableEntity {
		t.Errorf("not cron: %d", code)
	}
	code, out, _ := call("dana", "POST", "/api/projects/sales/notebooks/schedules", schedule)
	if code != 200 || out["dag"] != "notebook_churn_features" || out["branch"] != "storscale/schedule-churn-features" || out["new"] != true {
		t.Fatalf("scheduled: %d %v", code, out)
	}
	c := gl.commits[0]
	acts := c["actions"].([]any)
	nbFile, dagFile := acts[0].(map[string]any), acts[1].(map[string]any)
	if c["start_branch"] != "main" || nbFile["action"] != "create" || nbFile["file_path"] != "dags/notebooks/churn_features.ipynb" ||
		dagFile["file_path"] != "dags/notebook_churn_features.py" ||
		!strings.Contains(dagFile["content"].(string), `notebook.schedule(project="sales", name="churn_features", schedule="0 5 * * *", owner="dana")`) {
		t.Errorf("the commit: %v", c)
	}
	// Airflow's safe mode parses only files with both words in them.
	if dag := strings.ToLower(dagFile["content"].(string)); !strings.Contains(dag, "airflow") || !strings.Contains(dag, "dag") {
		t.Errorf("Airflow wouldn't read the DAG file: %s", dagFile["content"])
	}
	if len(gl.mrs) != 1 || gl.mrs[0]["target_branch"] != "main" {
		t.Errorf("the merge request: %v", gl.mrs)
	}
	// Scheduled again (another time): the same branch, its files updated, the same merge request.
	call("dana", "POST", "/api/projects/sales/notebooks/schedules", strings.Replace(schedule, "0 5", "0 6", 1))
	if c := gl.commits[1]; c["start_branch"] != nil || !strings.Contains(mustJSON(c["actions"]), `"action":"update"`) || len(gl.mrs) != 1 {
		t.Errorf("again: %v, %d merge requests", c, len(gl.mrs))
	}
	if !gl.token {
		t.Error("a call to GitLab without the repo's token")
	}

	// A run's notebook, from the pipelines; shown to members, scripts not run.
	run := "/api/projects/sales/notebooks/runs/notebook_churn_features/scheduled__2026-10-19T05:00:00+00:00"
	if code, _, _ := call("dana", "POST", run, `{"notebook": {}, "html": "<p>x</p>"}`); code != http.StatusForbidden {
		t.Errorf("a run's notebook from dana: %d", code)
	}
	if code, _, _ := call(pipelinesAccount, "POST", run, `{"notebook": `+nb+`, "html": "<p>2</p><script>x()</script>"}`); code != 200 {
		t.Fatalf("a run's notebook: %d", code)
	}
	r := httptest.NewRequest("GET", run, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "alice"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<p>2</p>") || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox;") {
		t.Errorf("alice reads it: %d %s %q", w.Code, w.Body.String(), w.Header().Get("Content-Security-Policy"))
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestNotebookNames(t *testing.T) {
	for in, want := range map[string]string{"work/Churn Features.ipynb": "churn_features", "2026-report.ipynb": "nb_2026_report", "a__b.ipynb": "a_b"} {
		if got := notebookName(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
