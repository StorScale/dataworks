package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ClientCredentials is the catalog's own account: Nessie answers only the
// platform's services, signed in with Keycloak as the client
// lakehouse-catalog (its service account is the one Nessie's rules allow).
// The token is fetched by client credentials and kept until shortly before
// it expires.
type ClientCredentials struct {
	TokenURL, ClientID, Secret string
	HTTP                       *http.Client // for the token endpoint; nil: a 30-second client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// CredentialsFromEnv is the account as the platform's services are given it:
// CATALOG_TOKEN_URL, CATALOG_CLIENT_ID (lakehouse-catalog) and
// CATALOG_CLIENT_SECRET. Nil without a secret: a catalog that asks no one to
// sign in (a test's).
func CredentialsFromEnv() *ClientCredentials {
	secret := os.Getenv("CATALOG_CLIENT_SECRET")
	if secret == "" {
		return nil
	}
	tokenURL := os.Getenv("CATALOG_TOKEN_URL")
	if tokenURL == "" {
		tokenURL = "http://keycloak:8080/sso/realms/lakehouse/protocol/openid-connect/token"
	}
	id := os.Getenv("CATALOG_CLIENT_ID")
	if id == "" {
		id = "lakehouse-catalog"
	}
	return &ClientCredentials{TokenURL: tokenURL, ClientID: id, Secret: secret}
}

// Token is a current access token.
func (c *ClientCredentials) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.ClientID}, "client_secret": {c.Secret}}
	req, err := http.NewRequestWithContext(ctx, "POST", c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("the catalog's account: %v", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the catalog's account (%s): Keycloak answered HTTP %d", c.ClientID, res.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("the catalog's account (%s): Keycloak gave no token", c.ClientID)
	}
	life := time.Duration(tok.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Minute
	}
	c.token, c.expires = tok.AccessToken, time.Now().Add(life*3/4)
	return c.token, nil
}

// Authorize puts the account's token on a request to the catalog.
func (c *ClientCredentials) Authorize(r *http.Request) error {
	if c == nil {
		return nil
	}
	t, err := c.Token(r.Context())
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+t)
	return nil
}

type authTransport struct {
	base http.RoundTripper
	cc   *ClientCredentials
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if err := t.cc.Authorize(r); err != nil {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, err
	}
	return t.base.RoundTrip(r)
}

// NessieHTTP is an HTTP client for the catalog (Nessie's APIs) as the platform's
// account, when the environment names one (CredentialsFromEnv); otherwise a
// plain one.
func NessieHTTP(timeout time.Duration) *http.Client {
	return ClientWith(CredentialsFromEnv(), timeout)
}

// ClientWith is NessieHTTP with the given account (nil: none).
func ClientWith(cc *ClientCredentials, timeout time.Duration) *http.Client {
	if cc == nil {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{Timeout: timeout, Transport: &authTransport{base: http.DefaultTransport, cc: cc}}
}
