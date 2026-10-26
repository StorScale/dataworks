package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

// A fileReader lists and reads the platform store's files (the assistants
// platformd registers, at assistants/<name>.json).
type fileReader interface {
	ListFiles(ctx context.Context, prefix string) ([]string, error)
	GetFile(ctx context.Context, key string) ([]byte, error)
}

const assistantPrefix = "assistant-"

// applyAssistants keeps a Keycloak client for each registered assistant (use
// case 10.4), and removes the clients of assistants taken away. It's done
// when the registrations changed, or every resync.
func (r *Reconciler) applyAssistants(ctx context.Context, resync time.Duration) {
	if r.Files == nil || r.PlatformURL == "" {
		return
	}
	keys, err := r.Files.ListFiles(ctx, "assistants/")
	if err != nil {
		r.Log.Error("reading assistants", "error", err)
		return
	}
	var want []project.Assistant
	h := sha256.New()
	for _, k := range keys {
		b, err := r.Files.GetFile(ctx, k)
		if err != nil {
			r.Log.Error("reading assistant", "key", k, "error", err)
			return
		}
		var a project.Assistant
		if json.Unmarshal(b, &a) != nil || a.Check() != nil {
			continue
		}
		want = append(want, a)
		h.Write(b)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if digest == r.assistantsDigest && time.Since(r.assistantsAt) < resync {
		return
	}
	if err := r.Keycloak.syncAssistants(ctx, want, r.PlatformURL); err != nil {
		r.Log.Error("assistants", "error", err)
		return
	}
	r.assistantsDigest, r.assistantsAt = digest, time.Now()
}

// syncAssistants makes the assistants' clients as the realm's storscale-agent
// is: public, PKCE, signing the person in (and the password grant, for
// checks and scripts), its tokens for the MCP server and Trino with the
// person's groups. Clients of the prefix that aren't wanted any more go.
func (k *Keycloak) syncAssistants(ctx context.Context, want []project.Assistant, platformURL string) error {
	mappers := append(append([]map[string]any{}, pipelinesMappers...), map[string]any{
		"name": "audience: the MCP server", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
		"config": map[string]string{"included.custom.audience": strings.TrimRight(platformURL, "/") + "/mcp",
			"access.token.claim": "true", "id.token.claim": "false"}})
	wanted := map[string]bool{}
	for _, a := range want {
		wanted[a.ClientID()] = true
		rep := map[string]any{"clientId": a.ClientID(), "name": a.Name, "description": a.Description, "enabled": true,
			"publicClient": true, "standardFlowEnabled": true, "directAccessGrantsEnabled": true, "serviceAccountsEnabled": false,
			"redirectUris": a.RedirectURIs, "attributes": map[string]string{"pkce.code.challenge.method": "S256"},
			"protocolMappers": mappers}
		id, err := k.clientUUID(ctx, a.ClientID())
		if err != nil {
			return err
		}
		if id == "" {
			if err := k.do(ctx, "POST", "/clients", rep, nil); err != nil && !isStatus(err, http.StatusConflict) {
				return fmt.Errorf("client %s: %w", a.ClientID(), err)
			}
			continue
		}
		delete(rep, "protocolMappers") // kept as made; a PUT doesn't change them
		rep["id"] = id
		if err := k.do(ctx, "PUT", "/clients/"+id, rep, nil); err != nil {
			return fmt.Errorf("client %s: %w", a.ClientID(), err)
		}
	}
	var clients []struct {
		ID       string `json:"id"`
		ClientID string `json:"clientId"`
	}
	if err := k.do(ctx, "GET", "/clients?search=true&clientId="+url.QueryEscape(assistantPrefix)+"&max=1000", nil, &clients); err != nil {
		return err
	}
	for _, c := range clients {
		if strings.HasPrefix(c.ClientID, assistantPrefix) && !wanted[c.ClientID] {
			if err := k.do(ctx, "DELETE", "/clients/"+c.ID, nil, nil); err != nil {
				return fmt.Errorf("client %s: %w", c.ClientID, err)
			}
		}
	}
	return nil
}
