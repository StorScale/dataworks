package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

// The governance view (use case 12.9): across every project, what awaits
// someone's approval, the rules readers are under, the agents and what they
// spend, the data products and their certification, and the erasures with
// their deadlines. To the administrators, the products' certifiers and the
// data-protection approvers: it reads what exists, and changes nothing.

type govPending struct {
	Project string `json:"project"`
	Kind    string `json:"kind"` // agent, delivery, access (a product asked for)
	Name    string `json:"name"`
	Waits   string `json:"waits"` // for whom, in words
	Since   string `json:"since,omitempty"`
	Link    string `json:"link"`
}

type govAgent struct {
	Project  string   `json:"project"`
	Name     string   `json:"name"`
	Owner    string   `json:"owner"`
	Approved bool     `json:"approved"`
	Tools    []string `json:"tools"`
	Runs     string   `json:"runs,omitempty"` // its schedule, and what it runs
	Budget   float64  `json:"budget"`
	Spend    float64  `json:"spend"`
	LastRun  *govRun  `json:"lastRun,omitempty"` // a scheduled agent's latest run
}

type govRun struct {
	State   string `json:"state"`
	Started string `json:"started"`
}

// govTagRule is what a catalog tag means across the platform (12.5): its mask, and who sees through it.
type govTagRule struct {
	Tag    string   `json:"tag"`
	Mask   string   `json:"mask"`
	Except []string `json:"except"`
}

type govRules struct {
	Project    string   `json:"project"`
	Readers    []string `json:"readers"`              // the groups and people who read it
	Tables     []string `json:"tables,omitempty"`     // readers see only these
	RowFilters []string `json:"rowFilters,omitempty"` // table: condition
	Masks      []string `json:"masks,omitempty"`      // table.column: how
	People     []string `json:"people,omitempty"`     // its tables of people's data
	Tagged     []string `json:"tagged,omitempty"`     // table.column: tag, masked by the install's rule for it
}

type govProduct struct {
	Project   string   `json:"project"`
	Name      string   `json:"name"`
	Owner     string   `json:"owner"`
	Certified bool     `json:"certified"`
	Consumers []string `json:"consumers"`
}

type govErasure struct {
	Project string `json:"project"`
	ID      string `json:"id"`
	At      string `json:"at"`
	DueBy   string `json:"dueBy"`
	By      string `json:"by"`
	Errors  int    `json:"errors"` // tables it couldn't erase from
}

func (s *server) handleGovernance(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	u := sess.User
	if !s.isAdmin(u) && !s.inGroups(u.Groups, s.cfg.ProductCertifierGroups) && !s.inGroups(u.Groups, s.cfg.DeliveryApproverGroups) {
		s.fail(w, r, http.StatusForbidden, "the administrators, the products' certifiers and the data-protection approvers see the governance view")
		return
	}
	defs, _ := s.roleDefs(r.Context()) // the roles of the company's own: who among them is under the readers' rules
	specs, err := s.store.Projects(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	pending, agents, rules, products, erasures := []govPending{}, []govAgent{}, []govRules{}, []govProduct{}, []govErasure{}
	for _, p := range specs {
		name, link := p.Metadata.Name, "/p/"+p.Metadata.Name
		spend := map[string]float64{}
		for _, use := range s.llmUses(r.Context(), p) {
			spend[use.Who] = use.Spend
		}
		runs := s.agentRuns(r, p)
		for _, a := range p.Spec.Agents {
			ga := govAgent{Project: name, Name: a.Name, Owner: a.Owner, Approved: a.Approved(), Tools: a.Tools, Budget: a.Budget, Spend: spend[a.Name],
				LastRun: runs[a.DAG(name)]}
			if ga.Tools == nil {
				ga.Tools = []string{}
			}
			if a.Schedule != "" {
				what := a.Notebook
				if what == "" {
					what = "its prompt"
				}
				ga.Runs = what + " at " + a.Schedule
			}
			agents = append(agents, ga)
			if !a.Approved() {
				pending = append(pending, govPending{Project: name, Kind: "agent", Name: a.Name, Link: link + "/agents",
					Waits: "an editor of " + name + " other than " + a.RegisteredBy, Since: stamp(a.RegisteredAt)})
			}
		}
		for _, d := range p.Spec.Deliveries {
			if !d.Approved() {
				needs := s.needs(p, d)
				pending = append(pending, govPending{Project: name, Kind: "delivery", Name: d.Name, Link: link + "/deliveries",
					Waits: strings.Join(needs, "; ")})
			}
		}
		for _, pr := range p.Spec.Products {
			gp := govProduct{Project: name, Name: pr.Name, Owner: pr.Owner, Certified: pr.Certified(), Consumers: []string{}}
			gp.Consumers = append(gp.Consumers, pr.Consumers...)
			products = append(products, gp)
			if s.files != nil {
				for _, q := range s.requests(r, name, pr.Name) {
					pending = append(pending, govPending{Project: name, Kind: "access", Name: pr.Name + " for " + q.Consumer,
						Waits: "an editor of " + name, Since: q.At, Link: "/products"})
				}
			}
		}
		gr := govRules{Project: name, Readers: []string{}}
		for _, m := range p.Spec.Members {
			def, custom := defs[m.Role]
			switch {
			case m.Role == project.Reader:
				gr.Readers = append(gr.Readers, firstOf(m.Group, m.User))
			case custom && def.Has(project.PermRead) && !def.Has(project.PermReadAll):
				gr.Readers = append(gr.Readers, firstOf(m.Group, m.User)+" ("+m.Role+")")
			}
		}
		if t := p.Spec.Tables; t != nil {
			if rv := t.Readers; rv != nil {
				gr.Tables = rv.Tables
				for _, f := range rv.RowFilters {
					gr.RowFilters = append(gr.RowFilters, f.Table+": "+f.Filter)
				}
				for _, m := range rv.Masks {
					gr.Masks = append(gr.Masks, m.Table+"."+m.Column+": "+m.Type)
				}
			}
			for _, pp := range t.People {
				gr.People = append(gr.People, pp.Table+"."+pp.Column)
			}
		}
		if c, err := s.classificationOf(r.Context(), name); err == nil {
			for _, col := range c.Columns {
				if rule, ok := s.cfg.Protection[col.Tag]; ok && (col.State == "confirmed" || s.cfg.ProtectSuggested) {
					gr.Tagged = append(gr.Tagged, fmt.Sprintf("%s.%s: %s (%s), %s", col.Table, col.Column, col.Tag, col.State, rule.Mask))
				}
			}
		}
		rules = append(rules, gr)
		if s.files != nil && s.erasureAllowed(u) {
			keys, _ := s.files.ListFiles(r.Context(), "erasures/"+name+"/")
			for _, k := range keys {
				if b, err := s.files.GetFile(r.Context(), k); err == nil && b != nil {
					var e erasure
					if json.Unmarshal(b, &e) == nil {
						erasures = append(erasures, govErasure{Project: name, ID: e.ID, At: stamp(e.At), DueBy: e.DueBy, By: e.By, Errors: len(e.Errors)})
					}
				}
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Since < pending[j].Since })
	sort.Slice(erasures, func(i, j int) bool { return erasures[i].At > erasures[j].At })
	certified := 0
	for _, p := range products {
		if p.Certified {
			certified++
		}
	}
	tagRules := []govTagRule{}
	for tag, rule := range s.cfg.Protection {
		except := rule.Except
		if except == nil {
			except = []string{}
		}
		tagRules = append(tagRules, govTagRule{Tag: tag, Mask: rule.Mask, Except: except})
	}
	sort.Slice(tagRules, func(i, j int) bool { return tagRules[i].Tag < tagRules[j].Tag })
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending, "agents": agents, "rules": rules, "products": products, "erasures": erasures,
		"tagRules": tagRules, "suggestedApplied": s.cfg.ProtectSuggested,
		"summary": fmt.Sprintf("%d awaiting approval; %d agents (%d approved); %d of %d products certified; %d erasures recorded",
			len(pending), len(agents), countApproved(agents), certified, len(products), len(erasures))})
}

// agentRuns are the latest runs of the project's scheduled agents, by DAG;
// none when its Airflow doesn't answer (the page shows what it can).
func (s *server) agentRuns(r *http.Request, p *project.Project) map[string]*govRun {
	out := map[string]*govRun{}
	if s.airflow == nil || !slices.ContainsFunc(p.Spec.Agents, func(a project.Agent) bool { return a.Runs() }) {
		return out
	}
	af, _, err := s.pipelinesOf(r.Context(), &projectView{Name: p.Metadata.Name, Spec: p})
	if err != nil {
		return out
	}
	ps, err := af.Of(r.Context(), p.Metadata.Name, 1)
	if err != nil {
		return out
	}
	for _, pl := range ps {
		if len(pl.Runs) > 0 {
			out[pl.ID] = &govRun{State: pl.Runs[0].State, Started: stamp(pl.Runs[0].Started)}
		}
	}
	return out
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func countApproved(as []govAgent) int {
	n := 0
	for _, a := range as {
		if a.Approved {
			n++
		}
	}
	return n
}
