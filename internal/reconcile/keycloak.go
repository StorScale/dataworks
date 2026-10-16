package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Keycloak is the realm's admin API, as the operator's own client (a service
// account with realm-management's manage-users, view-users, query-groups,
// view-clients and manage-clients: it makes each project's pipelines client).
type Keycloak struct {
	URL, Realm, ClientID, Secret string // URL: Keycloak as the operator reaches it

	mu      sync.Mutex
	token   string
	expires time.Time
}

type kcGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type kcUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (k *Keycloak) auth(ctx context.Context) (func(*http.Request), error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.token == "" || time.Now().After(k.expires) {
		var tok struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		}
		form := url.Values{"grant_type": {"client_credentials"}, "client_id": {k.ClientID}, "client_secret": {k.Secret}}
		req, _ := http.NewRequestWithContext(ctx, "POST", k.URL+"/realms/"+k.Realm+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return nil, &HTTPError{Method: "POST", URL: req.URL.String(), Status: res.StatusCode, Body: "the operator's client credentials"}
		}
		if err := decode(res, &tok); err != nil {
			return nil, err
		}
		k.token, k.expires = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn-15)*time.Second)
	}
	t := k.token
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+t) }, nil
}

func (k *Keycloak) do(ctx context.Context, method, path string, body, out any) error {
	auth, err := k.auth(ctx)
	if err != nil {
		return err
	}
	return call(ctx, method, k.URL+"/admin/realms/"+k.Realm+path, auth, body, out)
}

// group finds a top-level group by name; nil when there's none.
func (k *Keycloak) group(ctx context.Context, name string) (*kcGroup, error) {
	var found []kcGroup
	if err := k.do(ctx, "GET", "/groups?exact=true&briefRepresentation=true&search="+url.QueryEscape(name), nil, &found); err != nil {
		return nil, err
	}
	for _, g := range found {
		if g.Name == name {
			return &g, nil
		}
	}
	return nil, nil
}

func (k *Keycloak) ensureGroup(ctx context.Context, name string) (*kcGroup, error) {
	if g, err := k.group(ctx, name); g != nil || err != nil {
		return g, err
	}
	if err := k.do(ctx, "POST", "/groups", map[string]string{"name": name}, nil); err != nil && !isStatus(err, http.StatusConflict) {
		return nil, err
	}
	return k.group(ctx, name)
}

func (k *Keycloak) deleteGroup(ctx context.Context, name string) error {
	g, err := k.group(ctx, name)
	if g == nil || err != nil {
		return err
	}
	return k.do(ctx, "DELETE", "/groups/"+g.ID, nil, nil)
}

func (k *Keycloak) members(ctx context.Context, groupID string) ([]kcUser, error) {
	var out []kcUser
	return out, k.do(ctx, "GET", "/groups/"+groupID+"/members?briefRepresentation=true&max=10000", nil, &out)
}

func (k *Keycloak) user(ctx context.Context, username string) (*kcUser, error) {
	var found []kcUser
	if err := k.do(ctx, "GET", "/users?exact=true&briefRepresentation=true&username="+url.QueryEscape(username), nil, &found); err != nil {
		return nil, err
	}
	for _, u := range found {
		if strings.EqualFold(u.Username, username) {
			return &u, nil
		}
	}
	return nil, nil
}

// serviceAccount is a client's service-account user; nil when the client has none.
func (k *Keycloak) serviceAccount(ctx context.Context, clientID string) (*kcUser, error) {
	var clients []struct {
		ID string `json:"id"`
	}
	if err := k.do(ctx, "GET", "/clients?clientId="+url.QueryEscape(clientID), nil, &clients); err != nil || len(clients) == 0 {
		return nil, err
	}
	var u kcUser
	if err := k.do(ctx, "GET", "/clients/"+clients[0].ID+"/service-account-user", nil, &u); err != nil {
		if isStatus(err, http.StatusBadRequest) || isStatus(err, http.StatusNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

// syncMembers makes a group's members exactly these users (by ID).
func (k *Keycloak) syncMembers(ctx context.Context, g *kcGroup, want map[string]kcUser) error {
	have, err := k.members(ctx, g.ID)
	if err != nil {
		return err
	}
	in := map[string]bool{}
	for _, u := range have {
		in[u.ID] = true
		if _, ok := want[u.ID]; !ok {
			if err := k.do(ctx, "DELETE", "/users/"+u.ID+"/groups/"+g.ID, nil, nil); err != nil {
				return err
			}
		}
	}
	for id := range want {
		if !in[id] {
			if err := k.do(ctx, "PUT", "/users/"+id+"/groups/"+g.ID, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// pipelinesMappers are what a pipelines client's tokens carry, as the
// realm's own airflow-pipelines has them: its groups (Buckets' policies
// name them) and the lakehouse audience (Trino's and Buckets').
var pipelinesMappers = []map[string]any{
	{"name": "groups", "protocol": "openid-connect", "protocolMapper": "oidc-group-membership-mapper",
		"config": map[string]string{"claim.name": "groups", "full.path": "false", "access.token.claim": "true",
			"id.token.claim": "true", "userinfo.token.claim": "true"}},
	{"name": "audience: lakehouse", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
		"config": map[string]string{"included.client.audience": "lakehouse", "access.token.claim": "true", "id.token.claim": "false"}},
}

func (k *Keycloak) clientUUID(ctx context.Context, clientID string) (string, error) {
	var clients []struct {
		ID string `json:"id"`
	}
	if err := k.do(ctx, "GET", "/clients?clientId="+url.QueryEscape(clientID), nil, &clients); err != nil || len(clients) == 0 {
		return "", err
	}
	return clients[0].ID, nil
}

// ensurePipelinesClient makes a confidential client with a service account
// and this secret (the operator's, kept in a Secret: Keycloak's realm may be
// imported afresh, and the client made again, with the same secret).
func (k *Keycloak) ensurePipelinesClient(ctx context.Context, clientID, name, secret string) error {
	rep := map[string]any{"clientId": clientID, "name": name, "enabled": true, "publicClient": false,
		"clientAuthenticatorType": "client-secret", "secret": secret, "standardFlowEnabled": false,
		"directAccessGrantsEnabled": false, "serviceAccountsEnabled": true, "protocolMappers": pipelinesMappers}
	id, err := k.clientUUID(ctx, clientID)
	if err != nil {
		return err
	}
	if id == "" {
		if err := k.do(ctx, "POST", "/clients", rep, nil); err != nil && !isStatus(err, http.StatusConflict) {
			return fmt.Errorf("client %s: %w", clientID, err)
		}
		return nil
	}
	delete(rep, "protocolMappers") // kept as made; a PUT doesn't change them
	rep["id"] = id
	if err := k.do(ctx, "PUT", "/clients/"+id, rep, nil); err != nil {
		return fmt.Errorf("client %s: %w", clientID, err)
	}
	return nil
}

func (k *Keycloak) deleteClient(ctx context.Context, clientID string) error {
	id, err := k.clientUUID(ctx, clientID)
	if id == "" || err != nil {
		return err
	}
	return k.do(ctx, "DELETE", "/clients/"+id, nil, nil)
}
