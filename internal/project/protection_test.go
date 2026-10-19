package project

import (
	"strings"
	"testing"
)

func TestProtectionPolicies(t *testing.T) {
	p, err := Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := ParseProtection(`{"personal": {"mask": "MASK_HASH", "except": ["data-protection"]}, "salary": {"mask": "MASK_NULL"}}`)
	if err != nil {
		t.Fatal(err)
	}
	cols := []ProtectedColumn{
		{Table: "customers", Column: "email", Tag: "personal", State: "suggested", Automated: true},
		{Table: "customers", Column: "customer_id", Tag: "personal", State: "confirmed", Declared: true},
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
	if email.PolicyType != 1 || len(email.DataMaskPolicyItems) != 2 ||
		email.DataMaskPolicyItems[0].Groups[0] != "data-protection" || email.DataMaskPolicyItems[0].DataMaskInfo.DataMaskType != "MASK_NONE" ||
		email.DataMaskPolicyItems[1].Groups[0] != "public" || email.DataMaskPolicyItems[1].DataMaskInfo.DataMaskType != "MASK_HASH" {
		t.Errorf("email's policy: %+v", email)
	}
	if tb := email.Resources["table"].Values; len(tb) != 3 || tb[0] != "customers" || email.Resources["column"].Values[0] != "email" {
		t.Errorf("email's resources: %+v", email.Resources)
	}
	if pay := pols[2]; pay.DataMaskPolicyItems[len(pay.DataMaskPolicyItems)-1].DataMaskInfo.DataMaskType != "MASK_HASH" { // personal outranks a tag the install added
		t.Errorf("pay's policy: %+v", pay.DataMaskPolicyItems)
	}
	// Suggested tags left out when the install says so.
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
