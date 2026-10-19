package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/StorScale/storscale-platform/internal/llm"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// connStore is the store, in memory: a project's connections.
type connStore struct {
	store.Store
	conns map[string]store.StoredConnection
}

func (c *connStore) Connections(_ context.Context, _ string) ([]store.StoredConnection, error) {
	var out []store.StoredConnection
	for _, x := range c.conns {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (c *connStore) PutConnection(_ context.Context, _ string, x store.StoredConnection) error {
	c.conns[x.Name] = x
	return nil
}
func (c *connStore) DeleteConnection(_ context.Context, _, name string) error {
	delete(c.conns, name)
	return nil
}

// fakeLLM is the gateway's key API (llm_stub's).
type fakeLLM struct {
	mu   sync.Mutex
	n    int
	keys map[string]map[string]any // key -> alias, budget
}

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer sk-master" {
		http.Error(w, `{"error": "not the master key"}`, http.StatusUnauthorized)
		return
	}
	var in map[string]any
	json.NewDecoder(r.Body).Decode(&in)
	switch r.Method + " " + r.URL.Path {
	case "POST /key/generate":
		f.n++
		key := fmt.Sprintf("sk-%d", f.n)
		f.keys[key] = map[string]any{"key_alias": in["key_alias"], "max_budget": in["max_budget"], "metadata": in["metadata"]}
		json.NewEncoder(w).Encode(map[string]any{"key": key})
	case "POST /key/delete":
		for _, k := range in["keys"].([]any) {
			delete(f.keys, k.(string))
		}
		w.Write([]byte(`{}`))
	case "GET /key/list":
		var out []map[string]any
		for k, v := range f.keys {
			out = append(out, map[string]any{"key": k, "key_alias": v["key_alias"], "max_budget": v["max_budget"]})
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": out})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeLLM) aliases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, v := range f.keys {
		out = append(out, v["key_alias"].(string))
	}
	sort.Strings(out)
	return out
}

func TestApplyLLM(t *testing.T) {
	ctx := context.Background()
	gw := &fakeLLM{keys: map[string]map[string]any{
		"sk-old":   {"key_alias": "storscale/sales/agent/old", "max_budget": 1.0}, // an agent revoked before
		"sk-other": {"key_alias": "storscale/other", "max_budget": 1.0},           // another project's: not ours to touch
	}}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	st := &connStore{conns: map[string]store.StoredConnection{
		"sales-agent-nightly": {ConnectionMeta: store.ConnectionMeta{Name: "sales-agent-nightly", Kind: "agent"}, Data: map[string]string{"clientSecret": "s3cret"}},
	}}
	r := &Reconciler{Store: st, Gateway: &llm.Gateway{URL: srv.URL, MasterKey: "sk-master"}}
	p := &project.Project{Metadata: project.Metadata{Name: "sales"}, Spec: project.Spec{
		LLM: &project.LLM{Budget: 5},
		Agents: []project.Agent{
			{Name: "nightly", Owner: "bob", Budget: 1, ApprovedBy: "alice", ApprovedAt: time.Now()},
			{Name: "draft", Owner: "bob", Budget: 1},                                  // not approved: no key yet
			{Name: "free", Owner: "bob", ApprovedBy: "alice", ApprovedAt: time.Now()}, // no budget: no key
		},
	}}

	if err := r.applyLLM(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(gw.aliases()), "[storscale/other storscale/sales storscale/sales/agent/nightly]"; got != want {
		t.Fatalf("keys at the gateway: %s", got)
	}
	pk := st.conns["sales-llm"]
	if pk.Kind != "llm" || pk.AddedBy != "platform-operator" || pk.Data["llmKey"] == "" || pk.Data["llmUrl"] != srv.URL || pk.Data["llmBudget"] != "5" {
		t.Fatalf("the project's key as a connection: %+v", pk)
	}
	ak := st.conns["sales-agent-nightly"]
	if ak.Kind != "agent" || ak.Data["clientSecret"] != "s3cret" || ak.Data["llmKey"] == "" || ak.Data["llmBudget"] != "1" {
		t.Fatalf("the agent's key joins its secret: %+v", ak)
	}
	if _, ok := st.conns["sales-agent-draft"]; ok {
		t.Fatal("an unapproved agent got a connection")
	}
	first := pk.Data["llmKey"]

	// Nothing changed: the keys stay.
	if err := r.applyLLM(ctx, p); err != nil || st.conns["sales-llm"].Data["llmKey"] != first || gw.n != 2 {
		t.Fatalf("a second pass: %v, key %s, %d made", err, st.conns["sales-llm"].Data["llmKey"], gw.n)
	}

	// The budget changed: a new key, the old one gone.
	p.Spec.LLM.Budget = 7
	if err := r.applyLLM(ctx, p); err != nil {
		t.Fatal(err)
	}
	if k := st.conns["sales-llm"].Data; k["llmKey"] == first || k["llmBudget"] != "7" || gw.n != 3 || len(gw.keys) != 3 {
		t.Fatalf("the budget changed: %v, %d made, %d kept", k, gw.n, len(gw.keys))
	}

	// Budgets gone: the project's connection goes, the agent's loses its key.
	p.Spec.LLM = nil
	p.Spec.Agents[0].Budget = 0
	if err := r.applyLLM(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.conns["sales-llm"]; ok || st.conns["sales-agent-nightly"].Data["llmKey"] != "" || st.conns["sales-agent-nightly"].Data["clientSecret"] != "s3cret" {
		t.Fatalf("budgets gone: %+v", st.conns)
	}
	if got := fmt.Sprint(gw.aliases()); got != "[storscale/other]" {
		t.Fatalf("keys left: %s", got)
	}

	// The project gone: whatever keys it has left go, the other project's stay.
	gw.keys["sk-late"] = map[string]any{"key_alias": "storscale/sales", "max_budget": 1.0}
	if err := r.removeLLM(ctx, p); err != nil || fmt.Sprint(gw.aliases()) != "[storscale/other]" {
		t.Fatalf("removed: %v %v", err, gw.aliases())
	}
	if err := (&Reconciler{}).removeLLM(ctx, p); err != nil {
		t.Fatalf("no gateway: %v", err)
	}
}
