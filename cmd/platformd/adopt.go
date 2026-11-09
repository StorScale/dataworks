package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/trino"
)

// What a project's editors (in the table-adoption role) may adopt (tables.adopt), and adopting it: the
// external stores (the chart's externalStores), their schemas, and a
// schema's tables with their rows, size and last change, read as the table
// maintenance account in the stores' own catalogs (ext_<store>). Names and
// figures only: never a table's rows.

type adoptableTable struct {
	Name       string     `json:"name"`
	From       string     `json:"from"` // <store>.<schema>.<table>
	Rows       int64      `json:"rows"`
	Bytes      int64      `json:"bytes"`
	LastChange *time.Time `json:"lastChange,omitempty"`
	AdoptedAs  string     `json:"adoptedAs,omitempty"` // this project's name for it, when it adopts it
	Note       string     `json:"note,omitempty"`      // why it can't be adopted (a view, its metadata unreadable)
}

// handleAdoptable answers, for one of the project's editors in the table-adoption role: the stores
// (no store=), a store's schemas (store=), or a schema's tables (store= and schema=).
func (s *server) handleAdoptable(w http.ResponseWriter, r *http.Request) {
	sess, v := s.adopter(w, r, "table.adoptable", "see what may be adopted")
	if sess == nil {
		return
	}
	store, schema := r.URL.Query().Get("store"), r.URL.Query().Get("schema")
	if store == "" {
		out := []map[string]any{}
		for _, st := range s.cfg.ExternalStores {
			out = append(out, map[string]any{"name": st, "takeover": slices.Contains(s.cfg.TakeoverStores, st)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"stores": out})
		return
	}
	if !slices.Contains(s.cfg.ExternalStores, store) {
		s.fail(w, r, http.StatusNotFound, "no external store "+store+" (the chart's externalStores)")
		return
	}
	if s.maintainer == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform has no table maintenance account (MAINTENANCE_TRINO_PASSWORD), which reads the stores' catalogs")
		return
	}
	ext := trino.Quote(catalog.ExternalCatalog(store))
	if schema == "" {
		rows, err := s.maintainer.trino.Exec(r.Context(), "SHOW SCHEMAS FROM "+ext)
		if err != nil {
			s.fail(w, r, http.StatusBadGateway, "the store's schemas: "+err.Error())
			return
		}
		out := []string{}
		for _, row := range rows {
			if n, _ := row[0].(string); n != "" && n != "information_schema" {
				out = append(out, n)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"store": store, "schemas": out})
		return
	}
	rows, err := s.maintainer.trino.Exec(r.Context(), "SHOW TABLES FROM "+ext+"."+trino.Quote(schema))
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the schema's tables: "+err.Error())
		return
	}
	adopted := map[string]string{} // from -> name here
	if t := v.Spec.Spec.Tables; t != nil {
		for _, a := range t.Adopt {
			adopted[a.From] = a.Table
		}
	}
	tables := make([]adoptableTable, 0, len(rows))
	for _, row := range rows {
		if n, _ := row[0].(string); n != "" {
			from := store + "." + schema + "." + n
			tables = append(tables, adoptableTable{Name: n, From: from, AdoptedAs: adopted[from]})
		}
	}
	// Each table's newest snapshot: its rows, size and when; a few at a time.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range tables {
		wg.Add(1)
		go func(t *adoptableTable) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := s.maintainer.trino.Exec(r.Context(), fmt.Sprintf(
				`SELECT CAST(to_unixtime(committed_at) * 1000 AS bigint), summary['total-records'], summary['total-files-size'] FROM %s.%s.%s ORDER BY committed_at DESC LIMIT 1`,
				ext, trino.Quote(schema), trino.Quote(t.Name+"$snapshots")))
			switch {
			case err != nil && strings.Contains(err.Error(), "does not exist"):
				t.Note = "a view, or not an Iceberg table: nothing to adopt"
			case err != nil:
				t.Note = "its metadata can't be read: " + shortError(err.Error())
			case len(res) == 0:
				t.Note = "no snapshot yet: empty"
			default:
				if ms, ok := res[0][0].(float64); ok {
					at := time.UnixMilli(int64(ms)).UTC()
					t.LastChange = &at
				}
				t.Rows, _ = strconv.ParseInt(fmt.Sprint(res[0][1]), 10, 64)
				t.Bytes, _ = strconv.ParseInt(fmt.Sprint(res[0][2]), 10, 64)
			}
		}(&tables[i])
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"store": store, "schema": schema, "tables": tables,
		"takeover": slices.Contains(s.cfg.TakeoverStores, store), "quiet": s.cfg.TakeoverQuiet.String()})
}

// shortError is an error's first line, at most 160 characters.
func shortError(s string) string {
	s = firstLine(s)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// handleAdopt adds tables to the project's tables.adopt (its editors in the table-adoption role): each
// {table, from}, the operator registering it within a minute or so.
func (s *server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	sess, v := s.adopter(w, r, "table.adopt", "adopt tables")
	if sess == nil {
		return
	}
	var req struct {
		Tables []project.Adoption `json:"tables"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || len(req.Tables) == 0 {
		s.fail(w, r, http.StatusBadRequest, `the tables to adopt, as {"tables": [{"table": "orders", "from": "<store>.<schema>.<table>"}]}`)
		return
	}
	p := *v.Spec
	tables := project.Tables{}
	if p.Spec.Tables != nil {
		tables = *p.Spec.Tables
	}
	tables.Adopt = slices.Clone(tables.Adopt)
	var added []string
	for _, a := range req.Tables {
		store, _, _ := strings.Cut(a.From, ".")
		if !slices.Contains(s.cfg.ExternalStores, store) {
			s.fail(w, r, http.StatusUnprocessableEntity, a.From+": no external store "+store)
			return
		}
		if slices.ContainsFunc(tables.Adopt, func(x project.Adoption) bool { return x.Table == a.Table || x.From == a.From }) {
			s.fail(w, r, http.StatusConflict, fmt.Sprintf("the project already adopts %s, or a table by the name %s", a.From, a.Table))
			return
		}
		tables.Adopt = append(tables.Adopt, project.Adoption{Table: a.Table, From: a.From})
		added = append(added, a.Table+" from "+a.From)
	}
	p.Spec.Tables = &tables
	project.KeepAdoptionState(v.Spec, &p)
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "table.adopted", "project:"+v.Name, strings.Join(added, "; "))
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, map[string]any{"adopt": tables.Adopt})
}

// handleUnadopt takes an adopted table out of the project (its editors in the table-adoption role):
// unregistered within a minute, its files left to its store. One taken over
// is the project's own: it stays (drop it as any other table).
func (s *server) handleUnadopt(w http.ResponseWriter, r *http.Request) {
	sess, v := s.adopter(w, r, "table.unadopt", "stop adopting tables")
	if sess == nil {
		return
	}
	table := r.PathValue("table")
	t := v.Spec.Spec.Tables
	a := t.Adoption(table)
	switch {
	case a == nil:
		s.fail(w, r, http.StatusNotFound, "the project adopts no table "+table)
		return
	case !a.TakenOver.IsZero():
		s.fail(w, r, http.StatusConflict, table+" was taken over: it's the project's own now, dropped as its other tables are")
		return
	}
	p := *v.Spec
	tables := *t
	tables.Adopt = slices.DeleteFunc(slices.Clone(tables.Adopt), func(x project.Adoption) bool { return x.Table == table })
	p.Spec.Tables = &tables
	project.KeepAdoptionState(v.Spec, &p)
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "table.unadopted", "project:"+v.Name+"/table:"+table, "from "+a.From)
	s.kick(r.Context(), v.Name)
	w.WriteHeader(http.StatusNoContent)
}
