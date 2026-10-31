package reconcile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A project's sweep of its agents' or service accounts' clients removes its
// own that aren't kept, and never another project's whose name starts with
// its own (sales-eu beside sales: svc-sales-eu-… starts as svc-sales-… does).
func TestAClientSweepLeavesALongerNamedProjectsClients(t *testing.T) {
	var mu sync.Mutex
	clients := map[string]string{ // id -> clientId
		"1": "svc-sales-crm", "2": "svc-sales-old", "3": "svc-sales-eu-crm", "4": "agent-sales-bot", "5": "svc-salesforce",
	}
	kc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
			w.Write([]byte(`{"access_token":"t","expires_in":300}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/clients"):
			prefix := r.URL.Query().Get("clientId")
			out := []map[string]string{}
			for id, c := range clients {
				if strings.Contains(c, prefix) {
					out = append(out, map[string]string{"id": id, "clientId": c})
				}
			}
			json.NewEncoder(w).Encode(out)
		case r.Method == "DELETE":
			delete(clients, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer kc.Close()
	k := &Keycloak{URL: kc.URL, Realm: "lakehouse", ClientID: "platform-operator", Secret: "s"}
	if err := k.deleteClientsExcept(context.Background(), "svc-sales-", map[string]bool{"svc-sales-crm": true}, "svc-sales-eu-"); err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, c := range clients {
		left = append(left, c)
	}
	slices.Sort(left)
	if want := []string{"agent-sales-bot", "svc-sales-crm", "svc-sales-eu-crm", "svc-salesforce"}; !slices.Equal(left, want) {
		t.Errorf("left %v, want %v (only svc-sales-old removed)", left, want)
	}
}

// A service account that may act for people gets Keycloak's JWT authorization
// grant on its client, from the entra provider alone; any other client has it off.
func TestAClientMayExchangeEntraTokensOnlyWhenItActsForPeople(t *testing.T) {
	var mu sync.Mutex
	made := map[string]map[string]any{}
	kc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
			w.Write([]byte(`{"access_token":"t","expires_in":300}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/clients"):
			w.Write([]byte(`[]`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/clients"):
			var rep map[string]any
			json.NewDecoder(r.Body).Decode(&rep)
			made[rep["clientId"].(string)] = rep
			w.WriteHeader(201)
		default:
			http.NotFound(w, r)
		}
	}))
	defer kc.Close()
	k := &Keycloak{URL: kc.URL, Realm: "lakehouse", ClientID: "platform-operator", Secret: "s"}
	ctx := context.Background()
	if err := k.ensureAgentClient(ctx, "svc-sales-crm", "crm", "secret", "https://p", true); err != nil {
		t.Fatal(err)
	}
	if err := k.ensureAgentClient(ctx, "svc-sales-feed", "feed", "secret", "https://p", false); err != nil {
		t.Fatal(err)
	}
	if err := k.ensureAgentClient(ctx, "agent-sales-bot", "bot", "secret", "https://p"); err != nil {
		t.Fatal(err)
	}
	attr := func(c, k string) any { return made[c]["attributes"].(map[string]any)[k] }
	if attr("svc-sales-crm", "oauth2.jwt.authorization.grant.enabled") != "true" || attr("svc-sales-crm", "oauth2.jwt.authorization.grant.idp") != "entra" {
		t.Errorf("the account acting for people: %v", made["svc-sales-crm"]["attributes"])
	}
	for _, c := range []string{"svc-sales-feed", "agent-sales-bot"} {
		if attr(c, "oauth2.jwt.authorization.grant.enabled") != "false" {
			t.Errorf("%s may exchange Entra tokens: %v", c, made[c]["attributes"])
		}
	}
}
