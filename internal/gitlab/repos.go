package gitlab

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// What project templates and shared libraries need (use case U.2), signed
// in with the install's token (one allowed to make projects in its group):
// a template's repo forked into the group as a new project's, unlinked,
// with a token of its own; and a library's files at a ref.

// A Repo is a GitLab project, as GitLab describes it.
type Repo struct {
	ID            int    `json:"id"`
	Path          string `json:"path_with_namespace"`
	HTTPURL       string `json:"http_url_to_repo"`
	WebURL        string `json:"web_url"`
	DefaultBranch string `json:"default_branch"`
	ImportStatus  string `json:"import_status"` // a fork's copy: scheduled, started, finished, failed (none: not a copy)
	ImportError   string `json:"import_error"`
}

// Repo is the project's description.
func (c *Client) Repo(ctx context.Context) (Repo, error) {
	var r Repo
	return r, c.call(ctx, "GET", "", nil, &r)
}

// Fork copies the repo into a group (its full path), as a project named
// name at path. GitLab copies it in the background: see WaitForCopy.
func (c *Client) Fork(ctx context.Context, group, name, path, description string) (Repo, error) {
	var r Repo
	return r, c.call(ctx, "POST", "/fork", map[string]string{"namespace_path": group, "name": name, "path": path, "description": description}, &r)
}

// WaitForCopy waits until GitLab has copied a fork (at most within).
func (c *Client) WaitForCopy(ctx context.Context, within time.Duration) (Repo, error) {
	deadline := time.Now().Add(within)
	for {
		r, err := c.Repo(ctx)
		if err != nil {
			return r, err
		}
		switch r.ImportStatus {
		case "finished", "none", "":
			return r, nil
		case "failed":
			return r, fmt.Errorf("gitlab: copying the template's repo failed: %s", r.ImportError)
		}
		if time.Now().After(deadline) {
			return r, fmt.Errorf("gitlab: the template's repo wasn't copied within %s (still %s)", within, r.ImportStatus)
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Unfork ends a fork's link to the repo it was copied from: the new repo is its own.
func (c *Client) Unfork(ctx context.Context) error {
	err := c.call(ctx, "DELETE", "/fork", nil, nil)
	if e, ok := err.(*Error); ok && e.Code == http.StatusNotModified {
		return nil // not a fork
	}
	return err
}

// Delete removes the project (a new project's repo, when making the project failed).
func (c *Client) Delete(ctx context.Context) error {
	return c.call(ctx, "DELETE", "", nil, nil)
}

// AccessToken makes a token of the project's own: a bot member with a role
// (30, Developer) and scopes (api, read_repository), until expires.
func (c *Client) AccessToken(ctx context.Context, name string, scopes []string, level int, expires time.Time) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	return out.Token, c.call(ctx, "POST", "/access_tokens", map[string]any{"name": name, "scopes": scopes, "access_level": level,
		"expires_at": expires.UTC().Format("2006-01-02")}, &out)
}

// CommitOf is the commit a ref (a branch, a tag, a commit) names.
func (c *Client) CommitOf(ctx context.Context, ref string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	return out.ID, c.call(ctx, "GET", "/repository/commits/"+url.PathEscape(ref), nil, &out)
}

// Archive is the repo's files at a commit, as a .tar.gz (one top folder,
// named by GitLab). The caller closes it.
func (c *Client) Archive(ctx context.Context, sha string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/api/v4/projects/"+url.PathEscape(c.Project)+"/repository/archive.tar.gz?sha="+url.QueryEscape(sha), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	res, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, &Error{Code: res.StatusCode, Message: fmt.Sprintf("gitlab: %s's archive at %s: HTTP %d", c.Project, sha, res.StatusCode)}
	}
	return res.Body, nil
}
