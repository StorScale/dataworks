package reconcile

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
)

// Nessie is the catalog's Iceberg REST API.
type Nessie struct {
	URL, Warehouse string
}

func (n *Nessie) prefix(ctx context.Context) (string, error) {
	var cfg struct {
		Defaults  map[string]string `json:"defaults"`
		Overrides map[string]string `json:"overrides"`
	}
	if err := call(ctx, "GET", n.URL+"/iceberg/v1/config?warehouse="+url.QueryEscape(n.Warehouse), nil, nil, &cfg); err != nil {
		return "", err
	}
	if p := cfg.Overrides["prefix"]; p != "" {
		return p, nil
	}
	return cfg.Defaults["prefix"], nil
}

// ensureNamespace creates the namespace when it isn't there.
func (n *Nessie) ensureNamespace(ctx context.Context, namespace string) error {
	prefix, err := n.prefix(ctx)
	if err != nil {
		return err
	}
	base := n.URL + "/iceberg/v1/" + prefix + "/namespaces" // Nessie gives the prefix URL-encoded already
	err = call(ctx, "GET", base+"/"+url.PathEscape(namespace), nil, nil, nil)
	if !isStatus(err, http.StatusNotFound) {
		return err
	}
	err = call(ctx, "POST", base, nil, map[string]any{"namespace": []string{namespace}}, nil)
	if isStatus(err, http.StatusConflict) {
		return nil
	}
	return err
}

func (n *Nessie) api() *catalog.Nessie {
	return &catalog.Nessie{HTTP: &http.Client{Timeout: 30 * time.Second}, Base: n.URL + "/api/v2"}
}

// syncTags makes the project's Nessie tags (<project>.<name>) as its spec
// says: each at its commit; those no longer listed removed. Tags not the
// project's (another prefix) are left alone.
func (n *Nessie) syncTags(ctx context.Context, p *project.Project) error {
	api := n.api()
	have, err := api.Tags(ctx, p.Metadata.Name+".")
	if err != nil {
		return err
	}
	byName := map[string]catalog.Ref{}
	for _, r := range have {
		byName[r.Name] = r
	}
	want := map[string]bool{}
	if t := p.Spec.Tables; t != nil {
		for _, tg := range t.Tags {
			name := p.NessieTag(tg)
			want[name] = true
			cur, ok := byName[name]
			if ok && cur.Hash == tg.Commit {
				continue
			}
			if ok { // moved: Nessie's tags are made again
				if err := api.Untag(ctx, name, cur.Hash); err != nil {
					return err
				}
			}
			if _, err := api.Tag(ctx, name, tg.Commit); err != nil {
				return err
			}
		}
	}
	for name, r := range byName {
		if !want[name] {
			if err := api.Untag(ctx, name, r.Hash); err != nil {
				return err
			}
		}
	}
	return nil
}
