// Package pipelines reads a project's pipelines and their runs from Airflow,
// and starts runs, as the platform's own Airflow account. A pipeline belongs
// to a project when its DAG is tagged project:<name>.
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
	Runs        []Run  `json:"runs"`  // newest first
	Tasks       []Task `json:"tasks"` // the newest run's
}

type Run struct {
	ID          string    `json:"id"`
	State       string    `json:"state"` // queued, running, success, failed
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended"`
	RequestedBy string    `json:"requestedBy,omitempty"` // who asked the platform to start it
	Type        string    `json:"type"`                  // manual, scheduled
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
	var dags struct {
		DAGs []struct {
			ID          string  `json:"dag_id"`
			Description *string `json:"description"`
			Paused      bool    `json:"is_paused"`
		} `json:"dags"`
	}
	if err := c.call(ctx, "GET", "/dags?limit=100&tags="+url.QueryEscape("project:"+project), nil, &dags); err != nil {
		return nil, err
	}
	out := []Pipeline{}
	for _, d := range dags.DAGs {
		p := Pipeline{ID: d.ID, Paused: d.Paused, Runs: []Run{}, Tasks: []Task{}}
		if d.Description != nil {
			p.Description = *d.Description
		}
		var rs struct {
			Runs []struct {
				ID      string         `json:"dag_run_id"`
				State   string         `json:"state"`
				Started *time.Time     `json:"start_date"`
				Ended   *time.Time     `json:"end_date"`
				Type    string         `json:"run_type"`
				Conf    map[string]any `json:"conf"`
			} `json:"dag_runs"`
		}
		if err := c.call(ctx, "GET", fmt.Sprintf("/dags/%s/dagRuns?order_by=-start_date&limit=%d", url.PathEscape(d.ID), runs), nil, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs.Runs {
			run := Run{ID: r.ID, State: r.State, Type: r.Type, Started: deref(r.Started), Ended: deref(r.Ended)}
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
	ID       string `json:"connection_id"`
	Type     string `json:"conn_type"`
	Host     string `json:"host,omitempty"`
	Login    string `json:"login,omitempty"`
	Password string `json:"password,omitempty"`
	Extra    string `json:"extra,omitempty"` // JSON
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

// DeleteConnection removes a connection; one that isn't there is no error.
func (c *Client) DeleteConnection(ctx context.Context, id string) error {
	err := c.call(ctx, "DELETE", "/connections/"+url.PathEscape(id), nil, nil)
	if code(err) == http.StatusNotFound {
		return nil
	}
	return err
}
