// Package audit is the platform's audit log, in Ranger's Solr (the
// ranger_audits collection): Ranger's own records of every access Trino
// allowed or refused, and the platform's records of what people did in it
// (a project changed, an alert muted, maintenance asked for), written in
// Ranger's fields so Ranger's Audit page shows both.
package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Repo is the platform's records' service name, as Ranger's are its
// services' (the Trino service's records name that).
const Repo = "storscale-platform"

// An Event is one record: the platform's, or Ranger's.
type Event struct {
	Time     time.Time `json:"time"`
	User     string    `json:"user"`
	Action   string    `json:"action"`   // project.saved, alerts.muted; Ranger's: select, alter, show...
	Resource string    `json:"resource"` // project:sales; Ranger's: iceberg/sales/orders/region
	Allowed  bool      `json:"allowed"`
	Detail   string    `json:"detail,omitempty"` // what changed; Ranger's: the query, when it has it
	Source   string    `json:"source"`           // platform, or trino (Ranger's)
	Policy   int64     `json:"policy,omitempty"` // Ranger's policy that decided, if any
	IP       string    `json:"ip,omitempty"`
	Reason   string    `json:"reason,omitempty"` // Ranger's, for a refusal
}

type Log struct {
	URL  string // http://ranger-solr:8983/solr/ranger_audits
	HTTP *http.Client
	Keep time.Duration // how long the platform's records are kept
}

// doc is an Event in Ranger's audit fields.
type doc struct {
	ID       string `json:"id"`
	EvtTime  string `json:"evtTime"`
	ReqUser  string `json:"reqUser"`
	Access   string `json:"access"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	ResType  string `json:"resType"`
	Result   int    `json:"result"`
	ReqData  string `json:"reqData,omitempty"`
	Repo     string `json:"repo"`
	RepoType int    `json:"repoType"`
	Enforcer string `json:"enforcer"`
	Agent    string `json:"agent"`
	LogType  string `json:"logType"`
	CliIP    string `json:"cliIP,omitempty"`
	TTL      string `json:"_ttl_,omitempty"`
	SeqNum   int    `json:"seq_num"`
	Count    int    `json:"event_count"`
}

// Add records one of the platform's events, by the time it says (now when zero).
func (l *Log) Add(ctx context.Context, e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	id := make([]byte, 16)
	rand.Read(id)
	resType := e.Resource
	if i := strings.IndexAny(resType, ":/"); i > 0 {
		resType = resType[:i]
	}
	d := doc{ID: "storscale-" + hex.EncodeToString(id), EvtTime: e.Time.UTC().Format("2006-01-02T15:04:05.000Z"),
		ReqUser: e.User, Access: e.Action, Action: e.Action, Resource: e.Resource, ResType: resType, ReqData: e.Detail,
		Repo: Repo, Enforcer: Repo, Agent: "platformd", LogType: "RangerAudit", CliIP: e.IP, Count: 1}
	if e.Allowed {
		d.Result = 1
	}
	if l.Keep > 0 {
		d.TTL = fmt.Sprintf("+%dDAYS", int(l.Keep.Hours()/24))
	}
	body, _ := json.Marshal([]doc{d})
	req, err := http.NewRequestWithContext(ctx, "POST", l.URL+"/update?commitWithin=2000", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := l.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("audit: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("audit: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

// A Query picks records: all of them when it's empty.
type Query struct {
	Resources []string  // these resources, and what's under them (project:sales, iceberg/sales)
	User      string    // by this person
	Result    string    // allowed, denied, or "" for both
	Source    string    // platform, trino (Ranger's), or "" for both
	Text      string    // in the resource, the action or the detail
	Before    time.Time // older than this (the next page)
	Since     time.Time
	Limit     int
}

// esc escapes a term for Solr's query syntax.
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\+-&|!(){}[]^"~*?:/ `, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// params are Solr's, for the query.
func (q Query) params() url.Values {
	v := url.Values{"q": {"*:*"}, "sort": {"evtTime desc"}, "wt": {"json"}}
	var fq []string
	if len(q.Resources) > 0 {
		var any []string
		for _, r := range q.Resources {
			r = strings.ToLower(r)
			any = append(any, "resource:"+esc(r), "resource:"+esc(r)+`\/*`, "resource:"+esc(r)+`\:*`)
		}
		fq = append(fq, "("+strings.Join(any, " OR ")+")")
	}
	if q.User != "" {
		fq = append(fq, "reqUser:"+esc(strings.ToLower(q.User)))
	}
	switch q.Result {
	case "allowed":
		fq = append(fq, "result:1")
	case "denied":
		fq = append(fq, "result:0")
	}
	switch q.Source {
	case "platform":
		fq = append(fq, "repo:"+esc(Repo))
	case "trino": // Ranger's: its Trino service's, whatever it's named
		fq = append(fq, "-repo:"+esc(Repo))
	}
	if t := strings.TrimSpace(strings.ToLower(q.Text)); t != "" {
		w := "*" + esc(t) + "*"
		fq = append(fq, "(resource:"+w+" OR access:"+w+" OR reqData:"+w+")")
	}
	if !q.Before.IsZero() || !q.Since.IsZero() {
		from, to := "*", "*"
		if !q.Since.IsZero() {
			from = q.Since.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		if !q.Before.IsZero() {
			to = q.Before.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		fq = append(fq, "evtTime:["+from+" TO "+to+"}")
	}
	v["fq"] = fq
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	v.Set("rows", strconv.Itoa(limit))
	return v
}

// Find is the records a query picks, newest first.
func (l *Log) Find(ctx context.Context, q Query) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", l.URL+"/select?"+q.params().Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := l.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("audit: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return nil, fmt.Errorf("audit: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	var body struct {
		Response struct {
			Docs []struct {
				EvtTime  time.Time `json:"evtTime"`
				ReqUser  string    `json:"reqUser"`
				Access   string    `json:"access"`
				Action   string    `json:"action"`
				Resource string    `json:"resource"`
				Result   int       `json:"result"`
				ReqData  string    `json:"reqData"`
				Repo     string    `json:"repo"`
				Policy   int64     `json:"policy"`
				CliIP    string    `json:"cliIP"`
				Reason   string    `json:"reason"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("audit: %v", err)
	}
	out := []Event{}
	for _, d := range body.Response.Docs {
		e := Event{Time: d.EvtTime, User: d.ReqUser, Action: d.Access, Resource: d.Resource, Allowed: d.Result == 1,
			Detail: d.ReqData, Source: d.Repo, Policy: d.Policy, IP: d.CliIP, Reason: d.Reason}
		if e.Action == "" {
			e.Action = d.Action
		}
		if e.Source == Repo {
			e.Source = "platform"
		} else {
			e.Source = "trino" // Ranger's, from its Trino service (lakehouse)
		}
		if e.Policy < 0 {
			e.Policy = 0 // Ranger's -1: no policy decided
		}
		out = append(out, e)
	}
	return out, nil
}
