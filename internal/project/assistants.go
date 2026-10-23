package project

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// An Assistant is one the company approved to use the platform over MCP as
// the person who signs in to it (use case 10.4): DevAssist, say. The operator
// keeps a public Keycloak client for it, assistant-<name>, as the realm's
// storscale-agent is made; an administrator registers and removes them.
type Assistant struct {
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	RedirectURIs []string  `json:"redirectUris"` // where Keycloak may send it back to, as http://localhost:* or https://assistant.example.com/callback
	AddedBy      string    `json:"addedBy,omitempty"`
	AddedAt      time.Time `json:"addedAt,omitempty"`
}

// NameOK says whether a name is one the platform gives things (a project's, an assistant's).
func NameOK(s string) bool { return nameRE.MatchString(s) }

// IdentifierOK says whether a string is a table's, a column's or a tag's name: lowercase letters, digits and underscores.
func IdentifierOK(s string) bool { return identifierRE.MatchString(s) }

// ClientID is the assistant's Keycloak client: what Ranger's audit and the
// platform's name as the agent.
func (a Assistant) ClientID() string { return "assistant-" + a.Name }

// Check is why an assistant can't be registered as it is, or "".
func (a Assistant) Check() error {
	if !nameRE.MatchString(a.Name) {
		return errors.New("an assistant's name is lowercase letters, digits and dashes, 3 to 40 long, starting with a letter")
	}
	if len(a.RedirectURIs) == 0 {
		return errors.New("an assistant needs at least one redirect URI: where Keycloak sends the person back after they sign in")
	}
	for _, u := range a.RedirectURIs {
		p, err := url.Parse(strings.TrimSuffix(u, "*"))
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return errors.New("a redirect URI is an http or https address, as http://localhost:* or https://assistant.example.com/callback: not " + u)
		}
		if p.Scheme == "http" && p.Hostname() != "localhost" && p.Hostname() != "127.0.0.1" {
			return errors.New("an http redirect URI is for localhost only; elsewhere it's https: " + u)
		}
	}
	return nil
}
