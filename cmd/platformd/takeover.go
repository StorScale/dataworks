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
	// Out of the store's own catalog too, where the platform may write it: its
	// writers there no longer find the table, so nothing writes it but here.
	unregistered := false
	if slices.Contains(s.cfg.CatalogWriteStores, store) {
		if err := catalog.UnregisterInStore(r.Context(), s.maintainer.trino, ad.From); err != nil && !catalog.IsNotFound(err) {
			s.fail(w, r, http.StatusBadGateway, "taking "+ad.From+" out of the store's catalog (nothing taken over): "+err.Error())
			return
		}
		unregistered = true
	}
	ad.TakenOver, ad.TakenOverBy = s.now().UTC(), sess.User.Username
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	detail := fmt.Sprintf("taken over from %s at %s (its last change there %s)", ad.From, path.Base(latest.File), latest.At.Format("2006-01-02 15:04 UTC"))
	if unregistered {
		detail += "; taken out of the store's catalog"
	}
	s.record(r, sess.User.Username, "table.takenover", "project:"+v.Name+"/table:"+table, detail)
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, map[string]any{"table": table, "from": ad.From, "takenOver": ad.TakenOver, "by": ad.TakenOverBy,
		"metadata": latest.File, "lastChangeThere": latest.At, "outOfTheStoresCatalog": unregistered})
}

// handleGiveBack gives a table taken over back to the platform it came from
// (the project's editors): its current metadata file, everything written
// here included, is registered in the store's own catalog again (nothing
// copied), and the project adopts it as before, read-only and followed.
// Refused unless the platform may write the store's catalog, while the
// project still writes it (its last change must be TakeoverQuiet old), and
// when the store has a table by its name again.
func (s *server) handleGiveBack(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermWriteTables, "give its tables back")
	if sess == nil {
		return
	}
	table := r.PathValue("table")
	p := *v.Spec
	if p.Spec.Tables == nil || p.Spec.Tables.Adoption(table) == nil || p.Spec.Tables.Adoption(table).TakenOver.IsZero() {
		s.fail(w, r, http.StatusNotFound, "the project took no table "+table+" over")
		return
	}
	tables := *p.Spec.Tables
	tables.Adopt = slices.Clone(tables.Adopt)
	p.Spec.Tables = &tables
	ad := tables.Adoption(table)
	store, _, _ := strings.Cut(ad.From, ".")
	if !slices.Contains(s.cfg.CatalogWriteStores, store) {
		s.fail(w, r, http.StatusUnprocessableEntity, "the platform may not write the store "+store+"'s catalog (the chart's externalStores[].catalog.write), so nothing can be given back there")
		return
	}
	if s.maintainer == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform has no table maintenance account (MAINTENANCE_TRINO_PASSWORD), which writes the stores' catalogs")
		return
	}
	st, err := s.maintainer.iceberg.Stats(r.Context(), tables.Namespace, table)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project's table: "+err.Error())
		return
	}
	if since := s.now().Sub(st.LastChange); !st.LastChange.IsZero() && since < s.cfg.TakeoverQuiet {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%s is still written here: its last change was %s (%s ago), and it's given back once it's gone %s unwritten. Stop its pipelines first.",
			table, st.LastChange.Format("2006-01-02 15:04 UTC"), since.Round(time.Minute), s.cfg.TakeoverQuiet))
		return
	}
	if _, err := catalog.StoreMetadata(r.Context(), s.maintainer.trino, ad.From); err == nil {
		s.fail(w, r, http.StatusConflict, "the store has a table "+ad.From+" again (one of its own since the takeover): rename one of them first")
		return
	} else if !catalog.IsNotFound(err) {
		s.fail(w, r, http.StatusBadGateway, "the store's catalog: "+err.Error())
		return
	}
	cur, err := s.maintainer.iceberg.MetadataLocation(r.Context(), tables.Namespace, table)
	if err != nil || cur == "" {
		s.fail(w, r, http.StatusBadGateway, fmt.Sprintf("the project's table's metadata: %v", err))
		return
	}
	if err := catalog.RegisterInStore(r.Context(), s.maintainer.trino, ad.From, cur); err != nil {
		s.fail(w, r, http.StatusBadGateway, "putting "+ad.From+" back in the store's catalog: "+err.Error())
		return
	}
	takenOn := ad.TakenOver
	ad.TakenOver, ad.TakenOverBy = time.Time{}, ""
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "table.givenback", "project:"+v.Name+"/table:"+table,
		fmt.Sprintf("given back to %s at %s (taken over %s); adopted from it again", ad.From, path.Base(cur), takenOn.Format("2006-01-02")))
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, map[string]any{"table": table, "from": ad.From, "metadata": cur})
}
