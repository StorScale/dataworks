package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// What was sent about a person (use case 9.2). The delivery log holds no
// one's data: a delivery of personal data has the catalog as it read it
// kept (a Nessie tag at that commit, for deliveries.keepFor), and a search
// runs what each run sent again on the tables as they were then, for the
// rows of that person's ID.

// deliveryTagPrefix names the Nessie tags of deliveries' reads: capitalised,
// so no project's own tags (<project>.<name>, lower case) are ever these.
const deliveryTagPrefix = "Delivery."

func deliveryTag(project, delivery string, at time.Time) string {
	return fmt.Sprintf("%s%s.%s.%s", deliveryTagPrefix, project, delivery, at.UTC().Format("20060102T150405Z"))
}

// keepRead keeps the catalog as a personal delivery's run read it: the
// commit on main at readAt, tagged. It's what the run's record says of it.
func (s *server) keepRead(ctx context.Context, p *project.Project, delivery string, readAt time.Time) map[string]any {
	if s.history == nil {
		return map[string]any{"error": "no catalog history here"}
	}
	c, err := s.history.nessie.At(ctx, readAt, 20000)
	if err != nil || c == nil {
		return map[string]any{"error": fmt.Sprintf("the catalog's commit at %s: %v", readAt.Format(time.RFC3339), err)}
	}
	tag := deliveryTag(p.Metadata.Name, delivery, readAt)
	if _, err := s.history.nessie.Tag(ctx, tag, c.Hash); err != nil {
		return map[string]any{"commit": c.Hash, "error": "tagging it: " + err.Error()}
	}
	until, _ := keepUntil(s.cfg.DeliveryKeepFor, readAt)
	return map[string]any{"commit": c.Hash, "tag": tag, "until": until}
}

// expireDeliveryTags removes deliveries' tags past keepFor (nightly).
func (s *server) expireDeliveryTags(ctx context.Context) {
	if s.history == nil {
		return
	}
	tags, err := s.history.nessie.Tags(ctx, deliveryTagPrefix)
	if err != nil {
		s.log.Error("delivery tags", "error", err)
		return
	}
	today := s.now().UTC().Format("2006-01-02")
	for _, t := range tags {
		stamp := t.Name[strings.LastIndex(t.Name, ".")+1:]
		at, err := time.Parse("20060102T150405Z", stamp)
		if err != nil {
			continue
		}
		if until, ok := keepUntil(s.cfg.DeliveryKeepFor, at); ok && until < today {
			if err := s.history.nessie.Untag(ctx, t.Name, t.Hash); err != nil {
				s.log.Error("delivery tags: expiring", "tag", t.Name, "error", err)
			}
		}
	}
}

// A searchHit is one run of a delivery, and whether it carried the person.
type searchHit struct {
	Delivery string `json:"delivery"`
	Date     string `json:"date"`
	Sent     string `json:"sent"` // when it was recorded
	Target   string `json:"target"`
	Approval any    `json:"approval,omitempty"`
	Rows     int64  `json:"rows"`            // the person's rows it sent
	Error    string `json:"error,omitempty"` // it couldn't be told, and why
}

// asOf is a statement reading the project's tables at a commit: each of
// the tables named (bare, or with its namespace or catalog) as table#commit.
func asOf(sql, catalog, namespace, commit string, tables []string) string {
	for _, t := range tables {
		re := regexp.MustCompile(`(?i)(^|[^."\w])((?:"?` + regexp.QuoteMeta(catalog) + `"?\.)?(?:"?` + regexp.QuoteMeta(namespace) + `"?\.)?"?` + regexp.QuoteMeta(t) + `"?)\b`)
		sql = re.ReplaceAllString(sql, "${1}"+trino.Quote(catalog)+"."+trino.Quote(namespace)+"."+trino.Quote(t+"#"+commit))
	}
	return sql
}

func quoteText(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

// handleDeliverySearch: the runs of a project's deliveries that sent rows of
// a person's ID, to the data-protection approvers (and administrators).
func (s *server) handleDeliverySearch(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	who := sess.User.Username
	name := r.PathValue("name")
	if !s.isAdmin(sess.User) && !s.inGroups(sess.User.Groups, s.cfg.DeliveryApproverGroups) {
		s.recordRefusal(r, who, "deliveries.searched", "project:"+name, "not a data-protection approver")
		s.fail(w, r, http.StatusForbidden, "the data-protection approvers ("+strings.Join(s.cfg.DeliveryApproverGroups, ", ")+") search the delivery log")
		return
	}
	person := strings.TrimSpace(r.URL.Query().Get("person"))
	if person == "" || len(person) > 200 {
		s.fail(w, r, http.StatusUnprocessableEntity, "a person's ID (person=)")
		return
	}
	if s.files == nil || s.maintainer == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "searching needs the platform's files and its Trino account (maintenance)")
		return
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil || p.Spec.Tables == nil {
		s.fail(w, r, http.StatusNotFound, "no such project with tables")
		return
	}
	t := p.Spec.Tables
	keys, err := s.files.ListFiles(r.Context(), "deliveries/"+name+"/")
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	var tables []string
	if rows, err := s.maintainer.trino.Exec(r.Context(), "SHOW TABLES FROM "+trino.Quote(t.Catalog)+"."+trino.Quote(t.Namespace)); err == nil {
		for _, row := range rows {
			if len(row) > 0 {
				tables = append(tables, fmt.Sprint(row[0]))
			}
		}
	}
	// Longest first: orders_by_region before orders.
	sort.Slice(tables, func(i, j int) bool { return len(tables[i]) > len(tables[j]) })
	hits, checked, unkept := []searchHit{}, 0, 0
	for _, k := range keys {
		data, err := s.files.GetFile(r.Context(), k)
		if err != nil || data == nil {
			continue
		}
		var run struct {
			Delivery string         `json:"delivery"`
			Date     string         `json:"date"`
			Recorded string         `json:"recorded"`
			Target   string         `json:"target"`
			Approval any            `json:"approval"`
			Sends    map[string]any `json:"sends"`
			Kept     map[string]any `json:"kept"`
		}
		if json.Unmarshal(data, &run) != nil {
			continue
		}
		commit, _ := run.Kept["commit"].(string)
		if commit == "" || run.Kept["tag"] == nil {
			unkept++ // not personal, or from before reads were kept
			continue
		}
		d := project.Delivery{}
		d.Table, _ = run.Sends["table"].(string)
		d.Query, _ = run.Sends["query"].(string)
		hit := searchHit{Delivery: run.Delivery, Date: run.Date, Sent: run.Recorded, Target: run.Target, Approval: run.Approval}
		checked++
		_, column := p.PersonKeyOf(d)
		var sql string
		switch {
		case column == "":
			hit.Error = "what it sent has no column identifying a person (tables.people)"
		case d.Table != "":
			sql = fmt.Sprintf("SELECT count(*) FROM %s.%s.%s WHERE CAST(%s AS varchar) = %s", trino.Quote(t.Catalog), trino.Quote(t.Namespace),
				trino.Quote(d.Table+"#"+commit), trino.Quote(column), quoteText(person))
		default:
			sql = fmt.Sprintf("SELECT count(*) FROM (%s) AS sent WHERE CAST(sent.%s AS varchar) = %s",
				asOf(d.Query, t.Catalog, t.Namespace, commit, tables), trino.Quote(column), quoteText(person))
		}
		if sql != "" {
			rows, err := s.maintainer.trino.Exec(r.Context(), sql)
			switch {
			case err != nil:
				hit.Error = "it couldn't be run again: " + err.Error()
			case len(rows) == 1 && len(rows[0]) == 1:
				n, _ := rows[0][0].(float64)
				hit.Rows = int64(n)
			}
		}
		if hit.Rows > 0 || hit.Error != "" {
			hits = append(hits, hit)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Sent > hits[j].Sent })
	// Recorded without the ID: the audit log holds no one's data either.
	s.record(r, who, "deliveries.searched", "project:"+name, fmt.Sprintf("by a person's ID: %d runs checked, %d with their rows or unclear", checked, len(hits)))
	writeJSON(w, http.StatusOK, map[string]any{"runs": hits, "checked": checked, "notKept": unkept})
}
