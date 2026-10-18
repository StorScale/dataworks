// Package gitlab is the little of GitLab's API (v4) a project's reviews need
// (use case 7.3): its open merge requests, a commit's status, a note on a
// merge request. It signs in with the repo's token (PRIVATE-TOKEN).
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	Base    string // https://gitlab.example.com
	Token   string
	Project string // its path, group/repo
	HTTP    *http.Client
}

func New(base, token, project string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, Project: project, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// A MergeRequest is an open one, as GitLab lists it.
type MergeRequest struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	SHA          string `json:"sha"` // its head commit
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	WebURL       string `json:"web_url"`
}

// ProjectPath is a repo's path in GitLab, from its URL (https://host/group/repo.git).
func ProjectPath(repo string) string {
	u, err := url.Parse(repo)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/api/v4/projects/"+url.PathEscape(c.Project)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Message any `json:"message"`
		}
		json.Unmarshal(data, &e)
		return fmt.Errorf("gitlab: %s %s: HTTP %d %v", method, path, res.StatusCode, e.Message) // never the token
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// OpenMergeRequests are the project's open merge requests into target.
func (c *Client) OpenMergeRequests(ctx context.Context, target string) ([]MergeRequest, error) {
	var out []MergeRequest
	q := url.Values{"state": {"opened"}, "target_branch": {target}, "per_page": {"100"}}
	return out, c.call(ctx, "GET", "/merge_requests?"+q.Encode(), nil, &out)
}

// Status sets a commit's status, as its pipelines would (the merge request
// shows it): state is pending, running, success, failed or canceled.
func (c *Client) Status(ctx context.Context, sha, state, name, description, target string) error {
	in := map[string]string{"state": state, "name": name, "description": description}
	if target != "" {
		in["target_url"] = target
	}
	return c.call(ctx, "POST", "/statuses/"+url.PathEscape(sha), in, nil)
}

// Note comments on a merge request.
func (c *Client) Note(ctx context.Context, iid int, body string) error {
	return c.call(ctx, "POST", fmt.Sprintf("/merge_requests/%d/notes", iid), map[string]string{"body": body}, nil)
}
