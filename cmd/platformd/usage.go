package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// Usage and cost per project (use case 13.1, R13.1–R13.3). What a project
// uses is gathered three ways:
//
//   - Trino's time: Trino's HTTP event listener posts each finished query
//     here (/internal/usage/trino, with its shared secret); a query counts
//     for the projects whose tables it read (their namespaces), split evenly
//     when it read several's; its CPU time, the bytes it read, who ran it.
//     Each platformd adds up its own and keeps them, a minute apart, as
//     usage/<project>/<month>/trino-<host>.json in the platform's bucket;
//     readers add up every host's.
//   - storage: once a day, each table's size (its data files, Iceberg's
//     $files, read as the maintenance account) and the project's bucket's
//     (Buckets' count, by the operator), kept as usage/<project>/<month>/storage.json.
//   - the model calls: what the gateway says the project's keys have spent
//     this month, sampled with the storage for the months gone.
//
// The install's rate card (USAGE_RATES) prices a CPU-hour and a GB-month;
// the model calls are priced already. A project's cost.budget is alerted at
// 80% and at 100%. The project's editors see its usage, and the install's
// usage viewers (USAGE_VIEWER_GROUPS: finance) and administrators every project's.

// rates are the install's rate card.
type rates struct {
	Currency       string  `json:"currency"`
	CPUHour        float64 `json:"cpuHour"`        // a CPU-hour of Trino's
	StorageGBMonth float64 `json:"storageGBMonth"` // a GB kept a month
}

func parseRates(text string) (rates, error) {
	r := rates{Currency: "USD", CPUHour: 0.05, StorageGBMonth: 0.023}
	if strings.TrimSpace(text) == "" {
		return r, nil
	}
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return r, err
	}
	if r.CPUHour < 0 || r.StorageGBMonth < 0 {
		return r, fmt.Errorf("a rate is a price, not less than nothing")
	}
	return r, nil
}

const usageTopQueries = 10

// trinoUsage is a project's Trino use in a month, as one platformd counted it.
type trinoUsage struct {
	Queries    int                    `json:"queries"`
	CPUSeconds float64                `json:"cpuSeconds"`
	ReadBytes  float64                `json:"readBytes"`
	Failed     int                    `json:"failed"`
	ByUser     map[string]*usageShare `json:"byUser"`
	ByDay      map[string]*usageShare `json:"byDay"` // 2026-10-21
	Top        []topQuery             `json:"top"`   // the costliest, by CPU
}

type usageShare struct {
	Queries    int     `json:"queries"`
	CPUSeconds float64 `json:"cpuSeconds"`
}

type topQuery struct {
	ID         string    `json:"id"`
	User       string    `json:"user"`
	At         time.Time `json:"at"`
	CPUSeconds float64   `json:"cpuSeconds"`
	ReadBytes  float64   `json:"readBytes"`
	SQL        string    `json:"sql"` // its first 500 characters
}

func newTrinoUsage() *trinoUsage {
	return &trinoUsage{ByUser: map[string]*usageShare{}, ByDay: map[string]*usageShare{}, Top: []topQuery{}}
}

func (u *trinoUsage) add(user, day string, q topQuery, share float64, failed bool) {
	cpu, read := q.CPUSeconds*share, q.ReadBytes*share
	u.Queries++
	u.CPUSeconds += cpu
	u.ReadBytes += read
	if failed {
		u.Failed++
	}
	for _, at := range []struct {
		m map[string]*usageShare
		k string
	}{{u.ByUser, user}, {u.ByDay, day}} {
		if at.m[at.k] == nil {
			at.m[at.k] = &usageShare{}
		}
		at.m[at.k].Queries++
		at.m[at.k].CPUSeconds += cpu
	}
	q.CPUSeconds, q.ReadBytes = cpu, read
	u.Top = append(u.Top, q)
	sort.Slice(u.Top, func(i, j int) bool { return u.Top[i].CPUSeconds > u.Top[j].CPUSeconds })
	if len(u.Top) > usageTopQueries {
		u.Top = u.Top[:usageTopQueries]
	}
}

// merge adds another host's count to this one.
func (u *trinoUsage) merge(o *trinoUsage) {
	u.Queries += o.Queries
	u.CPUSeconds += o.CPUSeconds
	u.ReadBytes += o.ReadBytes
	u.Failed += o.Failed
	for _, pair := range [][2]map[string]*usageShare{{u.ByUser, o.ByUser}, {u.ByDay, o.ByDay}} {
		for k, v := range pair[1] {
			if pair[0][k] == nil {
				pair[0][k] = &usageShare{}
			}
			pair[0][k].Queries += v.Queries
			pair[0][k].CPUSeconds += v.CPUSeconds
		}
	}
	u.Top = append(u.Top, o.Top...)
	sort.Slice(u.Top, func(i, j int) bool { return u.Top[i].CPUSeconds > u.Top[j].CPUSeconds })
	if len(u.Top) > usageTopQueries {
		u.Top = u.Top[:usageTopQueries]
	}
}

// usageCounter is this platformd's count, by project and month, and what's changed since it was kept.
type usageCounter struct {
	mu    sync.Mutex
	host  string
	by    map[string]*trinoUsage // project/month
	dirty map[string]bool
}

func usageKey(projectName, month, host string) string {
	return "usage/" + projectName + "/" + month + "/trino-" + host + ".json"
}

// count is one finished query, for the projects it read.
func (c *usageCounter) count(s *server, projects map[string]bool, user string, q topQuery, failed bool) {
	if len(projects) == 0 {
		return
	}
	month, day := q.At.UTC().Format("2006-01"), q.At.UTC().Format("2006-01-02")
	share := 1 / float64(len(projects))
	c.mu.Lock()
	defer c.mu.Unlock()
	for name := range projects {
		k := name + "/" + month
		u := c.by[k]
		if u == nil {
			u = newTrinoUsage()
			if s.files != nil { // this host's count so far, kept before it started again
				if b, err := s.files.GetFile(context.Background(), usageKey(name, month, c.host)); err == nil && b != nil {
					json.Unmarshal(b, u)
					if u.ByUser == nil || u.ByDay == nil {
						u.ByUser, u.ByDay = map[string]*usageShare{}, map[string]*usageShare{}
					}
				}
			}
			c.by[k] = u
		}
		u.add(user, day, q, share, failed)
		c.dirty[k] = true
	}
}

// keep writes what's changed to the platform's bucket.
func (c *usageCounter) keep(ctx context.Context, s *server) {
	c.mu.Lock()
	writes := map[string][]byte{}
	for k := range c.dirty {
		name, month, _ := strings.Cut(k, "/")
		b, _ := json.Marshal(c.by[k])
		writes[usageKey(name, month, c.host)] = b
	}
	c.dirty = map[string]bool{}
	// a month gone is kept; its counts needn't stay in memory
	this := time.Now().UTC().Format("2006-01")
	for k := range c.by {
		if !strings.HasSuffix(k, "/"+this) && !strings.HasSuffix(k, "/"+time.Now().UTC().AddDate(0, 0, -1).Format("2006-01")) {
			delete(c.by, k)
		}
	}
	c.mu.Unlock()
	for key, b := range writes {
		if err := s.files.PutFile(ctx, key, b, "application/json"); err != nil {
			s.log.Warn("usage: keeping the count", "file", key, "error", err)
		}
	}
}

// startUsage counts Trino's queries as they finish, and samples storage and spend each day.
func (s *server) startUsage(ctx context.Context) {
	if s.files == nil || s.store == nil {
		return
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "platformd"
	}
	s.usage = &usageCounter{host: host, by: map[string]*trinoUsage{}, dirty: map[string]bool{}}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		sampled, tried := "", time.Time{}
		for {
			s.usage.keep(ctx, s)
			// once a day; a sample that couldn't be taken whole (Trino or the operator starting) again ten minutes on
			if day := time.Now().UTC().Format("2006-01-02"); day != sampled && time.Since(tried) >= 10*time.Minute {
				tried = time.Now()
				if s.sampleUsage(ctx, day) {
					sampled = day
				}
			}
			select {
			case <-ctx.Done():
				s.usage.keep(context.Background(), s)
				return
			case <-tick.C:
			}
		}
	}()
}

// trinoEvent is the part of Trino's QueryCompletedEvent the count reads.
type trinoEvent struct {
	Metadata struct {
		QueryID    string `json:"queryId"`
		Query      string `json:"query"`
		QueryState string `json:"queryState"`
	} `json:"metadata"`
	Statistics struct {
		CPUTime            json.RawMessage `json:"cpuTime"`
		PhysicalInputBytes float64         `json:"physicalInputBytes"`
	} `json:"statistics"`
	Context struct {
		User string `json:"user"`
	} `json:"context"`
	IOMetadata struct {
		Inputs []struct {
			Catalog string `json:"catalogName"`
			Schema  string `json:"schema"`
		} `json:"inputs"`
	} `json:"ioMetadata"`
	FailureInfo json.RawMessage `json:"failureInfo"`
	EndTime     json.RawMessage `json:"endTime"`
}

// seconds reads a duration as Trino's JSON writes it: a number of seconds,
// or text (1.5s, 250ms, PT1.5S).
func seconds(raw json.RawMessage) float64 {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0
	}
	if d, err := time.ParseDuration(strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(text, "PT"), "pt"))); err == nil {
		return d.Seconds()
	}
	if f, err := strconv.ParseFloat(strings.TrimSuffix(text, "s"), 64); err == nil {
		return f
	}
	return 0
}

// instant reads a time as Trino's JSON writes it: seconds since 1970 (with a fraction), or RFC 3339.
func instant(raw json.RawMessage) time.Time {
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		sec, frac := math.Modf(n)
		return time.Unix(int64(sec), int64(frac*1e9)).UTC()
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if t, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// handleTrinoEvents takes Trino's events (its HTTP event listener), one or a list, with Trino's shared secret.
func (s *server) handleTrinoEvents(w http.ResponseWriter, r *http.Request) {
	want := os.Getenv("TRINO_SHARED_SECRET")
	if want == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Storscale-Usage")), []byte(want)) != 1 {
		http.Error(w, "not Trino", http.StatusUnauthorized)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var events []trinoEvent
	if err := json.Unmarshal(data, &events); err != nil {
		var one trinoEvent
		if err := json.Unmarshal(data, &one); err != nil {
			http.Error(w, "a QueryCompletedEvent, as JSON", http.StatusBadRequest)
			return
		}
		events = []trinoEvent{one}
	}
	if s.usage == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var read []string
	for _, e := range events {
		for _, in := range e.IOMetadata.Inputs {
			read = append(read, in.Catalog+"."+in.Schema)
		}
	}
	namespaces := s.namespaces(r.Context(), read)
	for _, e := range events {
		projects := map[string]bool{}
		for _, in := range e.IOMetadata.Inputs {
			if name, ok := namespaces[in.Catalog+"."+in.Schema]; ok {
				projects[name] = true
			}
		}
		sql := e.Metadata.Query
		if len(sql) > 500 {
			sql = sql[:500] + "…"
		}
		s.usage.count(s, projects, e.Context.User, topQuery{ID: e.Metadata.QueryID, User: e.Context.User, At: instant(e.EndTime),
			CPUSeconds: seconds(e.Statistics.CPUTime), ReadBytes: e.Statistics.PhysicalInputBytes, SQL: sql},
			len(e.FailureInfo) > 0 && string(e.FailureInfo) != "null")
	}
	w.WriteHeader(http.StatusNoContent)
}

// namespaces are the projects by their tables' catalog.namespace, a minute
// old at most; read again sooner (a few seconds apart) when one asked for
// isn't there, so a project made a moment ago counts its first queries.
func (s *server) namespaces(ctx context.Context, want []string) map[string]string {
	s.mu.Lock()
	fresh := s.nsCache != nil && time.Since(s.nsCacheAt) < time.Minute
	for _, ns := range want {
		if _, ok := s.nsCache[ns]; !ok && time.Since(s.nsCacheAt) > 5*time.Second {
			fresh = false
		}
	}
	if fresh {
		defer s.mu.Unlock()
		return s.nsCache
	}
	s.mu.Unlock()
	out := map[string]string{}
	if specs, err := s.store.Projects(ctx); err == nil {
		for _, p := range specs {
			if p.Spec.Tables != nil {
				cat, ns := tablesOf(p)
				out[cat+"."+ns] = p.Metadata.Name
			}
		}
	}
	s.mu.Lock()
	s.nsCache, s.nsCacheAt = out, time.Now()
	s.mu.Unlock()
	return out
}

// storageSample is a project's storage and model spend on the days sampled in a month.
type storageSample struct {
	Days map[string]storageDay `json:"days"`
}

type storageDay struct {
	Tables   map[string]int64 `json:"tables"` // bytes of each table's data files
	Bucket   int64            `json:"bucket"` // bytes in its bucket (files)
	LLMSpend float64          `json:"llmSpend"`
}

func (d storageDay) bytes() int64 {
	n := d.Bucket
	for _, b := range d.Tables {
		n += b
	}
	return n
}

// sampleUsage samples each project's storage and model spend, once a day;
// whether it could (the buckets' sizes and every table's read).
func (s *server) sampleUsage(ctx context.Context, day string) bool {
	specs, err := s.store.Projects(ctx)
	if err != nil {
		return false
	}
	whole := true
	buckets := map[string]int64{}
	if req, err := http.NewRequestWithContext(ctx, "GET", s.cfg.OperatorURL+"/buckets/sizes", nil); err == nil {
		resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
		if err == nil && resp.StatusCode == 200 {
			whole = json.NewDecoder(resp.Body).Decode(&buckets) == nil && whole
		} else {
			whole = false
			s.log.Warn("usage: the buckets' sizes", "error", err)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	month := day[:7]
	for _, p := range specs {
		d := storageDay{Tables: map[string]int64{}, Bucket: buckets[bucketOf(p)]}
		if p.Spec.Files != nil && p.Spec.Files.Bucket != "" {
			d.Bucket = buckets[p.Spec.Files.Bucket]
		}
		if s.maintainer != nil && p.Spec.Tables != nil {
			var ok bool
			if d.Tables, ok = s.tableSizes(ctx, s.maintainer.trino, p); !ok {
				whole = false
			}
		}
		for _, u := range s.llmUses(ctx, p) {
			d.LLMSpend += u.Spend
		}
		key := "usage/" + p.Metadata.Name + "/" + month + "/storage.json"
		sample := storageSample{Days: map[string]storageDay{}}
		if b, err := s.files.GetFile(ctx, key); err == nil && b != nil {
			json.Unmarshal(b, &sample)
			if sample.Days == nil {
				sample.Days = map[string]storageDay{}
			}
		}
		sample.Days[day] = d
		b, _ := json.Marshal(sample)
		if err := s.files.PutFile(ctx, key, b, "application/json"); err != nil {
			s.log.Warn("usage: keeping the day's sample", "project", p.Metadata.Name, "error", err)
			whole = false
		}
	}
	return whole
}

// tableSizes are each of the project's tables' data files' bytes, as
// Iceberg's $files says; and whether they could be read (a namespace not
// made yet has none, and that's whole).
func (s *server) tableSizes(ctx context.Context, tc *trino.Client, p *project.Project) (map[string]int64, bool) {
	out := map[string]int64{}
	cat, ns := tablesOf(p)
	rows, err := tc.Exec(ctx, "SHOW TABLES FROM "+trino.Quote(cat)+"."+trino.Quote(ns))
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return out, true
		}
		s.log.Warn("usage: the tables' sizes", "project", p.Metadata.Name, "error", err)
		return out, false
	}
	for _, row := range rows {
		t, _ := row[0].(string)
		if t == "" {
			continue
		}
		got, err := tc.Exec(ctx, "SELECT coalesce(sum(file_size_in_bytes), 0) FROM "+trino.Quote(cat)+"."+trino.Quote(ns)+"."+trino.Quote(t+"$files"))
		if err == nil && len(got) == 1 {
			if n, err := strconv.ParseInt(fmt.Sprint(got[0][0]), 10, 64); err == nil {
				out[t] = n
			}
		}
	}
	return out, true
}

// usageOf is a project's month: Trino's time (every host's), storage, model spend, and what it costs.
type usageMonth struct {
	Project   string             `json:"project"`
	Month     string             `json:"month"`
	Trino     *trinoUsage        `json:"trino"`
	Storage   *storageDay        `json:"storage,omitempty"` // the latest day sampled
	StorageAt string             `json:"storageAt,omitempty"`
	AvgGB     float64            `json:"avgGB"` // over the days sampled
	LLMSpend  float64            `json:"llmSpend"`
	Cost      map[string]float64 `json:"cost"` // trino, storage, llm, total
	Budget    float64            `json:"budget,omitempty"`
	Share     *float64           `json:"share,omitempty"` // of the budget
	Currency  string             `json:"currency"`
}

func (s *server) usageOf(ctx context.Context, p *project.Project, month string) *usageMonth {
	u := &usageMonth{Project: p.Metadata.Name, Month: month, Trino: newTrinoUsage(), Currency: s.cfg.Rates.Currency, Cost: map[string]float64{}}
	prefix := "usage/" + p.Metadata.Name + "/" + month + "/"
	if s.files != nil {
		keys, _ := s.files.ListFiles(ctx, prefix)
		for _, k := range keys {
			b, err := s.files.GetFile(ctx, k)
			if err != nil || b == nil {
				continue
			}
			switch {
			case strings.HasPrefix(strings.TrimPrefix(k, prefix), "trino-"):
				var t trinoUsage
				if json.Unmarshal(b, &t) == nil {
					u.Trino.merge(&t)
				}
			case strings.HasSuffix(k, "/storage.json"):
				var sample storageSample
				if json.Unmarshal(b, &sample) == nil && len(sample.Days) > 0 {
					var days []string
					total := 0.0
					for d, x := range sample.Days {
						days = append(days, d)
						total += float64(x.bytes())
					}
					sort.Strings(days)
					last := sample.Days[days[len(days)-1]]
					u.Storage, u.StorageAt, u.LLMSpend = &last, days[len(days)-1], last.LLMSpend
					u.AvgGB = total / float64(len(days)) / 1e9
				}
			}
		}
	}
	this := time.Now().UTC().Format("2006-01")
	if month == this { // this month's spend now, not as of this morning
		spend := 0.0
		for _, x := range s.llmUses(ctx, p) {
			spend += x.Spend
		}
		u.LLMSpend = spend
	}
	// A GB-month: the average GB, for the part of the month gone (all of a month gone).
	part := 1.0
	if month == this {
		now := time.Now().UTC()
		days := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		part = float64(now.Day()) / float64(days)
	}
	trinoCost, storageCost := u.Trino.CPUSeconds/3600*s.cfg.Rates.CPUHour, u.AvgGB*part*s.cfg.Rates.StorageGBMonth
	total := trinoCost + storageCost + u.LLMSpend
	u.Cost["trino"], u.Cost["storage"], u.Cost["llm"], u.Cost["total"] = round2(trinoCost), round2(storageCost), round2(u.LLMSpend), round2(total)
	if c := p.Spec.Cost; c != nil && c.Budget > 0 {
		share := total / c.Budget // unrounded: a small project's share of a small budget counts
		u.Budget, u.Share = c.Budget, &share
	}
	return u
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// mayViewUsage: the project's editors, administrators and the install's usage viewers.
func (s *server) mayViewUsage(r *http.Request, role string) bool {
	if role == project.Editor {
		return true
	}
	sess := s.session(r)
	return sess != nil && (s.isAdmin(sess.User) || s.inGroups(sess.User.Groups, s.cfg.UsageGroups))
}

func monthOf(r *http.Request) (string, bool) {
	month := r.URL.Query().Get("month")
	if month == "" {
		return time.Now().UTC().Format("2006-01"), true
	}
	_, err := time.Parse("2006-01", month)
	return month, err == nil
}

// handleProjectUsage is a project's month, beside the month before.
func (s *server) handleProjectUsage(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, role, ok := s.projectViewable(w, r, who)
	if !ok {
		return
	}
	if !s.mayViewUsage(r, role) {
		s.fail(w, r, http.StatusForbidden, "the project's editors, and the install's usage viewers, see what it uses and costs")
		return
	}
	month, ok := monthOf(r)
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "month: as 2026-10")
		return
	}
	t, _ := time.Parse("2006-01", month)
	prev := s.usageOf(r.Context(), p, t.AddDate(0, -1, 0).Format("2006-01"))
	writeJSON(w, http.StatusOK, map[string]any{"usage": s.usageOf(r.Context(), p, month), "before": map[string]any{"month": prev.Month, "cost": prev.Cost},
		"rates": s.cfg.Rates})
}

// projectViewable is projectRole, letting the install's usage viewers (and administrators) in without being members.
func (s *server) projectViewable(w http.ResponseWriter, r *http.Request, who string) (*project.Project, string, bool) {
	if sess := s.session(r); sess != nil && s.inGroups(sess.User.Groups, s.cfg.UsageGroups) && !s.isAdmin(sess.User) {
		p, err := s.store.Project(r.Context(), r.PathValue("name"))
		if err != nil || p == nil {
			s.fail(w, r, http.StatusNotFound, "no such project")
			return nil, "", false
		}
		return p, "", true
	}
	return s.projectRole(w, r, who)
}

// handleUsage is every project's month, for the install's usage viewers and administrators.
func (s *server) handleUsage(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if !s.isAdmin(sess.User) && !s.inGroups(sess.User.Groups, s.cfg.UsageGroups) {
		s.fail(w, r, http.StatusForbidden, "the install's usage viewers and its administrators see every project's usage")
		return
	}
	month, ok := monthOf(r)
	if !ok {
		s.fail(w, r, http.StatusBadRequest, "month: as 2026-10")
		return
	}
	specs, err := s.store.Projects(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	t, _ := time.Parse("2006-01", month)
	before := t.AddDate(0, -1, 0).Format("2006-01")
	type row struct {
		*usageMonth
		Before float64 `json:"before"` // last month's total
	}
	out := []row{}
	for _, p := range specs {
		u := s.usageOf(r.Context(), p, month)
		u.Trino.ByUser, u.Trino.ByDay, u.Trino.Top = nil, nil, nil // the page's rows: the totals
		out = append(out, row{usageMonth: u, Before: s.usageOf(r.Context(), p, before).Cost["total"]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost["total"] > out[j].Cost["total"] })
	writeJSON(w, http.StatusOK, map[string]any{"month": month, "projects": out, "rates": s.cfg.Rates})
}

// usageIssues alert a project at 80% and at 100% of its monthly budget.
func (s *server) usageIssues(ctx context.Context, p *project.Project, link string) []issue {
	c := p.Spec.Cost
	if c == nil || c.Budget <= 0 {
		return nil
	}
	u := s.usageOf(ctx, p, time.Now().UTC().Format("2006-01"))
	total, cur := u.Cost["total"], s.cfg.Rates.Currency
	switch share := *u.Share; {
	case share >= 1:
		return []issue{{Key: "cost/" + p.Metadata.Name + "/" + u.Month, Kind: "cost", Link: link + "/usage",
			Title:  fmt.Sprintf("the project has cost %.2f %s this month, past its budget of %.2f", total, cur, c.Budget),
			Detail: "Nothing is stopped: see what drives it on the Usage tab."}}
	case share >= 0.8:
		return []issue{{Key: "cost/" + p.Metadata.Name + "/" + u.Month + "/near", Kind: "cost", Link: link + "/usage",
			Title: fmt.Sprintf("the project has used %.0f%% of its monthly budget (%.2f of %.2f %s)", share*100, total, c.Budget, cur)}}
	}
	return nil
}
