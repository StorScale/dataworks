package project

import (
	"strings"
	"testing"
	"time"
)

// A takeover is the platform's to record: a save can't claim one, nor lose
// one; a table taken over is the project's own (no longer read-only), and
// may then have a retention.
func TestATakeoverIsKeptAndOnlyThePlatformsToSet(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	before := &Project{}
	before.Spec.Tables = &Tables{Namespace: "retail", Adopt: []Adoption{
		{Table: "orders", From: "lakehouse.sales.orders", TakenOver: at, TakenOverBy: "bob"},
		{Table: "custs", From: "lakehouse.sales.custs"}}}

	saved := &Project{}
	saved.Spec.Tables = &Tables{Namespace: "retail", Adopt: []Adoption{
		{Table: "orders", From: "lakehouse.sales.orders"},                                  // the save left it out
		{Table: "custs", From: "lakehouse.sales.custs", TakenOver: at, TakenOverBy: "eve"}, // a claim
		{Table: "items", From: "lakehouse.sales.items2"}}}
	KeepAdoptionState(before, saved)
	tb := saved.Spec.Tables
	if !tb.TakenOver("orders") || tb.Adoption("orders").TakenOverBy != "bob" || tb.Adopted("orders") {
		t.Errorf("orders' takeover was lost: %+v", tb.Adoption("orders"))
	}
	if tb.TakenOver("custs") || !tb.Adopted("custs") {
		t.Errorf("a save claimed custs' takeover: %+v", tb.Adoption("custs"))
	}

	// Adopted from somewhere else now: a new adoption, not taken over.
	moved := &Project{}
	moved.Spec.Tables = &Tables{Namespace: "retail", Adopt: []Adoption{{Table: "orders", From: "lakehouse.archive.orders"}}}
	KeepAdoptionState(before, moved)
	if moved.Spec.Tables.TakenOver("orders") {
		t.Error("an adoption from another table kept the old one's takeover")
	}

	// A retention: refused on a table still followed, allowed on one taken over.
	var problems []string
	bad := func(f string, a ...any) { problems = append(problems, f) }
	tb.Retention = []Retention{{Table: "custs", Column: "at", Keep: "30d"}, {Table: "orders", Column: "at", Keep: "30d"}}
	saved.checkAdopt(bad)
	if len(problems) != 1 || !strings.Contains(problems[0], "take it over first") {
		t.Errorf("retention problems: %v", problems)
	}
}
