package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Iceberg is the Iceberg catalog's REST API (Nessie's,
// http://nessie:19120/iceberg), for one warehouse.
type Iceberg struct {
	HTTP      *http.Client
	Base      string
	Warehouse string
}

func (ic *Iceberg) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(ic.Base, "/")+path, nil)
	if err != nil {
		return err
	}
	res, err := ic.HTTP.Do(req)
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

// root is the warehouse's path: /v1/ and its prefix, as the catalog gives it.
func (ic *Iceberg) root(ctx context.Context) (string, error) {
	var cfg struct {
		Defaults  map[string]any `json:"defaults"` // strings, by the spec; not every catalog keeps to it
		Overrides map[string]any `json:"overrides"`
	}
	if err := ic.get(ctx, "/v1/config?warehouse="+url.QueryEscape(ic.Warehouse), &cfg); err != nil {
		return "", err
	}
	prefix, _ := cfg.Overrides["prefix"].(string)
	if prefix == "" {
		prefix, _ = cfg.Defaults["prefix"].(string)
	}
	// The prefix goes in the path as the catalog gives it: Nessie's comes
	// escaped already (main%7Cwarehouse); escaped again it's refused.
	if strings.ContainsAny(prefix, "/?#") {
		return "", fmt.Errorf("iceberg: the catalog's prefix %q isn't one path segment", prefix)
	}
	if prefix == "" {
		return "/v1/", nil
	}
	return "/v1/" + prefix + "/", nil
}

// Tables are a namespace's tables, by name.
func (ic *Iceberg) Tables(ctx context.Context, namespace string) ([]string, error) {
	root, err := ic.root(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	token := ""
	for {
		var page struct {
			Identifiers []struct {
				Name string `json:"name"`
			} `json:"identifiers"`
			Next string `json:"next-page-token"`
		}
		path := root + "namespaces/" + url.PathEscape(namespace) + "/tables"
		if token != "" {
			path += "?pageToken=" + url.QueryEscape(token)
		}
		if err := ic.get(ctx, path, &page); err != nil {
			if err == errNotFound {
				return nil, nil // no namespace yet: no tables
			}
			return nil, err
		}
		for _, id := range page.Identifiers {
			out = append(out, id.Name)
		}
		if page.Next == "" || page.Next == token {
			sort.Strings(out)
			return out, nil
		}
		token = page.Next
	}
}

// Stats are what a table holds now (its current snapshot's summary) and
// how far back it goes (its snapshots).
type Stats struct {
	DataFiles   int64     `json:"dataFiles"`
	Bytes       int64     `json:"bytes"`
	Rows        int64     `json:"rows"`
	Snapshots   int       `json:"snapshots"`
	Oldest      time.Time `json:"oldest"`      // the oldest snapshot's time
	LastChange  time.Time `json:"lastChange"`  // the current snapshot's
	Refs        int       `json:"refs"`        // branches and tags, main among them
	DeleteFiles int64     `json:"deleteFiles"` // position and equality deletes
	// The current snapshot's change: what it did (append, overwrite,
	// delete), and the rows and files it added and removed.
	Operation    string `json:"operation,omitempty"`
	AddedRows    int64  `json:"addedRows"`
	DeletedRows  int64  `json:"deletedRows"`
	AddedFiles   int64  `json:"addedFiles"`
	DeletedFiles int64  `json:"deletedFiles"`
}

type tableMetadata struct {
	Metadata struct {
		CurrentSchema int `json:"current-schema-id"`
		Schemas       []struct {
			ID     int `json:"schema-id"`
			Fields []struct {
				Name string `json:"name"`
				Type any    `json:"type"` // a name (long, string, decimal(10,2)) or a nested type
			} `json:"fields"`
		} `json:"schemas"`
		Current   *int64 `json:"current-snapshot-id"`
		Snapshots []struct {
			ID      int64             `json:"snapshot-id"`
			At      int64             `json:"timestamp-ms"`
			Summary map[string]string `json:"summary"`
		} `json:"snapshots"`
		Refs map[string]any `json:"refs"`
	} `json:"metadata"`
}

func (ic *Iceberg) load(ctx context.Context, namespace, table string) (*tableMetadata, error) {
	root, err := ic.root(ctx)
	if err != nil {
		return nil, err
	}
	var t tableMetadata
	if err := ic.get(ctx, root+"namespaces/"+url.PathEscape(namespace)+"/tables/"+url.PathEscape(table), &t); err != nil {
		if err == errNotFound {
			return nil, fmt.Errorf("no table %s.%s", namespace, table)
		}
		return nil, err
	}
	return &t, nil
}

// Columns are a table's columns now: its current schema's fields.
func (ic *Iceberg) Columns(ctx context.Context, namespace, table string) ([]string, error) {
	t, err := ic.load(ctx, namespace, table)
	if err != nil {
		return nil, err
	}
	for _, sc := range t.Metadata.Schemas {
		if sc.ID == t.Metadata.CurrentSchema {
			out := make([]string, 0, len(sc.Fields))
			for _, f := range sc.Fields {
				out = append(out, f.Name)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("%s.%s has no current schema", namespace, table)
}

// TypedColumns are a table's columns now, with their Trino types.
func (ic *Iceberg) TypedColumns(ctx context.Context, namespace, table string) ([]Column, error) {
	t, err := ic.load(ctx, namespace, table)
	if err != nil {
		return nil, err
	}
	for _, sc := range t.Metadata.Schemas {
		if sc.ID == t.Metadata.CurrentSchema {
			out := make([]Column, 0, len(sc.Fields))
			for _, f := range sc.Fields {
				out = append(out, Column{Name: f.Name, Type: TrinoType(f.Type)})
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("%s.%s has no current schema", namespace, table)
}

// TrinoType is an Iceberg type as Trino names it: long is bigint, string
// varchar, timestamptz timestamp(6) with time zone. A nested type (a
// struct, list or map) is named by its kind: row, array, map.
func TrinoType(t any) string {
	switch v := t.(type) {
	case string:
		switch {
		case v == "long":
			return "bigint"
		case v == "int":
			return "integer"
		case v == "string":
			return "varchar"
		case v == "float":
			return "real"
		case v == "timestamp":
			return "timestamp(6)"
		case v == "timestamptz":
			return "timestamp(6) with time zone"
		case v == "time":
			return "time(6)"
		case v == "binary":
			return "varbinary"
		case strings.HasPrefix(v, "fixed"):
			return "varbinary"
		case strings.HasPrefix(v, "decimal("):
			return strings.ReplaceAll(v, " ", "")
		}
		return v // boolean, double, date, uuid
	case map[string]any:
		switch v["type"] {
		case "struct":
			return "row"
		case "list":
			return "array"
		case "map":
			return "map"
		}
	}
	return ""
}

// Stats are a table's, now.
func (ic *Iceberg) Stats(ctx context.Context, namespace, table string) (Stats, error) {
	t, err := ic.load(ctx, namespace, table)
	if err != nil {
		return Stats{}, err
	}
	m := t.Metadata
	st := Stats{Snapshots: len(m.Snapshots), Refs: len(m.Refs)}
	num := func(s map[string]string, k string) int64 { n, _ := strconv.ParseInt(s[k], 10, 64); return n }
	for _, s := range m.Snapshots {
		at := time.UnixMilli(s.At).UTC()
		if st.Oldest.IsZero() || at.Before(st.Oldest) {
			st.Oldest = at
		}
		if m.Current != nil && s.ID == *m.Current {
			st.LastChange = at
			st.DataFiles, st.Bytes, st.Rows = num(s.Summary, "total-data-files"), num(s.Summary, "total-files-size"), num(s.Summary, "total-records")
			st.DeleteFiles = num(s.Summary, "total-delete-files")
			st.Operation = s.Summary["operation"]
			st.AddedRows, st.DeletedRows = num(s.Summary, "added-records"), num(s.Summary, "deleted-records")
			st.AddedFiles, st.DeletedFiles = num(s.Summary, "added-data-files"), num(s.Summary, "deleted-data-files")
		}
	}
	return st, nil
}

// LastChange is when a table last changed: its current snapshot's time.
// Zero, with no error, for a table with no snapshot yet.
func LastChange(ctx context.Context, c *http.Client, base, warehouse, namespace, table string) (time.Time, error) {
	st, err := (&Iceberg{HTTP: c, Base: base, Warehouse: warehouse}).Stats(ctx, namespace, table)
	return st.LastChange, err
}
