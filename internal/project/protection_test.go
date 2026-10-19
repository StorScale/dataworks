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
	if email.PolicyType != 1 || strings.Join(clear, " ") != "sales-editors data-protection maintenance" ||
		last.Groups[0] != "public" || last.DataMaskInfo.DataMaskType != "CUSTOM" || !strings.Contains(last.DataMaskInfo.ValueExpr, "sha256") {
		t.Errorf("email's policy: %s; %+v", strings.Join(clear, " "), last)
	}
	if tb := email.Resources["table"].Values; len(tb) != 3 || tb[0] != "customers" || email.Resources["column"].Values[0] != "email" {
		t.Errorf("email's resources: %+v", email.Resources)
	}
	if id := pols[0]; id.DataMaskPolicyItems[len(id.DataMaskPolicyItems)-1].DataMaskInfo.DataMaskType != "MASK_NULL" { // a hash can't be an int: nulled
		t.Errorf("customer_id's policy: %+v", id.DataMaskPolicyItems)
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
