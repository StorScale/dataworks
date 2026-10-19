package project

import (
	"strings"
	"testing"
	"time"
)

const companyJSON = `{"metadata":{"name":"company"},"spec":{"members":[{"user":"bob","role":"editor"}],
	"tables":{"readers":{"masks":[{"table":"bookings","column":"amount","type":"MASK_NULL"},{"table":"customers","column":"segment","type":"MASK"}],
	"rowFilters":[{"table":"bookings","filter":"amount < 2000"}]}},
	"products":[{"name":"bookings","table":"bookings","owner":"bob","version":"1","contract":{"columns":["booking_id","amount"],"within":"24h"}}]}}`

func TestProducts(t *testing.T) {
	p, err := Parse([]byte(companyJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ old, new, want string }{
		{`"owner":"bob"`, `"owner":"bob bob"`, "its owner is a person"},
		{`"version":"1"`, `"version":""`, "a version"},
		{`"within":"24h"`, `"within":"soon"`, "within"},
		{`"columns":["booking_id","amount"]`, `"columns":["Booking Id"]`, "isn't a column's name"},
		{`"version":"1"`, `"version":"1","consumers":["company"]`, "isn't another project's name"},
	} {
		_, err := Parse([]byte(strings.Replace(companyJSON, c.old, c.new, 1)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.new, err, c.want)
		}
	}

	// Consumers and certification are the platform's: a save keeps them as they were.
	before, _ := Parse([]byte(companyJSON))
	before.Spec.Products[0].Consumers = []string{"campaigns"}
	before.Spec.Products[0].Certification = &Certification{By: "priya", At: time.Now(), Hash: before.Spec.Products[0].PromiseHash()}
	forged, _ := Parse([]byte(strings.Replace(companyJSON, `"version":"1"`, `"version":"1","consumers":["eve"]`, 1)))
	KeepProductState(before, forged)
	if pr := forged.Spec.Products[0]; len(pr.Consumers) != 1 || pr.Consumers[0] != "campaigns" || !pr.Certified() {
		t.Errorf("kept: %+v", pr)
	}
	// A contract change: no longer certified.
	changed, _ := Parse([]byte(strings.Replace(companyJSON, `"booking_id","amount"`, `"booking_id"`, 1)))
	KeepProductState(before, changed)
	if changed.Spec.Products[0].Certified() {
		t.Error("certified after its contract changed")
	}

	// Its consumers read its table, under the producer's mask and row filter on it, and nothing else.
	p.Spec.Products[0].Consumers = []string{"campaigns"}
	var names []string
	for _, pol := range p.ProductPolicies() {
		names = append(names, pol.Name)
		if pol.Name == "project:company:product:bookings" {
			if g := pol.PolicyItems; len(g) != 3 || g[0].Groups[0] != "campaigns-readers" || g[1].Groups[0] != "campaigns-editors" || g[2].Groups[0] != "campaigns-pipelines" {
				t.Errorf("the table's readers: %+v", g)
			}
		}
		if pol.PolicyType == 2 && pol.RowFilterPolicyItems[0].RowFilterInfo.FilterExpr != "amount < 2000" {
			t.Errorf("filter: %+v", pol.RowFilterPolicyItems[0])
		}
	}
	want := "project:company:product:bookings:namespace,project:company:product:bookings,project:company:product:bookings:mask:amount,project:company:product:bookings:filter"
	if strings.Join(names, ",") != want {
		t.Errorf("policies:\n %s\nwant\n %s", strings.Join(names, ","), want)
	}
	p.Spec.Products[0].Consumers = nil
	if got := p.ProductPolicies(); len(got) != 0 {
		t.Errorf("no consumers, no policies: %v", got)
	}
}
