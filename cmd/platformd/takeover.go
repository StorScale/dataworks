package main

import (
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/project"
)

// handleTakeover takes one of a project's adopted tables over (its editors,
// who may write its tables): the table, which the other platform kept
// writing until now, becomes the project's own where its files are, from
// its store's newest metadata file. From then on it's written, maintained
// and erased here as the project's other tables are, and no longer followed.
//
// Refused unless its store takes takeovers (the chart's externalStores[]
// .takeover: a key that may write its buckets), and while the other
// platform still writes it: its last change there must be TakeoverQuiet
// old (default a day). Two writers would lose each other's changes, so the
// operator also says so, on the project's page, if the other platform
// writes it after all.
func (s *server) handleTakeover(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermWriteTables, "take over its adopted tables")
	if sess == nil {
		return
	}
	table := r.PathValue("table")
	p := *v.Spec
	if p.Spec.Tables == nil || p.Spec.Tables.Adoption(table) == nil {
		s.fail(w, r, http.StatusNotFound, "the project adopts no table "+table+" (tables.adopt)")
		return
	}
	tables := *p.Spec.Tables
	tables.Adopt = slices.Clone(tables.Adopt)
	p.Spec.Tables = &tables
	ad := tables.Adoption(table)
	if !ad.TakenOver.IsZero() {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%s was taken over on %s by %s", table, ad.TakenOver.UTC().Format("2006-01-02 15:04 UTC"), ad.TakenOverBy))
		return
	}
	store, _, _ := strings.Cut(ad.From, ".")
	if !slices.Contains(s.cfg.TakeoverStores, store) {
		s.fail(w, r, http.StatusUnprocessableEntity, "the store "+store+" takes no takeovers: its administrator gives it a key that may write its buckets first (the chart's externalStores[].takeover)")
		return
	}
	if s.maintainer == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform has no table maintenance account (MAINTENANCE_TRINO_PASSWORD), which reads the stores' catalogs")
		return
	}
	files, err := catalog.StoreMetadata(r.Context(), s.maintainer.trino, ad.From)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "where "+ad.From+" is now: "+err.Error())
		return
	}
	latest := files[0]
	if since := s.now().Sub(latest.At); !latest.At.IsZero() && since < s.cfg.TakeoverQuiet {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%s is still written there: its last change was %s (%s ago), and it's taken over once it's gone %s unwritten. Stop its writer first.",
			ad.From, latest.At.Format("2006-01-02 15:04 UTC"), since.Round(time.Minute), s.cfg.TakeoverQuiet))
		return
	}
	// The project's table from the store's newest file: re-registered if it
	// was a step behind (the operator follows within a minute or so).
	ns := tables.Namespace
	cur, err := s.maintainer.iceberg.MetadataLocation(r.Context(), ns, table)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project's table: "+err.Error())
		return
	}
	if cur != latest.File {
		if cur != "" && !slices.ContainsFunc(files, func(f catalog.StoreMetadataFile) bool { return f.File == cur }) {
			s.fail(w, r, http.StatusConflict, fmt.Sprintf("the project's %s isn't the store's (its metadata is %s): nothing taken over", table, cur))
			return
		}
		if cur != "" {
			if err := s.maintainer.iceberg.Unregister(r.Context(), ns, table); err != nil {
				s.fail(w, r, http.StatusBadGateway, "the project's table: "+err.Error())
				return
			}
		}
		if err := s.maintainer.iceberg.Register(r.Context(), ns, table, latest.File); err != nil {
			s.fail(w, r, http.StatusBadGateway, "the project's table, from "+path.Base(latest.File)+": "+err.Error())
			return
		}
	}
	ad.TakenOver, ad.TakenOverBy = s.now().UTC(), sess.User.Username
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	detail := fmt.Sprintf("taken over from %s at %s (its last change there %s)", ad.From, path.Base(latest.File), latest.At.Format("2006-01-02 15:04 UTC"))
	s.record(r, sess.User.Username, "table.takenover", "project:"+v.Name+"/table:"+table, detail)
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, map[string]any{"table": table, "from": ad.From, "takenOver": ad.TakenOver, "by": ad.TakenOverBy,
		"metadata": latest.File, "lastChangeThere": latest.At})
}
