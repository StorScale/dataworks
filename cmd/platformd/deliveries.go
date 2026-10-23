package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/catalog"
	"github.com/StorScale/storscale-platform/internal/project"
)

// keepDeliveryApprovals keeps what only the platform sets on a project's
// deliveries (use case 9.1): who last changed what each sends, and its
// approval, as they were; who changes what one sends now, it names.
func keepDeliveryApprovals(before, p *project.Project, who string) {
	prev := map[string]project.Delivery{}
	if before != nil {
		for _, d := range before.Spec.Deliveries {
			prev[d.Name] = d
		}
	}
	for i := range p.Spec.Deliveries {
		d := &p.Spec.Deliveries[i]
		old, ok := prev[d.Name]
		d.ChangedBy, d.Approval = old.ChangedBy, old.Approval
		if !ok || old.SendsHash() != d.SendsHash() {
			d.ChangedBy = who
		}
	}
}

// needs is who has yet to approve what a delivery sends now: another than
// the one who changed it, as an editor of the project (or an administrator);
// and, when it sends personal columns, as a data-protection approver too.
func (s *server) needs(p *project.Project, d project.Delivery) []string {
	var by, protection []string
	if a := d.Approval; a != nil && a.Hash == d.SendsHash() {
		by, protection = a.By, a.Protection
	}
	other := func(u string) bool { return u != d.ChangedBy }
	var out []string
	if !slices.ContainsFunc(by, other) {
		out = append(out, "an editor of the project other than "+d.ChangedBy)
	}
	if len(p.PersonalColumns(d)) > 0 && !slices.ContainsFunc(protection, other) {
		out = append(out, "a data-protection approver ("+strings.Join(s.platformGroupsWith(context.Background(), project.PermProtectPersons), ", ")+")")
	}
	return out
}

type deliveryView struct {
	project.Delivery
	Approved bool             `json:"approved"`
	Personal []string         `json:"personal"`
	Needs    []string         `json:"needs"`
	Runs     []map[string]any `json:"runs"`
}

// handleDeliveries lists a project's deliveries (its members): each with
// whether it's approved, the personal columns it sends, who has yet to
// approve, and its last runs (the delivery log).
func (s *server) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	out := []deliveryView{}
	for _, d := range v.Spec.Spec.Deliveries {
		out = append(out, deliveryView{Delivery: d, Approved: d.Approved(), Personal: v.Spec.PersonalColumns(d),
			Needs: s.needs(v.Spec, d), Runs: s.deliveryRuns(r, v.Name, d.Name, 10)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out,
		"canSearch": s.platformCan(r.Context(), sess.User, project.PermProtectPersons)})
}

// handleApproveDelivery records the signed-in person's approval of what a
// delivery sends now; once everyone it needs has approved, it runs.
func (s *server) handleApproveDelivery(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	who, name := sess.User.Username, r.PathValue("delivery")
	p := v.Spec
	i := slices.IndexFunc(p.Spec.Deliveries, func(d project.Delivery) bool { return d.Name == name })
	if i < 0 {
		s.fail(w, r, http.StatusNotFound, "no such delivery")
		return
	}
	d := &p.Spec.Deliveries[i]
	editor := v.can(project.PermApprove)
	protection := s.platformCan(r.Context(), sess.User, project.PermProtectPersons)
	switch {
	case who == d.ChangedBy:
		s.recordRefusal(r, who, "delivery.approved", "project:"+v.Name+"/"+name, "changed it")
		s.fail(w, r, http.StatusForbidden, "someone other than who changed what it sends approves it")
		return
	case !editor && !protection:
		s.fail(w, r, http.StatusForbidden, "the project's editors (and data-protection approvers) approve deliveries")
		return
	}
	hash := d.SendsHash()
	if d.Approval == nil || d.Approval.Hash != hash {
		d.Approval = &project.Approval{Hash: hash}
	}
	if editor && !slices.Contains(d.Approval.By, who) {
		d.Approval.By = append(d.Approval.By, who)
	}
	if protection && !slices.Contains(d.Approval.Protection, who) {
		d.Approval.Protection = append(d.Approval.Protection, who)
	}
	d.Approval.At = s.now().UTC()
	need := s.needs(p, *d)
	d.Approval.Done = len(need) == 0
	if err := s.saveSpec(r.Context(), p); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	detail := fmt.Sprintf("sends %s (%s) to %s", either(d.Table, "a query"), d.Format, d.Target)
	if cols := p.PersonalColumns(*d); len(cols) > 0 {
		detail += "; personal: " + strings.Join(cols, ", ")
	}
	s.record(r, who, "delivery.approved", "project:"+v.Name+"/"+name, detail)
	writeJSON(w, http.StatusOK, map[string]any{"approved": d.Approved(), "needs": need})
}

func either(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// inGroups says whether any of the person's groups is one of these.
func (s *server) inGroups(groups, of []string) bool {
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(of, g) })
}

// handleDeliveryLog takes a delivery's run from the project's pipelines
// (the platform's DAG, after sending): its files, sizes, checksums, target,
// approval and the tables' snapshots read. platformd keeps it in its bucket
// (deliveries/<project>/<delivery>/<time>.json), with an audit record.
func (s *server) handleDeliveryLog(w http.ResponseWriter, r *http.Request) {
	name, delivery := r.PathValue("name"), r.PathValue("delivery")
	who, ok := s.projectPipelines(w, r, name)
	if !ok {
		return
	}
	var run map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&run); err != nil {
		s.fail(w, r, http.StatusBadRequest, "the run, as JSON: "+err.Error())
		return
	}
	at := s.now().UTC()
	run["recorded"] = at
	// A delivery of personal data: the catalog as it read it, kept (9.2).
	if p, _ := s.store.Project(r.Context(), name); p != nil {
		i := slices.IndexFunc(p.Spec.Deliveries, func(d project.Delivery) bool { return d.Name == delivery })
		if i >= 0 && len(p.PersonalColumns(p.Spec.Deliveries[i])) > 0 {
			readAt, err := time.Parse(time.RFC3339Nano, fmt.Sprint(run["readAt"]))
			if err != nil {
				readAt = at
			}
			run["kept"] = s.keepRead(r.Context(), p, delivery, readAt)
		}
	}
	data, _ := json.Marshal(run)
	if err := s.files.PutFile(r.Context(), fmt.Sprintf("deliveries/%s/%s/%s.json", name, delivery, at.Format("20060102T150405.000Z")),
		data, "application/json"); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, who, "delivery.sent", "project:"+name+"/"+delivery, fmt.Sprint(run["summary"]))
	writeJSON(w, http.StatusOK, map[string]any{"recorded": at})
}

// deliveryRuns are a delivery's last runs, newest first.
func (s *server) deliveryRuns(r *http.Request, project, delivery string, limit int) []map[string]any {
	out := []map[string]any{}
	if s.files == nil {
		return out
	}
	keys, err := s.files.ListFiles(r.Context(), fmt.Sprintf("deliveries/%s/%s/", project, delivery))
	if err != nil {
		return out
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	for _, k := range keys[:min(limit, len(keys))] {
		data, err := s.files.GetFile(r.Context(), k)
		if err != nil || data == nil {
			continue
		}
		var run map[string]any
		if json.Unmarshal(data, &run) == nil {
			out = append(out, run)
		}
	}
	return out
}

// handleResetImport starts a run of one of a project's imports that copies
// everything again (its editors): dlt drops the import's tables and where
// its cursors got to, then copies from the start (use case 8.2).
func (s *server) handleResetImport(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	if !v.can(project.PermConnections) {
		s.fail(w, r, http.StatusForbidden, "the project's editors reset its imports")
		return
	}
	name := r.PathValue("import")
	if !slices.ContainsFunc(v.Spec.Spec.Imports, func(i project.Import) bool { return i.Name == name }) {
		s.fail(w, r, http.StatusNotFound, "no such import")
		return
	}
	if s.airflow == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "no Airflow here")
		return
	}
	dag := "import_" + strings.ReplaceAll(v.Name, "-", "_") + "_" + name
	runID := "reset-" + s.now().UTC().Format("20060102T150405")
	if _, err := s.airflow.Trigger(r.Context(), dag, runID, map[string]any{"reset": true, "requested_by": sess.User.Username}); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, sess.User.Username, "import.reset", "project:"+v.Name+"/"+name, "its tables and where it got to dropped, all copied again")
	writeJSON(w, http.StatusOK, map[string]string{"run": runID})
}

// handleGitLabHook takes a GitLab webhook's merge request event for a
// project (use case 7.3): with the token its repo's connection holds
// (webhookToken), it asks the operator to look at the project's merge
// requests now, not at its next pass. Polling goes on, so a lost event
// only waits for it.
func (s *server) handleGitLabHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.store == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "no projects here")
		return
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil || p.Spec.Pipelines == nil || p.Spec.Pipelines.Reviews == nil || p.Spec.Pipelines.Credentials == nil {
		s.fail(w, r, http.StatusNotFound, "no such project with reviews")
		return
	}
	secret, err := s.store.Connection(r.Context(), name, p.Spec.Pipelines.Credentials.SecretRef)
	want := secret["webhookToken"]
	if err != nil || want == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Gitlab-Token")), []byte(want)) != 1 {
		s.fail(w, r, http.StatusUnauthorized, "the webhook's token isn't the project's (webhookToken)")
		return
	}
	if kind := r.Header.Get("X-Gitlab-Event"); kind != "Merge Request Hook" && kind != "Push Hook" {
		writeJSON(w, http.StatusOK, map[string]string{"ignored": kind})
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "POST", s.cfg.OperatorURL+"/projects/"+name+"/kick", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the operator: "+err.Error())
		return
	}
	res.Body.Close()
	writeJSON(w, http.StatusAccepted, map[string]string{"project": name})
}

// handleCopies are a project's sources' copies (use case 8.1), each copied
// table with when its last change was committed: how far behind its source
// a copy is, when the source is changing.
func (s *server) handleCopies(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	type table struct {
		Source     string     `json:"source"`
		From       string     `json:"from"`
		Table      string     `json:"table"`
		LastChange *time.Time `json:"lastChange,omitempty"`
		Error      string     `json:"error,omitempty"`
	}
	out := []table{}
	t := v.Spec.Spec.Tables
	for _, src := range v.Spec.Spec.Sources {
		if src.Copy == nil || t == nil {
			continue
		}
		for _, from := range src.Copy.Tables {
			tb := table{Source: src.Name, From: from, Table: project.CopyTable(src, from)}
			if pa := s.projectAlerts; pa != nil {
				at, err := catalog.LastChange(r.Context(), pa.http, pa.nessie, pa.warehous, t.Namespace, tb.Table)
				switch {
				case err != nil:
					tb.Error = err.Error()
				case !at.IsZero():
					tb.LastChange = &at
				}
			}
			out = append(out, tb)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"copies": out})
}
