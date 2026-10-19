package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/llm"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

// The LLM gateway (use case 12.1): LiteLLM (or its stand-in, llm_stub), in
// front of the company's models, with a key per project and per agent, each
// with a budget. The operator makes the keys from the spec (spec.llm.budget,
// agents[].budget) and keeps them as the project's connections (<project>-llm,
// and the agent's own), where its pipelines, notebooks and agents read them;
// a project or agent gone, or its budget taken away, loses its key.

// applyLLM keeps the project's gateway keys as the spec says: a key for the
// project when it has a budget, one for each approved agent with a budget,
// each kept as a connection; keys for budgets gone are deleted.
func (r *Reconciler) applyLLM(ctx context.Context, p *project.Project) error {
	if r.Gateway == nil || r.Store == nil {
		return nil
	}
	name := p.Metadata.Name
	conns, err := r.Store.Connections(ctx, name)
	if err != nil {
		return err
	}
	have := map[string]store.StoredConnection{}
	for _, c := range conns {
		have[c.Name] = c
	}
	want := map[string]bool{} // aliases that should have a key
	var errs []error
	ensure := func(connName, alias string, budget float64, meta map[string]any, into map[string]string) {
		want[alias] = true
		c, ok := have[connName]
		if ok && c.Data["llmKey"] != "" && c.Data["llmBudget"] == fmt.Sprint(budget) {
			return
		}
		if ok && c.Data["llmKey"] != "" { // the budget changed: a new key, the old one gone
			r.Gateway.Delete(ctx, c.Data["llmKey"])
		}
		key, err := r.Gateway.Generate(ctx, alias, budget, meta)
		if err != nil {
			errs = append(errs, err)
			return
		}
		data := map[string]string{}
		for k, v := range c.Data {
			data[k] = v
		}
		for k, v := range into {
			data[k] = v
		}
		data["llmKey"], data["llmUrl"], data["llmBudget"] = key, r.Gateway.URL, fmt.Sprint(budget)
		cm := c.ConnectionMeta
		if !ok {
			cm = store.ConnectionMeta{Name: connName, Kind: "llm", AddedBy: "platform-operator", AddedAt: time.Now().UTC()}
		}
		cm.ChangedBy, cm.ChangedAt = "platform-operator", time.Now().UTC()
		if err := r.Store.PutConnection(ctx, name, store.StoredConnection{ConnectionMeta: cm, Data: data}); err != nil {
			errs = append(errs, fmt.Errorf("keeping the key as %s: %w", connName, err))
		}
	}
	if l := p.Spec.LLM; l != nil && l.Budget > 0 {
		ensure(name+"-llm", llm.KeyAlias(name, ""), l.Budget, map[string]any{"project": name}, nil)
	} else if c, ok := have[name+"-llm"]; ok && c.Kind == "llm" {
		r.Gateway.Delete(ctx, c.Data["llmKey"])
		r.Store.DeleteConnection(ctx, name, c.Name)
	}
	for _, a := range p.Spec.Agents {
		connName := a.ConnectionName(name)
		if a.Approved() && a.Budget > 0 {
			if _, ok := have[connName]; ok { // approved: its secret is kept there; the key joins it
				ensure(connName, llm.KeyAlias(name, a.Name), a.Budget, map[string]any{"project": name, "agent": a.Name, "owner": a.Owner}, nil)
			}
		} else if c, ok := have[connName]; ok && c.Data["llmKey"] != "" {
			r.Gateway.Delete(ctx, c.Data["llmKey"])
			delete(c.Data, "llmKey")
			delete(c.Data, "llmUrl")
			delete(c.Data, "llmBudget")
			r.Store.PutConnection(ctx, name, c)
		}
	}
	// Keys of the project's at the gateway that nothing wants any more (an
	// agent revoked, whose connection platformd removed).
	if err := r.sweepLLMKeys(ctx, name, want); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// sweepLLMKeys deletes the project's keys at the gateway but those wanted.
func (r *Reconciler) sweepLLMKeys(ctx context.Context, name string, want map[string]bool) error {
	keys, err := r.Gateway.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, k := range keys {
		if (strings.HasPrefix(k.Alias, "storscale/"+name+"/") || k.Alias == "storscale/"+name) && !want[k.Alias] {
			errs = append(errs, r.Gateway.Delete(ctx, k.Key))
		}
	}
	return errors.Join(errs...)
}

// removeLLM deletes a project's keys at the gateway, the project gone.
func (r *Reconciler) removeLLM(ctx context.Context, p *project.Project) error {
	if r.Gateway == nil {
		return nil
	}
	return r.sweepLLMKeys(ctx, p.Metadata.Name, nil)
}
