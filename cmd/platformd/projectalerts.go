package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/storscale-platform/internal/catalog"
	"github.com/StorScale/storscale-platform/internal/health"
	"github.com/StorScale/storscale-platform/internal/project"
)

// An issue is something wrong in a project that its people should hear of:
// a pipeline's latest run failed, a check is failing, a table is stale.
type issue struct {
	Key        string     `json:"key"`  // pipeline/<id>/<run>, check/<table>/<check>, stale/<table>
	Kind       string     `json:"kind"` // pipeline, check, stale, maintenance, copy, storage
	Title      string     `json:"title"`
	Detail     string     `json:"detail,omitempty"`
	Link       string     `json:"link"`
	MutedUntil *time.Time `json:"mutedUntil,omitempty"`
}

// projectAlerts tells each project's people, where its notify says, of its
// issues as they start, and of a check or a table as it comes right.
type projectAlerts struct {
	mu       sync.Mutex
	issues   map[string]map[string]issue // project -> key -> what's wrong now
	told     map[string]string           // project -> its notify, as last told (a hash)
	muted    map[string]time.Time        // project/key -> until (kept in the platform's bucket: mutesKey)
	unknown  map[string][]string         // project -> what couldn't be looked at, and why
	checked  bool                        // a first pass done: what was wrong already isn't news
	down     map[string]time.Time        // project/copy/<source> -> since when its copy isn't running
	http     *http.Client
	nessie   string // Nessie's Iceberg API (http://nessie:19120/iceberg)
	warehous string
}

func (s *server) startProjectAlerts(ctx context.Context) {
	pa := &projectAlerts{issues: map[string]map[string]issue{}, told: map[string]string{}, muted: map[string]time.Time{}, unknown: map[string][]string{},
		down: map[string]time.Time{},
		http: &http.Client{Timeout: 20 * time.Second}, warehous: "warehouse"}
	pa.nessie = strings.TrimRight(os.Getenv("NESSIE_URL"), "/")
	if pa.nessie == "" {
		pa.nessie = "http://nessie:19120"
	}
	pa.nessie += "/iceberg"
	if w := os.Getenv("NESSIE_WAREHOUSE"); w != "" {
		pa.warehous = w
	}
	s.projectAlerts = pa
	go func() {
		for {
			pa.pass(ctx, s)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
		}
	}()
}

// issuesOf are a project's issues just now, and what couldn't be looked at.
func (s *server) issuesOf(ctx context.Context, p *project.Project) (out []issue, unknown []string) {
	link := s.cfg.PlatformURL + "/p/" + p.Metadata.Name
	if s.airflow != nil && p.Spec.Pipelines != nil {
		if af, _, err := s.pipelinesOf(ctx, &projectView{Name: p.Metadata.Name, Spec: p}); err == nil {
			ps, err := af.Of(ctx, p.Metadata.Name, 1)
			if err == nil && af != s.airflow { // its imports, in the platform's Airflow
				if shared, err := s.airflow.Of(ctx, p.Metadata.Name, 1); err == nil {
					ps = append(ps, shared...)
				}
			}
			if err == nil {
				for _, pl := range ps {
					if len(pl.Runs) > 0 && pl.Runs[0].State == "failed" {
						out = append(out, issue{Key: "pipeline/" + pl.ID + "/" + pl.Runs[0].ID, Kind: "pipeline",
							Title: fmt.Sprintf("%s's latest run failed", pl.ID), Detail: "run " + pl.Runs[0].ID,
							Link: link + "/pipelines?pipeline=" + pl.ID})
					}
				}
			}
		}
	}
	if t := p.Spec.Tables; t != nil {
		if s.catalog != nil {
			if flow, err := s.catalog.Flow(ctx, t.Catalog, t.Namespace); err == nil {
				for _, tb := range flow.Tables {
					if tb.External {
						continue
					}
					for _, c := range tb.Checks {
						if c.Status == "Failed" {
							out = append(out, issue{Key: "check/" + tb.Name + "/" + c.Name, Kind: "check",
								Title: fmt.Sprintf("%s: the check %s is failing", tb.Name, c.Name), Detail: c.Result,
								Link: link + "/data?table=" + tb.Name + "&tab=quality"})
						}
					}
				}
			}
		}
		out = append(out, s.llmIssues(ctx, p, link)...)
		out = append(out, s.usageIssues(ctx, p, link)...)
		out = append(out, s.scoringIssues(ctx, p, link)...)
		if c, err := s.classificationOf(ctx, p.Metadata.Name); err == nil {
			for _, pc := range c.Columns {
				if pc.State == "suggested" {
					out = append(out, issue{Key: "protect/" + pc.Table + "/" + pc.Column, Kind: "protect",
						Title:  fmt.Sprintf("%s.%s looks %s: the catalog tagged it, and it's masked", pc.Table, pc.Column, pc.Tag),
						Detail: "Confirm the tag, or remove it if the column isn't " + pc.Tag + ", on the Data page.",
						Link:   link + "/data?table=" + pc.Table})
				}
			}
		}
		for _, f := range t.Freshness {
			within, ok := project.Within(f.Within)
			if !ok || s.projectAlerts == nil {
				continue
			}
			at, err := catalog.LastChange(ctx, s.projectAlerts.http, s.projectAlerts.nessie, s.projectAlerts.warehous, t.Namespace, f.Table)
			if err != nil {
				unknown = append(unknown, fmt.Sprintf("when %s last changed: %v", f.Table, err))
				continue
			}
			if at.IsZero() || time.Since(at) > within {
				detail := "never changed"
				if !at.IsZero() {
					detail = "last changed " + at.Format("2006-01-02 15:04 MST")
				}
				out = append(out, issue{Key: "stale/" + f.Table, Kind: "stale",
					Title: fmt.Sprintf("%s is stale: not changed within %s", f.Table, f.Within), Detail: detail,
					Link: link + "/data?table=" + f.Table})
			}
		}
	}
	storage, why := s.storageIssues(ctx, p, link)
	out, unknown = append(out, storage...), append(unknown, why...)
	out = append(out, s.copyIssues(ctx, p, link)...)
	out = append(out, s.productIssues(ctx, p, link, out, &unknown)...)
	if s.maintainer != nil {
		for _, run := range s.maintainer.failures(p.Metadata.Name) {
			out = append(out, issue{Key: "maintenance/" + run.Table, Kind: "maintenance",
				Title: fmt.Sprintf("%s: its maintenance failed", run.Table), Detail: run.Error,
				Link: link + "/data?table=" + run.Table})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, unknown
}

func notifyHash(n *project.Notify) string {
	if n == nil {
		return ""
	}
	b, _ := json.Marshal(n)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// pass looks at every project once, and tells what's new.
// mutesKey is where the mutes are kept, so a restart (or another of
// platformd's pods) keeps them.
const mutesKey = "alerts/mutes.json"

// loadMutes reads the kept mutes, those still running; nothing kept, or no bucket, leaves them as they are.
func (pa *projectAlerts) loadMutes(ctx context.Context, s *server) {
	if s.files == nil {
		return
	}
	b, err := s.files.GetFile(ctx, mutesKey)
	if err != nil || b == nil {
		return
	}
	kept := map[string]time.Time{}
	if json.Unmarshal(b, &kept) != nil {
		return
	}
	pa.mu.Lock()
	defer pa.mu.Unlock()
	pa.muted = map[string]time.Time{}
	for k, until := range kept {
		if time.Now().Before(until) {
			pa.muted[k] = until
		}
	}
}

// saveMutes keeps the running mutes; the caller holds pa.mu.
func (pa *projectAlerts) saveMutes(ctx context.Context, s *server) error {
	if s.files == nil {
		return nil
	}
	for k, until := range pa.muted {
		if !time.Now().Before(until) {
			delete(pa.muted, k)
		}
	}
	b, _ := json.Marshal(pa.muted)
	return s.files.PutFile(ctx, mutesKey, b, "application/json")
}

func (pa *projectAlerts) pass(ctx context.Context, s *server) {
	if s.store == nil {
		return
	}
	pa.loadMutes(ctx, s)
	projects, err := s.store.Projects(ctx)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, p := range projects {
		live[p.Metadata.Name] = true
		issues, unknown := s.issuesOf(ctx, p)
		pa.mu.Lock()
		if strings.Join(unknown, "\n") != strings.Join(pa.unknown[p.Metadata.Name], "\n") {
			for _, u := range unknown {
				s.log.Warn("project alerts: can't tell", "project", p.Metadata.Name, "what", u)
			}
		}
		pa.unknown[p.Metadata.Name] = unknown
		pa.mu.Unlock()
		send := pa.update(p.Metadata.Name, notifyHash(p.Spec.Notify), issues)
		if len(send) > 0 {
			n := &health.Notifier{Targets: func(ctx context.Context) (health.Targets, error) { return s.projectTargets(ctx, p) }}
			for _, a := range send {
				for target, err := range n.Send(ctx, a) {
					if err != nil {
						s.log.Error("project alert not sent", "project", p.Metadata.Name, "target", target, "alert", a.Title, "error", err)
					}
				}
			}
		}
	}
	pa.mu.Lock()
	for name := range pa.issues {
		if !live[name] {
			delete(pa.issues, name)
			delete(pa.told, name)
			delete(pa.unknown, name)
		}
	}
	pa.checked = true
	pa.mu.Unlock()
}

// update records a project's issues now, and is what to tell of them: the
// new ones (all of them, to a channel just named), and the checks and stale
// tables come right; none on platformd's first look, nor while muted.
func (pa *projectAlerts) update(name, hash string, now []issue) []health.Alert {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	before := pa.issues[name]
	cur := map[string]issue{}
	for _, is := range now {
		cur[is.Key] = is
	}
	pa.issues[name] = cur
	connected := hash != "" && pa.told[name] != hash
	pa.told[name] = hash
	var send []health.Alert
	if hash == "" || !pa.checked {
		return nil
	}
	for _, is := range now {
		if _, was := before[is.Key]; (connected || !was) && !pa.isMuted(name, is.Key) {
			send = append(send, alertOf(name, is, false))
		}
	}
	var gone []string
	for k, is := range before {
		if _, still := cur[k]; !still && is.Kind != "pipeline" && !pa.isMuted(name, k) {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	for _, k := range gone {
		send = append(send, alertOf(name, before[k], true))
	}
	return send
}

func (pa *projectAlerts) isMuted(project, key string) bool {
	return time.Now().Before(pa.muted[project+"/"+key])
}

func alertOf(project string, is issue, resolved bool) health.Alert {
	a := health.Alert{Title: project + ": " + is.Title, Severity: "down", Link: is.Link, LinkText: "Open it in the platform", At: time.Now().UTC(),
		Facts: [][2]string{{"Project", project}}}
	if is.Kind == "stale" || is.Kind == "maintenance" || (is.Kind == "storage" && strings.HasSuffix(is.Key, "/near")) {
		a.Severity = "warning"
	}
	if resolved {
		a.Title, a.Severity = project+": resolved: "+is.Title, "up"
	}
	if is.Detail != "" {
		a.Facts = append(a.Facts, [2]string{"Detail", is.Detail})
	}
	return a
}

// projectTargets are where a project's alerts go: its own Teams channel (a
// connection of its own) and its email, through the install's mail server.
func (s *server) projectTargets(ctx context.Context, p *project.Project) (health.Targets, error) {
	n := p.Spec.Notify
	var t health.Targets
	if n == nil {
		return t, nil
	}
	if n.Teams != "" {
		sec, err := s.store.Connection(ctx, p.Metadata.Name, n.Teams)
		if err != nil {
			return t, err
		}
		t.TeamsWebhook = strings.TrimSpace(sec["webhook"])
	}
	if n.Slack != "" {
		sec, err := s.store.Connection(ctx, p.Metadata.Name, n.Slack)
		if err != nil {
			return t, err
		}
		t.SlackWebhook = strings.TrimSpace(sec["webhook"])
	}
	if len(n.Email) > 0 {
		install, err := s.store.Secret(ctx, alertsSecret)
		if err == nil {
			mail := health.TargetsFrom(install)
			t.SMTPServer, t.SMTPFrom, t.SMTPUser, t.SMTPPassword = mail.SMTPServer, mail.SMTPFrom, mail.SMTPUser, mail.SMTPPassword
			t.EmailTo = n.Email
		}
	}
	return t, nil
}

// handleProjectAlerts are a project's issues now, for its people; editors
// (and administrators) mute one for a while.
func (s *server) handleProjectAlerts(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	if s.projectAlerts == nil {
		writeJSON(w, http.StatusOK, map[string]any{"checked": false, "issues": []issue{}})
		return
	}
	pa := s.projectAlerts
	if r.Method == "POST" {
		if r.Header.Get("X-Platform-Request") != "1" {
			s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
			return
		}
		if !v.can(project.PermMuteAlerts) {
			s.fail(w, r, http.StatusForbidden, "the project's editors mute its alerts")
			return
		}
		var req struct {
			Key   string  `json:"key"`
			Hours float64 `json:"hours"` // 0: unmute
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Key == "" || req.Hours < 0 || req.Hours > 24*7 {
			s.fail(w, r, http.StatusBadRequest, "want {key, hours}: up to a week")
			return
		}
		pa.loadMutes(r.Context(), s) // another pod's since this one's last pass
		pa.mu.Lock()
		if req.Hours == 0 {
			delete(pa.muted, v.Name+"/"+req.Key)
		} else {
			pa.muted[v.Name+"/"+req.Key] = time.Now().Add(time.Duration(req.Hours * float64(time.Hour)))
		}
		err := pa.saveMutes(r.Context(), s)
		pa.mu.Unlock()
		if err != nil {
			s.fail(w, r, http.StatusBadGateway, "the mute isn't kept: "+err.Error())
			return
		}
		s.log.Info("project alert muted", "project", v.Name, "key", req.Key, "hours", req.Hours, "by", sess.User.Username)
		s.record(r, sess.User.Username, "alert.muted", "project:"+v.Name+"/alert:"+req.Key, fmt.Sprintf("for %g hours", req.Hours))
	}
	pa.mu.Lock()
	unknown := append([]string{}, pa.unknown[v.Name]...)
	out := []issue{}
	for _, is := range pa.issues[v.Name] {
		if t := pa.muted[v.Name+"/"+is.Key]; time.Now().Before(t) {
			is.MutedUntil = &t
		}
		out = append(out, is)
	}
	pa.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	n := v.Spec.Spec.Notify
	dest := map[string]any{"teams": n != nil && n.Teams != "", "slack": n != nil && n.Slack != "", "email": []string{}}
	if n != nil && len(n.Email) > 0 {
		dest["email"] = n.Email
	}
	writeJSON(w, http.StatusOK, map[string]any{"checked": pa.checked, "issues": out, "notify": dest, "unknown": unknown})
}

// CopyDownAfter is how long a source's copy may be not running (starting,
// restarting, refused by its database) before its project is told.
const CopyDownAfter = 10 * time.Minute

// copyIssues are the project's copies (use case 8.1) that haven't been
// running for CopyDownAfter, as the operator last found them (its status's
// copy part, which names each copy that isn't).
func (s *server) copyIssues(ctx context.Context, p *project.Project, link string) []issue {
	pa := s.projectAlerts
	if pa == nil || s.store == nil {
		return nil
	}
	var part *project.Part
	if statuses, err := s.store.Statuses(ctx); err == nil {
		for _, st := range statuses {
			if st.Name != p.Metadata.Name {
				continue
			}
			for i := range st.Parts {
				if st.Parts[i].System == "copy" {
					part = &st.Parts[i]
				}
			}
		}
	}
	var out []issue
	pa.mu.Lock()
	defer pa.mu.Unlock()
	for _, src := range p.Spec.Sources {
		key := p.Metadata.Name + "/copy/" + src.Name
		if src.Copy == nil || part == nil || part.OK || !strings.Contains(part.Message, "copying "+src.Name+":") {
			delete(pa.down, key)
			continue
		}
		since, ok := pa.down[key]
		if !ok {
			pa.down[key] = time.Now()
			continue
		}
		if time.Since(since) < CopyDownAfter {
			continue
		}
		out = append(out, issue{Key: "copy/" + src.Name, Kind: "copy",
			Title:  fmt.Sprintf("the copy of %s isn't running (since %s)", src.Name, since.UTC().Format("15:04 MST")),
			Detail: part.Message, Link: link})
	}
	return out
}

// productIssues are the project's products whose tables break their
// contracts (use case 10.3): a contracted column gone, the table older than
// its window, its checks failing (as the issues found already say). Each
// names the consumers it lets down.
func (s *server) productIssues(ctx context.Context, p *project.Project, link string, found []issue, unknown *[]string) []issue {
	t := p.Spec.Tables
	if t == nil || s.history == nil {
		return nil
	}
	var out []issue
	for _, pr := range p.Spec.Products {
		c := pr.Contract
		if c == nil {
			continue
		}
		who := "no consumers yet"
		if len(pr.Consumers) > 0 {
			who = "its consumers: " + strings.Join(pr.Consumers, ", ")
		}
		at := link + "/data?table=" + pr.Table
		if len(c.Columns) > 0 {
			cols, err := s.history.iceberg.Columns(ctx, t.Namespace, pr.Table)
			if err != nil {
				*unknown = append(*unknown, fmt.Sprintf("the product %s's columns: %v", pr.Name, err))
			} else {
				var missing []string
				for _, col := range c.Columns {
					if !slices.Contains(cols, col) {
						missing = append(missing, col)
					}
				}
				if len(missing) > 0 {
					out = append(out, issue{Key: "product/" + pr.Name + "/columns", Kind: "product", Link: at, Detail: who,
						Title: fmt.Sprintf("the product %s breaks its contract: %s gone from %s", pr.Name, strings.Join(missing, ", "), pr.Table)})
				}
			}
		}
		if within, ok := project.Within(c.Within); ok && s.projectAlerts != nil {
			last, err := catalog.LastChange(ctx, s.projectAlerts.http, s.projectAlerts.nessie, s.projectAlerts.warehous, t.Namespace, pr.Table)
			if err != nil {
				*unknown = append(*unknown, fmt.Sprintf("when the product %s last changed: %v", pr.Name, err))
			} else if last.IsZero() || time.Since(last) > within {
				out = append(out, issue{Key: "product/" + pr.Name + "/stale", Kind: "product", Link: at, Detail: who,
					Title: fmt.Sprintf("the product %s breaks its contract: %s not changed within %s", pr.Name, pr.Table, c.Within)})
			}
		}
		if c.Checks && slices.ContainsFunc(found, func(i issue) bool { return strings.HasPrefix(i.Key, "check/"+pr.Table+"/") }) {
			out = append(out, issue{Key: "product/" + pr.Name + "/checks", Kind: "product", Link: at + "&tab=quality", Detail: who,
				Title: fmt.Sprintf("the product %s breaks its contract: a check on %s is failing", pr.Name, pr.Table)})
		}
	}
	return out
}
