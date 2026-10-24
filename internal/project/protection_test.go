package project

import (
	"strings"
	"testing"
)

func TestProtectionPolicies(t *testing.T) {
	p, err := Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{"readers":{"masks":[{"table":"orders","column":"card_number","type":"MASK_SHOW_LAST_4"}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := ParseProtection(`{"personal": {"mask": "MASK_HASH", "except": ["data-protection"]}, "salary": {"mask": "MASK_NULL"}}`)
	if err != nil {
		t.Fatal(err)
	}
	cols := []ProtectedColumn{
		{Table: "customers", Column: "email", Tag: "personal", State: "suggested", Automated: true, Type: "varchar"},
		{Table: "customers", Column: "customer_id", Tag: "personal", State: "confirmed", Declared: true, Type: "int"},
		{Table: "staff", Column: "pay", Tag: "salary", State: "confirmed"},
		{Table: "staff", Column: "pay", Tag: "personal", State: "confirmed"},  // a column tagged twice: the stronger rule (health, financial, personal; then others) wins
		{Table: "notes", Column: "text", Tag: "mystery", State: "confirmed"},  // no rule: ignored
		{Table: "bad name", Column: "x", Tag: "personal", State: "confirmed"}, // not an identifier: ignored
	}
	pols := p.ProtectionPolicies(cols, rules, true)
	names := []string{}
	for _, pl := range pols {
		names = append(names, pl.Name)
	}
	if got := strings.Join(names, " "); got != "project:sales:protect:customers.customer_id project:sales:protect:customers.email project:sales:protect:staff.pay" {
		t.Errorf("policies: %s", got)
	}
	email := pols[1]
	items := email.DataMaskPolicyItems
	clear := []string{}
	for _, it := range items[:len(items)-1] {
		if it.DataMaskInfo.DataMaskType != "MASK_NONE" {
			t.Errorf("not clear: %+v", it)
		}
		clear = append(clear, append(it.Groups, it.Users...)...)
	}
	last := items[len(items)-1]
	if email.PolicyType != 1 || strings.Join(clear, " ") != "sales-editors data-protection maintenance openmetadata" ||
		last.Groups[0] != "public" || last.DataMaskInfo.DataMaskType != "CUSTOM" || !strings.Contains(last.DataMaskInfo.ValueExpr, "sha256") {
		t.Errorf("email's policy: %s; %+v", strings.Join(clear, " "), last)
	}
	if tb := email.Resources["table"].Values; len(tb) != 3 || tb[0] != "customers" || email.Resources["column"].Values[0] != "email" {
		t.Errorf("email's resources: %+v", email.Resources)
	}
	// An int hashed to an int: its nulls, duplicates and joins kept, its values not shown.
	if id := pols[0].DataMaskPolicyItems[len(pols[0].DataMaskPolicyItems)-1].DataMaskInfo; id.DataMaskType != "CUSTOM" || !strings.Contains(id.ValueExpr, "from_big_endian_32") {
		t.Errorf("customer_id's policy: %+v", id)
	}
	if pay := pols[2]; pay.DataMaskPolicyItems[len(pay.DataMaskPolicyItems)-1].DataMaskInfo.DataMaskType != "CUSTOM" { // personal outranks a tag the install added
		t.Errorf("pay's policy: %+v", pay.DataMaskPolicyItems)
	}
	// Suggested tags left out when the install says so.
	// With pipelines, they see it as it is too (they write the tables).
	p.Spec.Pipelines = &Pipelines{ServiceAccount: "airflow-pipelines"}
	withPipes := p.ProtectionPolicies(cols, rules, true)[1].DataMaskPolicyItems
	if withPipes[1].Groups[0] != "sales-pipelines" {
		t.Errorf("pipelines not clear: %+v", withPipes)
	}
	p.Spec.Pipelines = nil
	if pols := p.ProtectionPolicies(cols, rules, false); len(pols) != 2 {
		t.Errorf("without suggested: %d policies", len(pols))
	}
	// The install's rules over the defaults.
	if d, _ := ParseProtection(""); d["personal"].Mask != "MASK_HASH" || d["health"].Mask != "MASK_NULL" || d["financial"].Mask != "MASK" {
		t.Errorf("defaults: %v", d)
	}
	for _, bad := range []string{`{"personal": {"mask": "BLUR"}}`, `{"Bad Tag": {}}`, `nope`} {
		if _, err := ParseProtection(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// A column the project masks itself and the catalog tags: one policy, the
// tag's, with the project's readers masked as the project says, but never not at all.
func TestProtectionOverAProjectsOwnMask(t *testing.T) {
	p, err := Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"},{"group":"analysts","role":"reader"}],
		"tables":{"readers":{"masks":[{"table":"orders","column":"card_number","type":"MASK_SHOW_LAST_4"},{"table":"orders","column":"note","type":"MASK_NONE"}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cols := []ProtectedColumn{{Table: "orders", Column: "card_number", Tag: "personal", State: "confirmed", Type: "varchar"},
		{Table: "orders", Column: "note", Tag: "financial", State: "confirmed", Type: "varchar"}}
	protection := p.ProtectionPolicies(cols, DefaultProtection(), true)
	if len(protection) != 2 {
		t.Fatalf("policies: %+v", protection)
	}
	masks := func(pl RangerPolicy) map[string]string {
		out := map[string]string{}
		for _, it := range pl.DataMaskPolicyItems {
			out[strings.Join(append(it.Groups, it.Users...), ",")] = it.DataMaskInfo.DataMaskType
		}
		return out
	}
	card, note := masks(protection[0]), masks(protection[1])
	if card["sales-readers"] != "MASK_SHOW_LAST_4" || card["public"] != "CUSTOM" || card["sales-editors"] != "MASK_NONE" { // its readers the last four; everyone else the tag's hash
		t.Errorf("card_number: %v", card)
	}
	if note["sales-readers"] != "MASK" || note["public"] != "MASK" { // the project's none doesn't lift financial's mask
		t.Errorf("note: %v", note)
	}
	for _, pl := range DropCoveredMasks(p.RangerPrefix(), p.RangerPolicies(), protection) {
		if strings.Contains(pl.Name, ":mask:orders.") {
			t.Errorf("the project's own mask kept beside the tag's: %s", pl.Name)
		}
	}
}
