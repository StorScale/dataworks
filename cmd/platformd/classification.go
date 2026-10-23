package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/StorScale/storscale-platform/internal/project"
)

// Classification (use case 12.5): where a project's personal data is, as the
// catalog has it (found by catalog-sync, or declared by the project) and
// catalog-sync wrote it to the platform's bucket; shown on the Data page,
// and confirmed or removed by the project's editors, in the catalog itself
// (as the platform's account), so the catalog and Ranger follow.

const protectionTag = "Protection."

// classificationOf is what catalog-sync wrote for a project, or an empty one.
func (s *server) classificationOf(ctx context.Context, name string) (*project.Classification, error) {
	c := &project.Classification{Project: name, Columns: []project.ProtectedColumn{}}
	if s.files == nil {
		return c, nil
	}
	b, err := s.files.GetFile(ctx, "classification/"+name+".json")
	if err != nil || b == nil {
		return c, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.Columns == nil {
		c.Columns = []project.ProtectedColumn{}
	}
	return c, nil
}

// handleClassification is a project's protected columns, to its members.
func (s *server) handleClassification(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	c, err := s.classificationOf(r.Context(), p.Metadata.Name)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": c.Columns, "at": c.At, "tags": project.DefaultProtectionTags()})
}

// handleClassify confirms or removes a column's tag, in the catalog: an
// editor's word on what the catalog found. Confirmed, the tag stays whatever
// the catalog finds later; removed, it isn't suggested again.
func (s *server) handleClassify(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	if !s.memberCan(r, p.Metadata.Name, who, project.PermClassify) {
		s.recordRefusal(r, who, "classification.changed", "project:"+p.Metadata.Name, "not permitted (classify)")
		s.fail(w, r, http.StatusForbidden, "the project's editors confirm or remove a column's tags")
		return
	}
	var body struct {
		Table  string `json:"table"`
		Column string `json:"column"`
		Tag    string `json:"tag"`
		Action string `json:"action"` // confirm, remove
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a table, column, tag and action (confirm or remove), as JSON")
		return
	}
	switch {
	case !project.IdentifierOK(body.Table) || !project.IdentifierOK(body.Column) || !project.IdentifierOK(body.Tag):
		s.fail(w, r, http.StatusUnprocessableEntity, "a table, a column and a tag, each an identifier")
		return
	case body.Action != "confirm" && body.Action != "remove":
		s.fail(w, r, http.StatusUnprocessableEntity, "the action is confirm or remove")
		return
	case s.catalog == nil || p.Spec.Tables == nil:
		s.fail(w, r, http.StatusServiceUnavailable, "the catalog isn't set up, or the project has no tables")
		return
	}
	t := p.Spec.Tables
	fqn := s.catalog.Service + "." + t.Catalog + "." + t.Namespace + "." + body.Table
	tt, err := s.catalog.TableTags(r.Context(), fqn)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the catalog: "+err.Error())
		return
	}
	col := -1
	for i, c := range tt.Columns {
		if c.Name == body.Column {
			col = i
		}
	}
	if col < 0 {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("the catalog has no column %s.%s", body.Table, body.Column))
		return
	}
	want := protectionTag + body.Tag
	at := -1
	for i, tg := range tt.Columns[col].Tags {
		if tg.FQN == want {
			at = i
		}
	}
	var patch []map[string]any
	switch {
	case body.Action == "remove" && at < 0:
		writeJSON(w, http.StatusOK, map[string]string{"state": "absent"})
		return
	case body.Action == "remove":
		patch = []map[string]any{{"op": "remove", "path": fmt.Sprintf("/columns/%d/tags/%d", col, at)}}
	case at >= 0:
		patch = []map[string]any{
			{"op": "replace", "path": fmt.Sprintf("/columns/%d/tags/%d/state", col, at), "value": "Confirmed"},
			{"op": "replace", "path": fmt.Sprintf("/columns/%d/tags/%d/labelType", col, at), "value": "Manual"},
		}
	default: // a tag the catalog hadn't: added, confirmed
		label := map[string]any{"tagFQN": want, "source": "Classification", "labelType": "Manual", "state": "Confirmed"}
		if tt.Columns[col].Tags == nil {
			patch = []map[string]any{{"op": "add", "path": fmt.Sprintf("/columns/%d/tags", col), "value": []any{label}}}
		} else {
			patch = []map[string]any{{"op": "add", "path": fmt.Sprintf("/columns/%d/tags/%d", col, len(tt.Columns[col].Tags)), "value": label}}
		}
	}
	if err := s.catalog.PatchTable(r.Context(), tt.ID, patch); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	// The file follows at catalog-sync's next round; the project's view now.
	// Only its columns change: the rest (what catalog-sync suggested before,
	// what was dismissed) is catalog-sync's memory, kept as it is.
	if s.files != nil {
		if raw, err := s.files.GetFile(r.Context(), "classification/"+p.Metadata.Name+".json"); err == nil && raw != nil {
			var doc map[string]json.RawMessage
			var cols []project.ProtectedColumn
			if json.Unmarshal(raw, &doc) == nil {
				json.Unmarshal(doc["columns"], &cols)
				kept := []project.ProtectedColumn{}
				for _, pc := range cols {
					if pc.Table == body.Table && pc.Column == body.Column && pc.Tag == body.Tag {
						continue
					}
					kept = append(kept, pc)
				}
				if body.Action == "confirm" {
					kept = append(kept, project.ProtectedColumn{Table: body.Table, Column: body.Column, Tag: body.Tag, State: "confirmed"})
				}
				doc["columns"], _ = json.Marshal(kept)
				if b, err := json.Marshal(doc); err == nil {
					s.files.PutFile(r.Context(), "classification/"+p.Metadata.Name+".json", b, "application/json")
				}
			}
		}
	}
	s.record(r, who, "classification."+strings.TrimSuffix(body.Action, "e")+"ed", "project:"+p.Metadata.Name, body.Table+"."+body.Column+" "+body.Tag)
	writeJSON(w, http.StatusOK, map[string]string{"state": map[string]string{"confirm": "confirmed", "remove": "removed"}[body.Action]})
}
