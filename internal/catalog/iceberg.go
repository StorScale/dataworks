package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LastChange is when a table last changed: its current snapshot's time, as
// the Iceberg catalog (Nessie's REST API, http://nessie:19120/iceberg) has
// it. Zero, with no error, for a table with no snapshot yet.
func LastChange(ctx context.Context, c *http.Client, base, warehouse, namespace, table string) (time.Time, error) {
	base = strings.TrimRight(base, "/")
	get := func(path string, out any) error {
		req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		if err != nil {
			return err
		}
		res, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("iceberg: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return errNotFound
		}
		if res.StatusCode != 200 {
			return fmt.Errorf("iceberg: GET %s: HTTP %d", path, res.StatusCode)
		}
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return fmt.Errorf("iceberg: GET %s: %v", path, err)
		}
		return nil
	}
	var cfg struct {
		Defaults  map[string]any `json:"defaults"` // strings, by the spec; not every catalog keeps to it
		Overrides map[string]any `json:"overrides"`
	}
	if err := get("/v1/config?warehouse="+url.QueryEscape(warehouse), &cfg); err != nil {
		return time.Time{}, err
	}
	prefix, _ := cfg.Overrides["prefix"].(string)
	if prefix == "" {
		prefix, _ = cfg.Defaults["prefix"].(string)
	}
	path := "/v1/"
	if prefix != "" {
		path += url.PathEscape(prefix) + "/"
	}
	var t struct {
		Metadata struct {
			Current   *int64 `json:"current-snapshot-id"`
			Snapshots []struct {
				ID int64 `json:"snapshot-id"`
				At int64 `json:"timestamp-ms"`
			} `json:"snapshots"`
		} `json:"metadata"`
	}
	if err := get(path+"namespaces/"+url.PathEscape(namespace)+"/tables/"+url.PathEscape(table), &t); err != nil {
		if err == errNotFound {
			return time.Time{}, fmt.Errorf("no table %s.%s", namespace, table)
		}
		return time.Time{}, err
	}
	for _, s := range t.Metadata.Snapshots {
		if t.Metadata.Current != nil && s.ID == *t.Metadata.Current {
			return time.UnixMilli(s.At).UTC(), nil
		}
	}
	return time.Time{}, nil
}
