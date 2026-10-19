package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// A Product is a table the project publishes for others (use case 10.3): its
// owner, version and contract, the business terms it holds; and, the
// platform's alone, the projects consuming it and its certification.
type Product struct {
	Name        string    `json:"name"`
	Table       string    `json:"table"` // one of the project's
	Description string    `json:"description,omitempty"`
	Owner       string    `json:"owner"` // a person: who answers for it
	Version     string    `json:"version"`
	Terms       []string  `json:"terms,omitempty"` // the company's terms (10.1) it holds
	Contract    *Contract `json:"contract,omitempty"`

	// The platform's, kept as they were on each save: the projects reading it
	// in place (approved by the producer's editors), and who certified what.
	Consumers     []string       `json:"consumers,omitempty"`
	Certification *Certification `json:"certification,omitempty"`
}

// A Contract is what a product promises its consumers.
type Contract struct {
	Columns []string `json:"columns,omitempty"` // kept, with these names
	Within  string   `json:"within,omitempty"`  // changed at least this recently, as 24h or 1d
	Checks  bool     `json:"checks,omitempty"`  // the project's checks on its table pass
}

// A Certification is the governance group's: who certified the product, and
// what (Hash: its table, version and contract), and when.
type Certification struct {
	By   string    `json:"by"`
	At   time.Time `json:"at"`
	Hash string    `json:"hash"`
}

// PromiseHash is what a certification covers: the product's table, version
// and contract.
func (pr Product) PromiseHash() string {
	b, _ := json.Marshal(struct {
		Table, Version string
		Contract       *Contract
	}{pr.Table, pr.Version, pr.Contract})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

// Certified says whether the product is certified as it is now.
func (pr Product) Certified() bool {
	return pr.Certification != nil && pr.Certification.Hash == pr.PromiseHash()
}

// GroupOf is a project's group for a role, by the project's name.
func GroupOf(project, role string) string {
	p := Project{Metadata: Metadata{Name: project}}
	return p.Group(role)
}

func (p *Project) productProblems(bad func(string, ...any)) {
	seen := map[string]bool{}
	for _, pr := range p.Spec.Products {
		switch {
		case !identifierRE.MatchString(pr.Name):
			bad("products: name %q: an identifier", pr.Name)
		case seen[pr.Name]:
			bad("products: %q twice", pr.Name)
		case p.Spec.Tables == nil:
			bad("products: %s: a product is one of the project's tables, and it has none (tables)", pr.Name)
		case !identifierRE.MatchString(pr.Table):
			bad("products: %s: table %q isn't a table's name", pr.Name, pr.Table)
		case !principalRE.MatchString(pr.Owner):
			bad("products: %s: its owner is a person (their user name)", pr.Name)
		case pr.Version == "" || len(pr.Version) > 20:
			bad("products: %s: a version, as 1 or 2.3", pr.Name)
		}
		seen[pr.Name] = true
		if c := pr.Contract; c != nil {
			for _, col := range c.Columns {
				if !identifierRE.MatchString(col) {
					bad("products: %s: contract: %q isn't a column's name", pr.Name, col)
				}
			}
			if _, ok := Within(c.Within); c.Within != "" && !ok {
				bad("products: %s: contract: within %q: as 30m, 24h or 7d", pr.Name, c.Within)
			}
		}
		for _, c := range pr.Consumers {
			if !nameRE.MatchString(c) || c == p.Metadata.Name {
				bad("products: %s: consumer %q isn't another project's name", pr.Name, c)
			}
		}
	}
}

// consumerGroups are, for each of the project's tables that is a product
// with consumers, the consuming projects' groups (readers, editors and
// pipelines), and all of them together.
func (p *Project) consumerGroups() (map[string][]string, []string) {
	byTable := map[string][]string{}
	var all []string
	for _, pr := range p.Spec.Products {
		for _, c := range sorted(pr.Consumers) {
			for _, g := range []string{GroupOf(c, Reader), GroupOf(c, Editor), GroupOf(c, "pipelines")} {
				if !slices.Contains(byTable[pr.Table], g) {
					byTable[pr.Table] = append(byTable[pr.Table], g)
				}
				if !slices.Contains(all, g) {
					all = append(all, g)
				}
			}
		}
	}
	return byTable, all
}

// ProductPolicies let each product's consumers read its table, and only it,
// as the producer's readers do (the producer's masks and row filters on it
// hold for them: tablePolicies adds them to those policies, as Ranger keeps
// one policy to a resource). These are its tables' own read policies, where
// the producer has none for that table alone.
func (p *Project) ProductPolicies() []RangerPolicy {
	t := p.Spec.Tables
	if t == nil {
		return nil
	}
	byTable, _ := p.consumerGroups()
	var listed []string
	if t.Readers != nil {
		listed = t.Readers.Tables
	}
	desc := "From the project " + p.Metadata.Name + ": a data product's consumers, which the platform operator keeps."
	var out []RangerPolicy
	for _, pr := range p.Spec.Products {
		groups := byTable[pr.Table]
		if len(groups) == 0 || slices.Contains(listed, pr.Table) || slices.ContainsFunc(out, func(x RangerPolicy) bool {
			return x.Name == p.RangerPrefix()+"product-table:"+pr.Table
		}) {
			continue
		}
		var items []RangerItem
		for _, g := range groups {
			items = append(items, allow(g, "select", "show"))
		}
		out = append(out, RangerPolicy{Name: p.RangerPrefix() + "product-table:" + pr.Table, Description: desc,
			Resources:   atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", pr.Table, "column", "*")),
			PolicyItems: items})
	}
	return out
}

// KeepProductState keeps what only the platform sets on a project's
// products (who consumes each, its certification) as they were.
func KeepProductState(before, p *Project) {
	prev := map[string]Product{}
	if before != nil {
		for _, pr := range before.Spec.Products {
			prev[pr.Name] = pr
		}
	}
	for i := range p.Spec.Products {
		pr := &p.Spec.Products[i]
		old := prev[pr.Name]
		pr.Consumers, pr.Certification = slices.Clone(old.Consumers), old.Certification
	}
}

// ProductOf is the project's product of that name, or nil.
func (p *Project) ProductOf(name string) *Product {
	for i := range p.Spec.Products {
		if p.Spec.Products[i].Name == name {
			return &p.Spec.Products[i]
		}
	}
	return nil
}

func (pr Product) String() string {
	return fmt.Sprintf("%s (%s, version %s)", pr.Name, pr.Table, pr.Version)
}
