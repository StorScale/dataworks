package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/trino"
)

// Erasure (use case 6.9, under the CCPA): a person's rows deleted from the
// tables a project declares as holding people's data (tables.people, by the
// column that identifies a person), as the platform's maintenance account,
// which reads and deletes unmasked. Asked by a data-protection approver (or
// an administrator) who has verified the request; recorded without the
// person's ID (a hash of it, so the same request is recognised), with what
// was deleted where, the deliveries that carried the person's data (9.2's
// search, so recipients can be told), and when the old versions of the
// tables (the history kept, the closes tagged) stop holding the rows. The
// records are kept at least two years; the deadline is the install's.

// An erasure is the record of one.
type erasure struct {
	ID          string            `json:"id"`
	Project     string            `json:"project"`
	PersonHash  string            `json:"personHash"` // sha256 of the ID, its first 16 hex digits: never the ID
	RequestedOn string            `json:"requestedOn"`
	DueBy       string            `json:"dueBy"`
	By          string            `json:"by"`
	VerifiedBy  string            `json:"verifiedBy"`
	At          time.Time         `json:"at"`
	Tables      []erasedTable     `json:"tables"`
	Files       []erasedFile      `json:"files"` // the landing files its imports read, written again without the person
	Deliveries  []searchHit       `json:"deliveries"`
	Checked     int               `json:"deliveriesChecked"`
	NotKept     int               `json:"deliveriesNotCheckable"`
	HistoryKept string            `json:"historyKept,omitempty"` // how long the tables' old versions still hold the rows
	AgesOutBy   string            `json:"agesOutBy,omitempty"`
	Closes      []string          `json:"closes,omitempty"` // tags (closes) that keep versions with the rows, however old
	Errors      map[string]string `json:"errors,omitempty"` // table -> why it couldn't be done
}

type erasedTable struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Rows   int64  `json:"rows"`
}

func personHash(id string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return hex.EncodeToString(sum[:])[:16]
}

// erasureAllowed says whether the session may ask for, or read, erasures.
func (s *server) erasureAllowed(ctx context.Context, u user) bool {
	return s.platformCan(ctx, u, project.PermProtectPersons)
}

// handleErasures lists a project's erasure records, to the approvers.
func (s *server) handleErasures(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	name := r.PathValue("name")
	if !s.erasureAllowed(r.Context(), sess.User) {
		s.fail(w, r, http.StatusForbidden, "the data-protection approvers (a platform role with protect-personal) see erasures")
		return
	}
	out := []erasure{}
	if s.files != nil {
		keys, _ := s.files.ListFiles(r.Context(), "erasures/"+name+"/")
		for _, k := range keys {
			if b, err := s.files.GetFile(r.Context(), k); err == nil && b != nil {
				var e erasure
				if json.Unmarshal(b, &e) == nil {
					out = append(out, e)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	writeJSON(w, http.StatusOK, map[string]any{"erasures": out, "deadlineDays": s.cfg.ErasureDeadlineDays,
		"canErase": s.erasureAllowed(r.Context(), sess.User)})
}

// handleErase deletes a person's rows from the project's tables of people's
// data, and records it.
func (s *server) handleErase(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	who, name := sess.User.Username, r.PathValue("name")
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	if !s.erasureAllowed(r.Context(), sess.User) {
		s.recordRefusal(r, who, "erasure", "project:"+name, "not a data-protection approver")
		s.fail(w, r, http.StatusForbidden, "the data-protection approvers (a platform role with protect-personal) ask for erasures")
		return
	}
	var body struct {
		Person      string `json:"person"`
		RequestedOn string `json:"requestedOn"` // 2026-10-19: when the person asked
		VerifiedBy  string `json:"verifiedBy"`  // who verified the request (the privacy tool, a person); default the caller
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a person's ID (person), when they asked (requestedOn) and who verified it (verifiedBy), as JSON")
		return
	}
	body.Person = strings.TrimSpace(body.Person)
	if body.Person == "" || len(body.Person) > 200 {
		s.fail(w, r, http.StatusUnprocessableEntity, "a person's ID (person)")
		return
	}
	requested := s.now().UTC()
	if body.RequestedOn != "" {
		d, err := time.Parse("2006-01-02", body.RequestedOn)
		if err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, "requestedOn as a date, 2026-10-19")
			return
		}
		requested = d
	}
	if body.VerifiedBy == "" {
		body.VerifiedBy = who
	}
	if s.files == nil || s.maintainer == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "erasing needs the platform's files and its Trino account (maintenance)")
		return
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil || p.Spec.Tables == nil {
		s.fail(w, r, http.StatusNotFound, "no such project with tables")
		return
	}
	t := p.Spec.Tables
	if len(t.People) == 0 {
		s.fail(w, r, http.StatusUnprocessableEntity, "the project declares no tables of people's data (tables.people), so there's nothing to erase from")
		return
	}
	ctx := r.Context()
	e := erasure{ID: s.now().UTC().Format("20060102T150405") + "-" + personHash(body.Person)[:6], Project: name, PersonHash: personHash(body.Person),
		RequestedOn: requested.Format("2006-01-02"), DueBy: requested.AddDate(0, 0, s.cfg.ErasureDeadlineDays).Format("2006-01-02"),
		By: who, VerifiedBy: body.VerifiedBy, At: s.now().UTC(), Tables: []erasedTable{}, Files: []erasedFile{}, Deliveries: []searchHit{}, Errors: map[string]string{}}
	// What was sent about them, before their rows go (the search reads the
	// tables as they were at each delivery: unchanged by the deletion).
	if hits, checked, unkept, err := s.searchPerson(ctx, p, body.Person); err == nil {
		e.Deliveries, e.Checked, e.NotKept = hits, checked, unkept
	} else {
		e.Errors["deliveries"] = err.Error()
	}
	for _, pk := range t.People {
		table := strings.Join([]string{trino.Quote(t.Catalog), trino.Quote(t.Namespace), trino.Quote(pk.Table)}, ".")
		where := fmt.Sprintf(" WHERE CAST(%s AS varchar) = %s", trino.Quote(pk.Column), quoteText(body.Person))
		rows, err := s.maintainer.trino.Exec(ctx, "SELECT count(*) FROM "+table+where)
		if err != nil {
			e.Errors[pk.Table] = err.Error()
			continue
		}
		n := int64(0)
		if len(rows) == 1 && len(rows[0]) == 1 {
			f, _ := rows[0][0].(float64)
			n = int64(f)
		}
		if n > 0 {
			if _, err := s.maintainer.trino.Exec(ctx, "DELETE FROM "+table+where); err != nil {
				e.Errors[pk.Table] = err.Error()
				continue
			}
		}
		e.Tables = append(e.Tables, erasedTable{Table: pk.Table, Column: pk.Column, Rows: n})
	}
	// The files the imports read them from, so the next import doesn't bring them back: as who erases.
	if len(landingFiles(p)) > 0 {
		token, err := s.sessionToken(ctx, sess, true)
		var c *minio.Client
		if err == nil {
			c, err = s.bucketAs(ctx, token)
		}
		if err != nil {
			e.Errors["landing files"] = err.Error()
		} else {
			e.Files = eraseFromLanding(ctx, c, p, body.Person, e.Errors)
		}
	}
	// Where the rows still are: the tables' old versions, for as long as the
	// history is kept, and in any close tagged while they were there.
	if s.history != nil && s.history.keep > 0 {
		e.HistoryKept = s.history.keep.String()
		e.AgesOutBy = e.At.Add(s.history.keep).Format("2006-01-02")
	}
	for _, tg := range t.Tags {
		e.Closes = append(e.Closes, tg.Name)
	}
	if len(e.Errors) == 0 {
		e.Errors = nil
	}
	b, _ := json.Marshal(e)
	if err := s.files.PutFile(ctx, "erasures/"+name+"/"+e.ID+".json", b, "application/json"); err != nil {
		s.fail(w, r, http.StatusBadGateway, "the erasure ran but its record couldn't be kept: "+err.Error())
		return
	}
	rows := int64(0)
	for _, et := range e.Tables {
		rows += et.Rows
	}
	// Recorded without the ID.
	fileRows := int64(0)
	for _, f := range e.Files {
		fileRows += f.Rows
	}
	s.record(r, who, "erasure", "project:"+name, fmt.Sprintf("%s: %d rows from %d tables and %d from %d landing files, %d deliveries had carried the person's data; verified by %s; due %s",
		e.ID, rows, len(e.Tables), fileRows, len(e.Files), len(e.Deliveries), e.VerifiedBy, e.DueBy))
	code := http.StatusOK
	if e.Errors != nil {
		code = http.StatusMultiStatus
	}
	writeJSON(w, code, e)
}

// String is the erasure in words.
func (e erasure) String() string {
	parts := []string{}
	for _, t := range e.Tables {
		parts = append(parts, fmt.Sprintf("%s (%d)", t.Table, t.Rows))
	}
	return strings.Join(parts, ", ")
}
