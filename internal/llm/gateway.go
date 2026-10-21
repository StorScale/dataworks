// Package llm is the LLM gateway's key API (use case 12.1), as LiteLLM has
// it and its stand-in (llm_stub) plays it: keys with budgets, made by the
// operator for projects and agents, their spend read by platformd.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Gateway is the gateway's key API, as LiteLLM has it.
type Gateway struct {
	URL       string
	MasterKey string
	HTTP      *http.Client
}

// GatewayKey is a key as the gateway lists it.
type GatewayKey struct {
	Key       string         `json:"key"`   // the stand-in lists the key itself
	Token     string         `json:"token"` // LiteLLM lists its hash, which its key API takes in the key's place
	Alias     string         `json:"key_alias"`
	MaxBudget *float64       `json:"max_budget"`
	Spend     float64        `json:"spend"`
	Metadata  map[string]any `json:"metadata"`
}

func (g *Gateway) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.MasterKey)
	req.Header.Set("Content-Type", "application/json")
	h := g.HTTP
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return fmt.Errorf("the LLM gateway: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("the LLM gateway: %s %s: HTTP %d %s", method, path, res.StatusCode, string(data[:min(len(data), 200)]))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Generate makes a key with a monthly budget (USD) and an alias naming its owner.
func (g *Gateway) Generate(ctx context.Context, alias string, budget float64, meta map[string]any) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	err := g.call(ctx, "POST", "/key/generate", map[string]any{"key_alias": alias, "max_budget": budget, "budget_duration": "30d", "metadata": meta}, &out)
	if err == nil && out.Key == "" {
		err = errors.New("the LLM gateway gave no key")
	}
	return out.Key, err
}

// Delete removes keys.
func (g *Gateway) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return g.call(ctx, "POST", "/key/delete", map[string]any{"keys": keys}, nil)
}

// Info is a key's budget and spend.
func (g *Gateway) Info(ctx context.Context, key string) (*GatewayKey, error) {
	var out struct {
		Info GatewayKey `json:"info"`
	}
	if err := g.call(ctx, "GET", "/key/info?key="+url.QueryEscape(key), nil, &out); err != nil {
		return nil, err
	}
	out.Info.Key = key
	return &out.Info, nil
}

// ID is what names the key to the gateway's key API: the key, or LiteLLM's hash of it.
func (k GatewayKey) ID() string {
	if k.Key != "" {
		return k.Key
	}
	return k.Token
}

// List is every key the gateway has (the stand-in's and LiteLLM's /key/list):
// the whole objects, a page at a time (LiteLLM pages its list, 100 at most).
func (g *Gateway) List(ctx context.Context) ([]GatewayKey, error) {
	var all []GatewayKey
	for page := 1; page < 1000; page++ {
		var out struct {
			Keys       []GatewayKey `json:"keys"`
			TotalPages int          `json:"total_pages"`
		}
		if err := g.call(ctx, "GET", fmt.Sprintf("/key/list?return_full_object=true&size=100&page=%d", page), nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Keys...)
		if page >= out.TotalPages || len(out.Keys) == 0 {
			break
		}
	}
	return all, nil
}

// KeyAlias names a project's key, or an agent's, at the gateway.
func KeyAlias(projectName, agent string) string {
	if agent == "" {
		return "storscale/" + projectName
	}
	return "storscale/" + projectName + "/agent/" + agent
}
