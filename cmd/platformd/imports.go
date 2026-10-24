package main

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// A project's imports, from its editors (use case 8.4: a connector added
// from a form): one import added, replaced or removed, the rest of the spec
// as it is, checked as any change is. Administrators keep the whole spec.

// handlePutImport adds or replaces one of the project's imports.
func (s *server) handlePutImport(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermConnections, "change its imports")
	if sess == nil {
		return
	}
	var im project.Import
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&im); err != nil {
		s.fail(w, r, http.StatusBadRequest, "an import, as JSON: "+err.Error())
		return
	}
	im.Name = r.PathValue("import")
	if im.Connection != "" {
		cs, err := s.store.Connections(r.Context(), v.Name)
		if err != nil {
			s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
			return
		}
		if !slices.ContainsFunc(cs, func(c store.StoredConnection) bool { return c.Name == im.Connection }) {
			s.fail(w, r, http.StatusUnprocessableEntity, "no connection "+im.Connection+" among the project's (its Connections tab)")
			return
		}
	}
	p := *v.Spec
	p.Spec.Imports = slices.Clone(p.Spec.Imports)
	i := slices.IndexFunc(p.Spec.Imports, func(x project.Import) bool { return x.Name == im.Name })
	added := i < 0
	// An import runs as the project's pipelines (they read its landing area
	// and write its tables): a project without them gets them, as the shared
	// Airflow's, rather than an import that's refused when it runs.
	gavePipelines := p.Spec.Pipelines == nil
	if gavePipelines {
		p.Spec.Pipelines = &project.Pipelines{ServiceAccount: "airflow-pipelines"}
	}
	if added {
		p.Spec.Imports = append(p.Spec.Imports, im)
	} else {
		p.Spec.Imports[i] = im
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	what := map[bool]string{true: "added", false: "changed"}[added] + ": " + im.Source
	if gavePipelines {
		what += "; the project runs pipelines now (airflow-pipelines), as imports need"
	}
	if app := im.App(); app != nil {
		what += " (" + strings.Join(app.Objects, ", ") + ")"
	}
	s.record(r, sess.User.Username, "import.saved", "project:"+v.Name+"/import:"+im.Name, what)
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, im)
}

// handleDeleteImport removes one of the project's imports (its tables stay, as data does).
func (s *server) handleDeleteImport(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermConnections, "change its imports")
	if sess == nil {
		return
	}
	name := r.PathValue("import")
	p := *v.Spec
	p.Spec.Imports = slices.DeleteFunc(slices.Clone(p.Spec.Imports), func(x project.Import) bool { return x.Name == name })
	if len(p.Spec.Imports) == len(v.Spec.Spec.Imports) {
		s.fail(w, r, http.StatusNotFound, "no such import")
		return
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "import.removed", "project:"+v.Name+"/import:"+name, "its tables stay")
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

// handleConnectors are the platform's connectors, for the form: each one's
// source, what it copies, and the connection kind it signs in with.
func (s *server) handleConnectors(w http.ResponseWriter, r *http.Request) {
	if s.signedIn(w, r) == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": project.Connectors})
}
