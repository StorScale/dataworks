// Package trino runs statements in Trino as one of the platform's own
// accounts (a password, over HTTPS), or as whoever a Keycloak token names
// (Trino's JWT sign-in, as dbt and the notebooks use it), through Trino's
// HTTP protocol: the statement is posted, then its nextUri followed until
// it's done.
package trino

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	URL      string // https://trino:8443
	User     string
	Password string
	Token    string // a Keycloak access token: the statements run as whoever it names, in place of User and Password
	HTTP     *http.Client
}

// As is this client's connection, signing in with a Keycloak token: the
// statements run as whoever the token names, under their rules.
func (c *Client) As(token string) *Client {
	return &Client{URL: c.URL, Token: token, HTTP: c.HTTP}
}

// New is a client trusting the certificate in caFile (Trino's own, when
// it's self-signed), or the system's when it's "".
func New(url, user, password, caFile string) (*Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no certificate", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &Client{URL: strings.TrimRight(url, "/"), User: user, Password: password,
		HTTP: &http.Client{Transport: tr, Timeout: 2 * time.Minute}}, nil
}

// An Error is Trino's, about the statement.
type Error struct {
	Name, Message string
}

func (e *Error) Error() string { return e.Name + ": " + e.Message }

type response struct {
	ID      string `json:"id"`
	NextURI string `json:"nextUri"`
	Stats   struct {
		State string `json:"state"`
	} `json:"stats"`
	Error *struct {
		Message   string `json:"message"`
		ErrorName string `json:"errorName"`
	} `json:"error"`
	Data [][]any `json:"data"`
}

// Exec runs a statement to its end, with these session properties
// (catalog.name=value), and is its rows.
func (c *Client) Exec(ctx context.Context, sql string, session ...string) ([][]any, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+"/v1/statement", strings.NewReader(sql))
	if err != nil {
		return nil, err
	}
	if len(session) > 0 {
		req.Header.Set("X-Trino-Session", strings.Join(session, ","))
	}
	var rows [][]any
	for {
		var res response
		if err := c.do(req, &res); err != nil {
			return rows, err
		}
		rows = append(rows, res.Data...)
		if res.Error != nil {
			return rows, &Error{Name: res.Error.ErrorName, Message: res.Error.Message}
		}
		if res.NextURI == "" {
			return rows, nil
		}
		if req, err = http.NewRequestWithContext(ctx, "GET", res.NextURI, nil); err != nil {
			return rows, err
		}
	}
}

// do sends a request, again while Trino is too busy to take it (503).
func (c *Client) do(req *http.Request, out *response) error {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token) // Trino names the user from the token
	} else {
		req.SetBasicAuth(c.User, c.Password)
		req.Header.Set("X-Trino-User", c.User)
	}
	req.Header.Set("X-Trino-Source", "storscale-platform")
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	for attempt := 0; ; attempt++ {
		if body != nil {
			req.Body = io.NopCloser(strings.NewReader(string(body)))
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		res.Body.Close()
		switch {
		case res.StatusCode == http.StatusServiceUnavailable && attempt < 10:
			select {
			case <-req.Context().Done():
				return req.Context().Err()
			case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
			}
			continue
		case res.StatusCode == http.StatusUnauthorized:
			return errors.New("trino: the sign-in was refused (HTTP 401)")
		case res.StatusCode != http.StatusOK:
			return fmt.Errorf("trino: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
		}
		return json.Unmarshal(data, out)
	}
}

// Quote is an identifier, quoted for Trino.
func Quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
