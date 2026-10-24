package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// fakeKeycloak is just enough of a Keycloak realm: its keys, and a token
// endpoint that checks PKCE and issues an ID token for the person signing in.
type fakeKeycloak struct {
	*httptest.Server
	key       *rsa.PrivateKey
	issuer    string // as browsers see it: tokens must name this one
	challenge string // the code_challenge from the authorization request
	nonce     string
	person    map[string]any
}

func newFakeKeycloak(t *testing.T) *fakeKeycloak {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k := &fakeKeycloak{key: key, issuer: "http://storscale.localhost:8800/sso/realms/lakehouse"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /realms/lakehouse/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /realms/lakehouse/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		id, secret, _ := r.BasicAuth()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if id != "platform" || secret != "s3cret" || r.Form.Get("code") != "the-code" ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != k.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
		claims := map[string]any{"iss": k.issuer, "aud": "platform", "sub": "id-1", "nonce": k.nonce,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		for kk, v := range k.person {
			claims[kk] = v
		}
		raw, _ := jwt.Signed(signer).Claims(claims).Serialize()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw, "expires_in": 300})
	})
	k.Server = httptest.NewServer(mux)
	t.Cleanup(k.Close)
	return k
}

func newTestServer(t *testing.T, k *fakeKeycloak) *httptest.Server {
	return newTestServerWith(t, k, func(*config) {})
}

func newTestServerWith(t *testing.T, k *fakeKeycloak, change func(*config)) *httptest.Server {
	ts, _ := newTestPlatform(t, k, change)
	return ts
}

// newTestPlatform is newTestServerWith, with the server itself.
func newTestPlatform(t *testing.T, k *fakeKeycloak, change func(*config)) (*httptest.Server, *server) {
	web := t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>StorScale Platform</title>"), 0o644)
	cfg := config{
		PlatformURL: "http://storscale.localhost:8800", KeycloakURL: "http://storscale.localhost:8800/sso",
		KeycloakDirectURL: k.URL, Realm: "lakehouse", ClientID: "platform", ClientSecret: "s3cret",
		Groups: []string{"analysts", "engineers"}, AdminGroups: []string{"engineers"}, WebDir: web,
	}
	change(&cfg)
	s, err := newServer(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return ts, s
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// signIn goes through /auth/login and /auth/callback as a browser would, and
// returns the session cookie.
func signIn(t *testing.T, ts *httptest.Server, k *fakeKeycloak, person map[string]any, next string) (*http.Cookie, string) {
	t.Helper()
	res, err := noRedirects.Get(ts.URL + "/auth/login?next=" + url.QueryEscape(next))
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), k.issuer+"/protocol/openid-connect/auth") {
		t.Fatalf("login: %d to %q, want Keycloak's authorization endpoint as browsers see it", res.StatusCode, loc)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "http://storscale.localhost:8800/auth/callback" {
		t.Fatalf("authorization request: %v", q)
	}
	k.challenge, k.nonce, k.person = q.Get("code_challenge"), q.Get("nonce"), person
	res, err = noRedirects.Get(ts.URL + "/auth/callback?code=the-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("callback: %d %s", res.StatusCode, body)
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			if !c.HttpOnly || c.Secure || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("session cookie %+v: want HttpOnly, SameSite=Lax, and not Secure over http (Safari would drop it)", c)
			}
			return c, res.Header.Get("Location")
		}
	}
	t.Fatal("no session cookie")
	return nil, ""
}

func getSession(t *testing.T, ts *httptest.Server, c *http.Cookie) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/api/session", nil)
	if c != nil {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func toolIDs(body map[string]any) []string {
	var ids []string
	for _, t := range body["tools"].([]any) {
		ids = append(ids, t.(map[string]any)["id"].(string))
	}
	return ids
}

func TestSignInAnalyst(t *testing.T) {
	k := newFakeKeycloak(t)
	ts := newTestServer(t, k)
	if code, _ := getSession(t, ts, nil); code != http.StatusUnauthorized {
		t.Fatalf("no session: %d, want 401", code)
	}
	c, next := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "name": "Alice", "groups": []string{"analysts"}}, "/tools/sql")
	if next != "/tools/sql" {
		t.Errorf("after sign-in: %q, want the page they asked for", next)
	}
	code, body := getSession(t, ts, c)
	if code != http.StatusOK || body["user"].(map[string]any)["username"] != "alice" {
		t.Fatalf("session: %d %v", code, body)
	}
	if got := strings.Join(toolIDs(body), ","); got != "notebooks,sql,dashboards,pipelines,catalog,monitoring" {
		t.Errorf("alice's tools: %s (no administrators' tools)", got)
	}
}

func TestEngineersSeeAdministratorsTools(t *testing.T) {
	k := newFakeKeycloak(t)
	ts := newTestServer(t, k)
	c, _ := signIn(t, ts, k, map[string]any{"preferred_username": "bob", "groups": []string{"engineers"}}, "/")
	_, body := getSession(t, ts, c)
	if ids := toolIDs(body); ids[len(ids)-1] != "access" {
		t.Errorf("bob's tools: %v, want access policies too", ids)
	}
}

func TestPeopleInNeitherGroupAreRefused(t *testing.T) {
	k := newFakeKeycloak(t)
	ts := newTestServer(t, k)
	c, _ := signIn(t, ts, k, map[string]any{"preferred_username": "carol", "groups": []string{}}, "/")
	code, body := getSession(t, ts, c)
	if code != http.StatusForbidden || body["tools"] != nil {
		t.Fatalf("carol: %d %v, want 403 and no tools", code, body)
	}
}

func TestCallbackRejectsAnUnknownState(t *testing.T) {
	k := newFakeKeycloak(t)
	ts := newTestServer(t, k)
	res, _ := noRedirects.Get(ts.URL + "/auth/callback?code=the-code&state=made-up")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/auth/login" || len(res.Cookies()) != 0 {
		t.Fatalf("unknown state: %d to %q, cookies %v; want a fresh sign-in", res.StatusCode, res.Header.Get("Location"), res.Cookies())
	}
}

func TestCallbackRejectsATokenFromAnotherIssuer(t *testing.T) {
	k := newFakeKeycloak(t)
	ts := newTestServer(t, k)
	res, _ := noRedirects.Get(ts.URL + "/auth/login")
	q, _ := url.Parse(res.Header.Get("Location"))
	k.challenge, k.nonce = q.Query().Get("code_challenge"), q.Query().Get("nonce")
	k.issuer = "http://elsewhere/realms/lakehouse"
	k.person = map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}
	res, _ = noRedirects.Get(ts.URL + "/auth/callback?code=the-code&state=" + url.QueryEscape(q.Query().Get("state")))
	if res.StatusCode == http.StatusFound || len(res.Cookies()) != 0 {
		t.Fatalf("a token from another issuer was accepted: %d", res.StatusCode)
	}
}

func TestLogout(t *testing.T) {
	k := newFakeKeycloak(t)
	ts := newTestServer(t, k)
	c, _ := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}, "/")
	post := func(header bool) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+"/auth/logout", nil)
		req.AddCookie(c)
		if header {
			req.Header.Set("X-Platform-Request", "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := post(false); res.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without X-Platform-Request: %d, want 403", res.StatusCode)
	}
	res := post(true)
	var body map[string]string
	json.NewDecoder(res.Body).Decode(&body)
	end, _ := url.Parse(body["redirect"])
	if !strings.HasPrefix(body["redirect"], k.issuer+"/protocol/openid-connect/logout") || end.Query().Get("id_token_hint") == "" ||
		end.Query().Get("post_logout_redirect_uri") != "http://storscale.localhost:8800/" {
		t.Errorf("logout redirect %q: want Keycloak's logout, with the ID token and a way back", body["redirect"])
	}
	if code, _ := getSession(t, ts, c); code != http.StatusUnauthorized {
		t.Errorf("after logout: %d, want 401", code)
	}
}

func TestWebAppAndHeaders(t *testing.T) {
	ts := newTestServer(t, newFakeKeycloak(t))
	res, err := http.Get(ts.URL + "/tools/sql")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "StorScale Platform") {
		t.Errorf("app route: %d %s, want index.html", res.StatusCode, body)
	}
	if res.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Errorf("CSP %q: the platform itself is never framed", res.Header.Get("Content-Security-Policy"))
	}
	res, _ = http.Get(ts.URL + "/api/nothing")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown API path: %d, want 404", res.StatusCode)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"": "/", "/tools/sql": "/tools/sql", "//evil.example": "/", "https://evil.example": "/", "/\\evil": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolsFollowThePlatformsAddress(t *testing.T) {
	tools := toolsFor("https://data.example.com", []string{"analysts"}, []string{"engineers"})
	if tools[0].URL != "https://data.example.com/notebooks/hub/" || tools[1].EmbedURL != "https://data.example.com/dashboards/login/keycloak?next=%2Fdashboards%2Fsqllab%2F" ||
		tools[4].URL != "https://catalog.data.example.com/_storscale/launch.html" {
		t.Errorf("tools: %+v", tools[:2])
	}
}

func TestSemanticRequestsGoAsThePerson(t *testing.T) {
	var got []string
	semanticd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		w.Write([]byte(`{"metrics": []}`))
	}))
	defer semanticd.Close()
	k := newFakeKeycloak(t)
	ts := newTestServerWith(t, k, func(c *config) { c.SemanticURL = semanticd.URL })
	c, _ := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}, "/")
	call := func(method, path string, header bool) int {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(`{}`))
		req.AddCookie(c)
		if header {
			req.Header.Set("X-Platform-Request", "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if code := call("GET", "/api/projects/sales/semantic", false); code != http.StatusOK {
		t.Fatalf("GET: %d", code)
	}
	if code := call("PUT", "/api/projects/sales/semantic", false); code != http.StatusForbidden {
		t.Errorf("PUT without X-Platform-Request: %d, want 403", code)
	}
	if code := call("POST", "/api/projects/sales/semantic/query", true); code != http.StatusOK {
		t.Errorf("POST query: %d", code)
	}
	want := []string{"GET /api/projects/sales/semantic Bearer at", "POST /api/projects/sales/semantic/query Bearer at"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("semanticd got %q, want %q: the person's own access token", got, want)
	}
}

func TestAnswersHaveNoNullLists(t *testing.T) {
	type inner struct {
		Tags []string `json:"tags"`
	}
	type view struct {
		Name    string            `json:"name"`
		List    []string          `json:"list"`
		Kept    []string          `json:"kept,omitempty"`
		Raw     json.RawMessage   `json:"raw,omitempty"`
		Inner   *inner            `json:"inner"`
		Many    []inner           `json:"many"`
		ByName  map[string][]int  `json:"byName"`
		At      time.Time         `json:"at"`
		Unknown any               `json:"unknown"`
		Nothing []map[string]bool `json:"nothing"`
	}
	shared := &inner{}
	v := view{Name: "x", Inner: shared, Many: []inner{{}}, ByName: map[string][]int{"a": nil}, Unknown: map[string]any{"l": []string(nil)}}
	w := httptest.NewRecorder()
	writeJSON(w, 200, v)
	got := strings.TrimSpace(w.Body.String())
	for _, want := range []string{`"list":[]`, `"inner":{"tags":[]}`, `"many":[{"tags":[]}]`, `"byName":{"a":[]}`, `"unknown":{"l":[]}`, `"nothing":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s: no %s", got, want)
		}
	}
	if strings.Contains(got, "null") || strings.Contains(got, `"kept"`) || strings.Contains(got, `"raw"`) {
		t.Errorf("%s", got)
	}
	if shared.Tags != nil {
		t.Error("what the answer pointed at was changed")
	}
}
