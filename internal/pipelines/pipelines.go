// Package pipelines reads a project's pipelines and their runs from Airflow,
// and starts runs, as the platform's own Airflow account. A project's
// pipelines are the DAGs of its own bundle (project-<name>: its pipelines
// repo), and those of the platform's own bundle tagged project:<name>. A tag
// on a DAG from another project's repo counts for nothing.
package pipelines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type Client struct {
	URL, User, Password string // URL: Airflow's base (http://airflow:8090/pipelines)
	http                http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func New(url, user, password string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), User: user, Password: password, http: http.Client{Timeout: 30 * time.Second}}
}

type Pipeline struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Paused      bool   `json:"paused"`
	Bundle      string `json:"bundle"`           // project-<name>, or the platform's own
	Commit      string `json:"commit,omitempty"` // of the repo, as Airflow last parsed it
	// ClashesWith are other bundles this DAG ID has also come from: two repos
	// define it, and Airflow keeps whichever it parsed last.
	ClashesWith     []string `json:"clashesWith,omitempty"`
	HasImportErrors bool     `json:"hasImportErrors"`
	Runs            []Run    `json:"runs"`  // newest first
	Tasks           []Task   `json:"tasks"` // the newest run's
}

type Run struct {
	ID          string    `json:"id"`
	State       string    `json:"state"` // queued, running, success, failed
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended"`
	RequestedBy string    `json:"requestedBy,omitempty"` // who asked the platform to start it
	Type        string    `json:"type"`                  // manual, scheduled
	Commit      string    `json:"commit,omitempty"`      // the code it ran
}

// ImportError is a file of the project's repo Airflow couldn't load.
type ImportError struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// PlatformBundle holds the platform's own DAGs (Airflow's dags folder).
const PlatformBundle = "dags-folder"

// StorscaleBundle holds the platform's DAGs in its Airflow image (a merge
// request's review, a project's deliveries), in every Airflow.
const StorscaleBundle = "storscale"

// Bundle is a project's own bundle, its pipelines repo.
func Bundle(project string) string { return "project-" + project }

type dag struct {
	ID              string  `json:"dag_id"`
	Description     *string `json:"description"`
	Paused          bool    `json:"is_paused"`
	BundleName      *string `json:"bundle_name"`
	BundleVersion   *string `json:"bundle_version"`
	HasImportErrors bool    `json:"has_import_errors"`
}

// dags are all the DAGs a query finds, a page at a time.
func (c *Client) dags(ctx context.Context, q url.Values) ([]dag, error) {
	var out []dag
	for offset := 0; ; offset += 100 {
		q.Set("limit", "100")
		q.Set("offset", fmt.Sprint(offset))
		var page struct {
			DAGs  []dag `json:"dags"`
			Total int   `json:"total_entries"`
		}
		if err := c.call(ctx, "GET", "/dags?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.DAGs...)
		if len(page.DAGs) < 100 || len(out) >= page.Total {
			return out, nil
		}
	}
}

// clashes are the other bundles a DAG's versions came from.
func (c *Client) clashes(ctx context.Context, id, bundle string) ([]string, error) {
	var vs struct {
		Versions []struct {
			Bundle *string `json:"bundle_name"`
		} `json:"dag_versions"`
	}
	if err := c.call(ctx, "GET", "/dags/"+url.PathEscape(id)+"/dagVersions?limit=100&order_by=-version_number", nil, &vs); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range vs.Versions {
		if v.Bundle != nil && *v.Bundle != bundle && !slices.Contains(out, *v.Bundle) {
			out = append(out, *v.Bundle)
		}
	}
	return out, nil
}

type Task struct {
	ID      string    `json:"id"`
	State   string    `json:"state"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
}

func (c *Client) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{"username": c.User, "password": c.Password})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.URL+"/auth/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if res.StatusCode/100 != 2 || json.NewDecoder(res.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("Airflow refused the platform's account (HTTP %d)", res.StatusCode)
	}
	c.token, c.expires = tok.AccessToken, time.Now().Add(10*time.Minute)
	return c.token, nil
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	tok, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, c.URL+"/api/v2"+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
	}
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return &APIError{Method: method, Path: path, Code: res.StatusCode, Body: string(msg)}
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// Of is a project's pipelines, each with its recent runs and its newest run's tasks.
func (c *Client) Of(ctx context.Context, project string, runs int) ([]Pipeline, error) {
	own, err := c.dags(ctx, url.Values{"bundle_name": {Bundle(project)}})
	if err != nil {
		return nil, err
	}
	tagged, err := c.dags(ctx, url.Values{"bundle_name": {PlatformBundle}, "tags": {"project:" + project}})
	if err != nil {
		return nil, err
	}
	// The platform's DAGs from its image (a project's deliveries), tagged as the project's.
	platform, err := c.dags(ctx, url.Values{"bundle_name": {StorscaleBundle}, "tags": {"project:" + project}})
	if err != nil {
		return nil, err
	}
	out := []Pipeline{}
	for _, d := range append(append(own, tagged...), platform...) {
		p := Pipeline{ID: d.ID, Paused: d.Paused, HasImportErrors: d.HasImportErrors, Runs: []Run{}, Tasks: []Task{}}
		if d.Description != nil {
			p.Description = *d.Description
		}
		if d.BundleName != nil {
			p.Bundle = *d.BundleName
		}
		if d.BundleVersion != nil {
			p.Commit = *d.BundleVersion
		}
		if p.ClashesWith, err = c.clashes(ctx, d.ID, p.Bundle); err != nil {
			return nil, err
		}
		var rs struct {
			Runs []struct {
				ID      string         `json:"dag_run_id"`
				State   string         `json:"state"`
				Started *time.Time     `json:"start_date"`
				Ended   *time.Time     `json:"end_date"`
				Type    string         `json:"run_type"`
				Conf    map[string]any `json:"conf"`
				Commit  *string        `json:"bundle_version"`
			} `json:"dag_runs"`
		}
		if err := c.call(ctx, "GET", fmt.Sprintf("/dags/%s/dagRuns?order_by=-start_date&limit=%d", url.PathEscape(d.ID), runs), nil, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs.Runs {
			run := Run{ID: r.ID, State: r.State, Type: r.Type, Started: deref(r.Started), Ended: deref(r.Ended)}
			if r.Commit != nil {
				run.Commit = *r.Commit
			}
			if by, ok := r.Conf["requested_by"].(string); ok {
				run.RequestedBy = by
			}
			p.Runs = append(p.Runs, run)
		}
		if len(p.Runs) > 0 {
			var ts struct {
				Tasks []struct {
					ID      string     `json:"task_id"`
					State   *string    `json:"state"`
					Started *time.Time `json:"start_date"`
					Ended   *time.Time `json:"end_date"`
				} `json:"task_instances"`
			}
			path := fmt.Sprintf("/dags/%s/dagRuns/%s/taskInstances", url.PathEscape(d.ID), url.PathEscape(p.Runs[0].ID))
			if err := c.call(ctx, "GET", path, nil, &ts); err != nil {
				return nil, err
			}
			for _, t := range ts.Tasks {
				task := Task{ID: t.ID, Started: deref(t.Started), Ended: deref(t.Ended)}
				if t.State != nil {
					task.State = *t.State
				}
				p.Tasks = append(p.Tasks, task)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// ImportErrors are the files of the project's repo Airflow couldn't load.
func (c *Client) ImportErrors(ctx context.Context, project string) ([]ImportError, error) {
	out := []ImportError{}
	for offset := 0; ; offset += 100 {
		var page struct {
			Errors []struct {
				File   string  `json:"filename"`
				Bundle *string `json:"bundle_name"`
				Stack  string  `json:"stack_trace"`
			} `json:"import_errors"`
		}
		if err := c.call(ctx, "GET", fmt.Sprintf("/importErrors?limit=100&offset=%d", offset), nil, &page); err != nil {
			return nil, err
		}
		for _, e := range page.Errors {
			if e.Bundle != nil && *e.Bundle == Bundle(project) {
				msg := e.Stack
				if len(msg) > 2000 {
					msg = msg[len(msg)-2000:] // the end says what went wrong
				}
				out = append(out, ImportError{File: e.File, Error: msg})
			}
		}
		if len(page.Errors) < 100 {
			return out, nil
		}
	}
}

// Start starts a run of a project's pipeline, recording who asked for it.
// It's refused when the pipeline isn't the project's.
func (c *Client) Start(ctx context.Context, project, pipeline, requestedBy string) (string, error) {
	ps, err := c.Of(ctx, project, 1)
	if err != nil {
		return "", err
	}
	found := false
	for _, p := range ps {
		found = found || p.ID == pipeline
	}
	if !found {
		return "", fmt.Errorf("no pipeline %q in project %s", pipeline, project)
	}
	var run struct {
		ID string `json:"dag_run_id"`
	}
	err = c.call(ctx, "POST", "/dags/"+url.PathEscape(pipeline)+"/dagRuns", map[string]any{
		"logical_date": nil, "conf": map[string]string{"requested_by": requestedBy}}, &run)
	return run.ID, err
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// APIError is Airflow's API refusing a request.
type APIError struct {
	Method, Path string
	Code         int
	Body         string // Airflow's answer, which may echo what was sent
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Airflow: %s %s: HTTP %d %s", e.Method, e.Path, e.Code, e.Body)
}

func code(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// A Connection is one of Airflow's connections (its REST API's shape). The
// platform keeps each project's git credentials in one, encrypted in
// Airflow's database, for the project's DAG bundle.
type Connection struct {
	ID          string `json:"connection_id"`
	Type        string `json:"conn_type"`
	Host        string `json:"host,omitempty"`
	Login       string `json:"login,omitempty"`
	Password    string `json:"password,omitempty"`
	Extra       string `json:"extra,omitempty"` // JSON
	Schema      string `json:"schema,omitempty"`
	Port        int    `json:"port,omitempty"`
	Description string `json:"description,omitempty"`
}

// PutConnection makes Airflow hold the connection as given, creating it or
// replacing what it held.
func (c *Client) PutConnection(ctx context.Context, conn Connection) error {
	path := "/connections/" + url.PathEscape(conn.ID)
	err := c.call(ctx, "PATCH", path, conn, nil)
	if code(err) == http.StatusNotFound {
		err = c.call(ctx, "POST", "/connections", conn, nil)
	}
	return err
}

// Trigger starts a run of a DAG with this run id and conf; a run of that id
// already there is no error (started is false): a run per id, once.
func (c *Client) Trigger(ctx context.Context, dag, runID string, conf map[string]any) (started bool, err error) {
	err = c.call(ctx, "POST", "/dags/"+url.PathEscape(dag)+"/dagRuns", map[string]any{
		"dag_run_id": runID, "logical_date": nil, "conf": conf}, nil)
	if code(err) == http.StatusConflict {
		return false, nil
	}
	return err == nil, err
}

// DeleteConnection removes a connection; one that isn't there is no error.
func (c *Client) DeleteConnection(ctx context.Context, id string) error {
	err := c.call(ctx, "DELETE", "/connections/"+url.PathEscape(id), nil, nil)
	if code(err) == http.StatusNotFound {
		return nil
	}
	return err
}
