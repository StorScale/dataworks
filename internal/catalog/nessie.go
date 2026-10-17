package catalog

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

// Nessie is Nessie's own REST API (http://nessie:19120/api/v2): the
// catalog's history (its commits on main), and its tags. A table is read as
// it was through its name at a commit: "orders#<commit>".
type Nessie struct {
	HTTP *http.Client
	Base string // http://nessie:19120/api/v2
}

// A Ref is a branch or tag, and the commit it points at.
type Ref struct {
	Type string `json:"type"` // BRANCH, TAG
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// A Commit is one change to the catalog.
type Commit struct {
	Hash    string    `json:"hash"`
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
	Author  string    `json:"author,omitempty"`
	Keys    []string  `json:"keys,omitempty"` // the tables it changed: sales.orders
}

func (n *Nessie) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(n.Base, "/")+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := n.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("nessie: %v", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("nessie: %s %s: HTTP %d: %s", method, path, res.StatusCode, e.Message)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Ref is a branch or tag; nil, with no error, when there's none.
func (n *Nessie) Ref(ctx context.Context, name string) (*Ref, error) {
	var r struct {
		Reference Ref `json:"reference"`
	}
	if err := n.do(ctx, "GET", "/trees/"+url.PathEscape(name), nil, &r); err != nil {
		if err == errNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &r.Reference, nil
}

// Tags are the catalog's tags whose names begin with prefix.
func (n *Nessie) Tags(ctx context.Context, prefix string) ([]Ref, error) {
	var out []Ref
	token := ""
	for {
		q := url.Values{"max-records": {"500"}}
		if token != "" {
			q.Set("page-token", token)
		}
		var page struct {
			References []Ref  `json:"references"`
			HasMore    bool   `json:"hasMore"`
			Token      string `json:"token"`
		}
		if err := n.do(ctx, "GET", "/trees?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, r := range page.References {
			if r.Type == "TAG" && strings.HasPrefix(r.Name, prefix) {
				out = append(out, r)
			}
		}
		if !page.HasMore || page.Token == "" {
			return out, nil
		}
		token = page.Token
	}
}

// Tag makes a tag at a commit of main.
func (n *Nessie) Tag(ctx context.Context, name, hash string) (*Ref, error) {
	var r struct {
		Reference Ref `json:"reference"`
	}
	q := url.Values{"name": {name}, "type": {"TAG"}}
	err := n.do(ctx, "POST", "/trees?"+q.Encode(), Ref{Type: "BRANCH", Name: "main", Hash: hash}, &r)
	return &r.Reference, err
}

// Untag removes a tag (as it points at hash).
func (n *Nessie) Untag(ctx context.Context, name, hash string) error {
	return n.do(ctx, "DELETE", "/trees/"+url.PathEscape(name+"@"+hash), nil, nil)
}

type logPage struct {
	LogEntries []struct {
		CommitMeta struct {
			Hash       string    `json:"hash"`
			Message    string    `json:"message"`
			Committer  string    `json:"committer"`
			Authors    []string  `json:"authors"`
			CommitTime time.Time `json:"commitTime"`
		} `json:"commitMeta"`
		Operations []struct {
			Key struct {
				Elements []string `json:"elements"`
			} `json:"key"`
		} `json:"operations"`
	} `json:"logEntries"`
	HasMore bool   `json:"hasMore"`
	Token   string `json:"token"`
}

// History is main's commits, newest first, those that changed a key (as
// sales.orders: "" for every commit), at most limit of them, looking back
// at most scan commits.
func (n *Nessie) History(ctx context.Context, key string, limit, scan int) ([]Commit, error) {
	var out []Commit
	token, seen := "", 0
	for {
		q := url.Values{"max-records": {"200"}, "fetch": {"ALL"}}
		if token != "" {
			q.Set("page-token", token)
		}
		var page logPage
		if err := n.do(ctx, "GET", "/trees/main/history?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, e := range page.LogEntries {
			seen++
			c := Commit{Hash: e.CommitMeta.Hash, Time: e.CommitMeta.CommitTime, Message: e.CommitMeta.Message, Author: e.CommitMeta.Committer}
			if len(e.CommitMeta.Authors) > 0 && e.CommitMeta.Authors[0] != "" {
				c.Author = e.CommitMeta.Authors[0]
			}
			touched := false
			for _, op := range e.Operations {
				k := strings.Join(op.Key.Elements, ".")
				c.Keys = append(c.Keys, k)
				touched = touched || k == key
			}
			if key == "" || touched {
				out = append(out, c)
				if len(out) >= limit {
					return out, nil
				}
			}
		}
		if !page.HasMore || page.Token == "" || seen >= scan {
			return out, nil
		}
		token = page.Token
	}
}

// At is the commit of main that was current at a time: the newest commit
// at or before it (nil if the history kept starts after it).
func (n *Nessie) At(ctx context.Context, t time.Time, scan int) (*Commit, error) {
	token, seen := "", 0
	for {
		q := url.Values{"max-records": {"200"}}
		if token != "" {
			q.Set("page-token", token)
		}
		var page logPage
		if err := n.do(ctx, "GET", "/trees/main/history?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, e := range page.LogEntries { // newest first
			seen++
			if !e.CommitMeta.CommitTime.After(t) {
				return &Commit{Hash: e.CommitMeta.Hash, Time: e.CommitMeta.CommitTime, Message: e.CommitMeta.Message}, nil
			}
		}
		if !page.HasMore || page.Token == "" || seen >= scan {
			return nil, nil
		}
		token = page.Token
	}
}
