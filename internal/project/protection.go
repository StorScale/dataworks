package project

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Protection by tag (use case 12.5): the catalog classifies a column
// (personal, financial, health: the Protection classification's tags, by
// detection or declaration), catalog-sync writes what's tagged to the
// platform's bucket, and the operator turns it into Ranger's masks here, as
// it does for a project's own readers.masks. What a tag means is the
// install's: ProtectionRules.

// A ProtectedColumn is one the catalog has tagged.
type ProtectedColumn struct {
	Table     string `json:"table"`
	Column    string `json:"column"`
	Tag       string `json:"tag"`                 // personal, financial, health
	State     string `json:"state"`               // suggested, confirmed
	Declared  bool   `json:"declared,omitempty"`  // by the project (tables.people)
	Automated bool   `json:"automated,omitempty"` // found by the catalog, not a person
}

// A Classification is what catalog-sync wrote for a project.
type Classification struct {
	Project string            `json:"project"`
	At      int64             `json:"at"`
	Columns []ProtectedColumn `json:"columns"`
}

// A ProtectionRule is what a tag means: the mask Ranger applies, and the
// groups that see the column as it is.
type ProtectionRule struct {
	Mask   string   `json:"mask"`             // one of MaskTypes
	Except []string `json:"except,omitempty"` // groups that see it unmasked
}

// ProtectionRules are the install's, by tag.
type ProtectionRules map[string]ProtectionRule

// DefaultProtection is what the tags mean unless the install says otherwise:
// personal hashed, financial masked, health nulled, for everyone.
func DefaultProtection() ProtectionRules {
	return ProtectionRules{"personal": {Mask: "MASK_HASH"}, "financial": {Mask: "MASK"}, "health": {Mask: "MASK_NULL"}}
}

// DefaultProtectionTags are the tags the catalog's Protection classification has.
func DefaultProtectionTags() []string { return []string{"personal", "financial", "health"} }

// ParseProtection reads the install's rules (JSON, as PROTECTION_TAGS gives
// them) over the defaults: a tag given replaces the default's rule.
func ParseProtection(text string) (ProtectionRules, error) {
	rules := DefaultProtection()
	if strings.TrimSpace(text) == "" {
		return rules, nil
	}
	var given ProtectionRules
	if err := json.Unmarshal([]byte(text), &given); err != nil {
		return nil, fmt.Errorf("PROTECTION_TAGS: %v", err)
	}
	for tag, rule := range given {
		if !identifierRE.MatchString(tag) {
			return nil, fmt.Errorf("PROTECTION_TAGS: %q isn't a tag's name", tag)
		}
		if rule.Mask == "" {
			rule.Mask = "MASK"
		}
		if !slices.Contains(MaskTypes, rule.Mask) {
			return nil, fmt.Errorf("PROTECTION_TAGS: %s: mask %q isn't one of %s", tag, rule.Mask, strings.Join(MaskTypes, ", "))
		}
		rules[tag] = rule
	}
	return rules, nil
}

// ProtectionPolicies are the mask policies for a project's tagged columns:
// one per column, masking it for everyone (Ranger's public group) but the
// rule's excepted groups, who come first and see it as it is. A suggested
// tag counts when applySuggested says so; a tag with no rule is ignored.
func (p *Project) ProtectionPolicies(cols []ProtectedColumn, rules ProtectionRules, applySuggested bool) []RangerPolicy {
	t := p.Spec.Tables
	if t == nil {
		return nil
	}
	byColumn := map[string]ProtectedColumn{} // the strongest tag per column: the first rule in tag order wins
	order := map[string]int{}
	for i, tag := range []string{"health", "financial", "personal"} {
		order[tag] = i
	}
	for _, c := range cols {
		if _, ok := rules[c.Tag]; !ok || !identifierRE.MatchString(c.Table) || !identifierRE.MatchString(c.Column) {
			continue
		}
		if c.State != "confirmed" && !(c.State == "suggested" && applySuggested) {
			continue
		}
		key := c.Table + "." + c.Column
		if have, ok := byColumn[key]; ok && rank(order, have.Tag) <= rank(order, c.Tag) {
			continue
		}
		byColumn[key] = c
	}
	keys := make([]string, 0, len(byColumn))
	for k := range byColumn {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []RangerPolicy
	for _, key := range keys {
		c := byColumn[key]
		rule := rules[c.Tag]
		var items []RangerItem
		for _, g := range sorted(rule.Except) {
			it := allow(g, "select")
			it.DataMaskInfo = &RangerMask{DataMaskType: "MASK_NONE"}
			items = append(items, it)
		}
		it := allow("public", "select")
		it.DataMaskInfo = maskInfo(rule.Mask)
		items = append(items, it)
		out = append(out, RangerPolicy{Name: p.RangerPrefix() + "protect:" + c.Table + "." + c.Column, PolicyType: 1,
			Description:         fmt.Sprintf("The project %s's %s.%s is %s (%s in the catalog): masked for everyone but %s.", p.Metadata.Name, c.Table, c.Column, c.Tag, c.State, exceptWords(rule.Except)),
			Resources:           atAnyRef(resources("catalog", t.Catalog, "schema", t.Namespace, "table", c.Table, "column", c.Column)),
			DataMaskPolicyItems: items})
	}
	return out
}

func rank(order map[string]int, tag string) int {
	if r, ok := order[tag]; ok {
		return r
	}
	return len(order)
}

func exceptWords(groups []string) string {
	if len(groups) == 0 {
		return "no one"
	}
	return strings.Join(sorted(groups), ", ")
}
