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
