package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGateway is the stand-in's key API (llm_stub), as far as the client uses it.
func fakeGateway(t *testing.T, master string) (*httptest.Server, map[string]map[string]any) {
	t.Helper()
	keys := map[string]map[string]any{}
	n := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+master {
			http.Error(w, `{"error": {"message": "Authentication Error: not the master key"}}`, http.StatusUnauthorized)
			return
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		switch r.Method + " " + r.URL.Path {
		case "POST /key/generate":
			n++
			key := "sk-" + strings.Repeat("a", n)
			keys[key] = map[string]any{"key_alias": in["key_alias"], "max_budget": in["max_budget"], "spend": 0.25, "metadata": in["metadata"]}
			json.NewEncoder(w).Encode(map[string]any{"key": key})
		case "POST /key/delete":
			for _, k := range in["keys"].([]any) {
				delete(keys, k.(string))
			}
			w.Write([]byte(`{}`))
		case "GET /key/info":
			info, ok := keys[r.URL.Query().Get("key")]
			if !ok {
				http.Error(w, `{"error": {"message": "key not found"}}`, http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"info": info})
		case "GET /key/list":
			var out []map[string]any
			for k, v := range keys {
				row := map[string]any{"key": k}
				for kk, vv := range v {
					row[kk] = vv
				}
				out = append(out, row)
			}
			json.NewEncoder(w).Encode(map[string]any{"keys": out})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s, keys
}

func TestGateway(t *testing.T) {
	ctx := context.Background()
	srv, keys := fakeGateway(t, "sk-master")
	g := &Gateway{URL: srv.URL + "/", MasterKey: "sk-master"}

	key, err := g.Generate(ctx, KeyAlias("sales", ""), 5, map[string]any{"project": "sales"})
	if err != nil || key != "sk-a" {
		t.Fatalf("generate: %q %v", key, err)
	}
	if keys[key]["key_alias"] != "storscale/sales" || keys[key]["max_budget"] != 5.0 {
		t.Fatalf("the key at the gateway: %v", keys[key])
	}
	info, err := g.Info(ctx, key)
	if err != nil || info.Key != key || info.Alias != "storscale/sales" || info.Spend != 0.25 || info.MaxBudget == nil || *info.MaxBudget != 5 {
		t.Fatalf("info: %+v %v", info, err)
	}
	if _, err := g.Info(ctx, "sk-nothing"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("a key that isn't: %v", err)
	}
	agent, _ := g.Generate(ctx, KeyAlias("sales", "nightly"), 1, nil)
	list, err := g.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if err := g.Delete(ctx, key, agent); err != nil || len(keys) != 0 {
		t.Fatalf("delete: %v, left %v", err, keys)
	}
	if err := g.Delete(ctx); err != nil {
		t.Fatalf("deleting nothing: %v", err)
	}

	wrong := &Gateway{URL: srv.URL, MasterKey: "sk-other"}
	if _, err := wrong.Generate(ctx, "x", 1, nil); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("the wrong master key: %v", err)
	}
	if KeyAlias("sales", "nightly") != "storscale/sales/agent/nightly" {
		t.Fatal(KeyAlias("sales", "nightly"))
	}
}
