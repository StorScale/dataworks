// Package catalog reads what the platform's catalog (OpenMetadata) knows of
// a project's tables: their columns, the lineage between them (and the
// pipelines that made it), and the results of their checks. It reads as the
// catalog's ingestion bot, with the token catalog-sync shares.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type Client struct {
	URL       string // the catalog's API, as platformd reaches it (http://openmetadata:8585)
	TokenFile string // the ingestion bot's token (/catalog/token)
	Service   string // the database service Trino's tables are under (trino)
	http      http.Client
}

func New(url, tokenFile, service string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), TokenFile: tokenFile, Service: service, http: http.Client{Timeout: 20 * time.Second}}
}

// Flow is a project's tables, how data flows between them, and their checks.
type Flow struct {
	Tables []Table `json:"tables"`
	Edges  []Edge  `json:"edges"`
}

type Table struct {
	Name        string   `json:"name"` // as the project knows it (orders)
	FQN         string   `json:"fqn"`  // as the catalog does (trino.iceberg.sales.orders)
	Description string   `json:"description,omitempty"`
	Columns     []Column `json:"columns"`
	Checks      []Check  `json:"checks"`
	External    bool     `json:"external,omitempty"` // in the flow, but not the project's
	Rows        *int64   `json:"rows,omitempty"`     // the profiler's count, when it has run (6.11)
	ProfiledAt  int64    `json:"profiledAt,omitempty"`
}

type Column struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"` // as the catalog shows it: bigint, varchar, decimal(10,2)
	Profile *ColumnProfile `json:"profile,omitempty"`
}

// A ColumnProfile is what the catalog's profiler found in a column.
type ColumnProfile struct {
	NullProportion *float64 `json:"nulls,omitempty"`    // 0 to 1
	Distinct       *float64 `json:"distinct,omitempty"` // distinct values
	Min            any      `json:"min,omitempty"`
	Max            any      `json:"max,omitempty"`
	Mean           *float64 `json:"mean,omitempty"`
}

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // Success, Failed, Aborted, or "" (not run yet)
	Result string `json:"result"`
	When   int64  `json:"when,omitempty"` // ms since the epoch
}

type Edge struct {
	From     string       `json:"from"` // table FQNs
	To       string       `json:"to"`
	Pipeline string       `json:"pipeline,omitempty"` // its display name
	Columns  []ColumnEdge `json:"columns"`
}

type ColumnEdge struct {
	From []string `json:"from"` // column names in From
	To   string   `json:"to"`   // a column name in To
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return fmt.Errorf("the catalog isn't set up yet (no token): %w", err)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", c.URL+"/api/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("catalog: GET %s: HTTP %d %s", path, res.StatusCode, body)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

var errNotFound = fmt.Errorf("not found")

// A TagLabel is a tag on a table or a column, as the catalog keeps it.
type TagLabel struct {
	FQN       string `json:"tagFQN"`
	Source    string `json:"source"`
	LabelType string `json:"labelType"`
	State     string `json:"state"`
}

// A TaggedColumn is a column with its tags.
type TaggedColumn struct {
	Name string     `json:"name"`
	Tags []TagLabel `json:"tags"`
}

// TableTags are a table's ID and its columns' tags.
type TableTags struct {
	ID      string         `json:"id"`
	Columns []TaggedColumn `json:"columns"`
}

// TableTags reads a table's columns and their tags.
func (c *Client) TableTags(ctx context.Context, fqn string) (*TableTags, error) {
	var t TableTags
	if err := c.get(ctx, "/tables/name/"+url.PathEscape(fqn)+"?fields=columns,tags", &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// PatchTable applies a JSON patch to a table (its tags, say), as the bot.
func (c *Client) PatchTable(ctx context.Context, id string, patch []map[string]any) error {
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return fmt.Errorf("the catalog isn't set up yet (no token): %w", err)
	}
	body, _ := json.Marshal(patch)
	req, _ := http.NewRequestWithContext(ctx, "PATCH", c.URL+"/api/v1/tables/"+url.PathEscape(id), strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json-patch+json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("catalog: PATCH table %s: HTTP %d %s", id, res.StatusCode, msg)
	}
	return nil
}

type omTable struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	FQN         string `json:"fullyQualifiedName"`
	Description string `json:"description"`
	Deleted     bool   `json:"deleted"`
	Columns     []struct {
		Name            string `json:"name"`
		DataType        string `json:"dataType"`
		DataTypeDisplay string `json:"dataTypeDisplay"`
	} `json:"columns"`
}

// Flow is the flow of a project's namespace (catalog.namespace in Trino).
func (c *Client) Flow(ctx context.Context, trinoCatalog, namespace string) (*Flow, error) {
	schema := c.Service + "." + trinoCatalog + "." + namespace
	var tables struct {
		Data []omTable `json:"data"`
	}
	err := c.get(ctx, "/tables?limit=200&fields=columns&databaseSchema="+url.QueryEscape(schema), &tables)
	if err == errNotFound {
		return &Flow{Tables: []Table{}, Edges: []Edge{}}, nil // not ingested yet
	}
	if err != nil {
		return nil, err
	}
	flow := &Flow{Tables: []Table{}, Edges: []Edge{}}
	ids := map[string]string{} // entity ID -> FQN
	inProject := map[string]bool{}
	for _, t := range tables.Data {
		if t.Deleted {
			continue
		}
		ids[t.ID], inProject[t.FQN] = t.FQN, true
		tb := Table{Name: t.Name, FQN: t.FQN, Description: t.Description, Columns: []Column{}, Checks: []Check{}}
		for _, col := range t.Columns {
			typ := strings.ToLower(col.DataTypeDisplay)
			if typ == "" {
				typ = strings.ToLower(col.DataType)
			}
			tb.Columns = append(tb.Columns, Column{Name: col.Name, Type: typ})
		}
		if tb.Checks, err = c.checks(ctx, t.FQN); err != nil {
			return nil, err
		}
		c.profile(ctx, &tb) // what the profiler found, when it has run
		flow.Tables = append(flow.Tables, tb)
	}
	seen := map[string]bool{}
	external := map[string]Table{}
	for _, t := range tables.Data {
		if t.Deleted {
			continue
		}
		var lin struct {
			Nodes []omTable `json:"nodes"`
			Up    []omEdge  `json:"upstreamEdges"`
			Down  []omEdge  `json:"downstreamEdges"`
		}
		if err := c.get(ctx, "/lineage/table/name/"+url.PathEscape(t.FQN)+"?upstreamDepth=1&downstreamDepth=1", &lin); err != nil && err != errNotFound {
			return nil, err
		}
		for _, n := range lin.Nodes {
			ids[n.ID] = n.FQN
			if !inProject[n.FQN] {
				external[n.FQN] = Table{Name: n.FQN, FQN: n.FQN, Columns: []Column{}, Checks: []Check{}, External: true}
			}
		}
		for _, e := range append(lin.Up, lin.Down...) {
			from, to := ids[e.From], ids[e.To]
			if from == "" || to == "" || seen[from+">"+to] {
				continue
			}
			seen[from+">"+to] = true
			edge := Edge{From: from, To: to, Columns: []ColumnEdge{}}
			if p := e.Details.Pipeline; p != nil {
				edge.Pipeline = p.DisplayName
				if edge.Pipeline == "" {
					edge.Pipeline = p.Name
				}
			}
			for _, cl := range e.Details.Columns {
				ce := ColumnEdge{To: lastPart(cl.To)}
				for _, f := range cl.From {
					ce.From = append(ce.From, lastPart(f))
				}
				edge.Columns = append(edge.Columns, ce)
			}
			flow.Edges = append(flow.Edges, edge)
		}
	}
	for _, t := range external {
		flow.Tables = append(flow.Tables, t)
	}
	sort.Slice(flow.Tables, func(i, j int) bool { return flow.Tables[i].FQN < flow.Tables[j].FQN })
	sort.Slice(flow.Edges, func(i, j int) bool { return flow.Edges[i].From+flow.Edges[i].To < flow.Edges[j].From+flow.Edges[j].To })
	return flow, nil
}

type omEdge struct {
	From    string `json:"fromEntity"`
	To      string `json:"toEntity"`
	Details struct {
		Pipeline *struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"pipeline"`
		Columns []struct {
			From []string `json:"fromColumns"`
			To   string   `json:"toColumn"`
		} `json:"columnsLineage"`
	} `json:"lineageDetails"`
}

func (c *Client) checks(ctx context.Context, fqn string) ([]Check, error) {
	var res struct {
		Data []struct {
			Name   string `json:"name"`
			Result *struct {
				Status    string `json:"testCaseStatus"`
				Result    string `json:"result"`
				Timestamp int64  `json:"timestamp"`
			} `json:"testCaseResult"`
		} `json:"data"`
	}
	link := "<#E::table::" + fqn + ">"
	err := c.get(ctx, "/dataQuality/testCases?limit=100&fields=testCaseResult&includeAllTests=true&entityLink="+url.QueryEscape(link), &res)
	if err == errNotFound {
		return []Check{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Check{}
	for _, d := range res.Data {
		ch := Check{Name: d.Name}
		if d.Result != nil {
			ch.Status, ch.Result, ch.When = d.Result.Status, d.Result.Result, d.Result.Timestamp
		}
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// profile adds the table's latest profile (its row count, each column's
// nulls, distinct values, min, max and mean) when the profiler has run;
// without one the table is as it was.
func (c *Client) profile(ctx context.Context, tb *Table) {
	var res struct {
		Profile *struct {
			RowCount  *int64 `json:"rowCount"`
			Timestamp int64  `json:"timestamp"`
		} `json:"profile"`
		Columns []struct {
			Name    string `json:"name"`
			Profile *struct {
				NullProportion *float64 `json:"nullProportion"`
				DistinctCount  *float64 `json:"distinctCount"`
				Min            any      `json:"min"`
				Max            any      `json:"max"`
				Mean           *float64 `json:"mean"`
			} `json:"profile"`
		} `json:"columns"`
	}
	if err := c.get(ctx, "/tables/"+url.PathEscape(tb.FQN)+"/tableProfile/latest?includeColumnProfile=true", &res); err != nil || res.Profile == nil {
		return
	}
	tb.Rows, tb.ProfiledAt = res.Profile.RowCount, res.Profile.Timestamp
	byName := map[string]*ColumnProfile{}
	for _, col := range res.Columns {
		if col.Profile != nil {
			byName[col.Name] = &ColumnProfile{NullProportion: col.Profile.NullProportion, Distinct: col.Profile.DistinctCount,
				Min: col.Profile.Min, Max: col.Profile.Max, Mean: col.Profile.Mean}
		}
	}
	for i := range tb.Columns {
		tb.Columns[i].Profile = byName[tb.Columns[i].Name]
	}
}

func lastPart(fqn string) string {
	return fqn[strings.LastIndex(fqn, ".")+1:]
}
