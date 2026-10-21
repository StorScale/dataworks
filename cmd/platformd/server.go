package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/StorScale/storscale-platform/internal/audit"
	"github.com/StorScale/storscale-platform/internal/catalog"
	"github.com/StorScale/storscale-platform/internal/flow"
	"github.com/StorScale/storscale-platform/internal/llm"
	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/store"
)

const (
	sessionCookie = "storscale_session"
	sessionTTL    = 10 * time.Hour
	loginTTL      = 10 * time.Minute
)

// A session is a signed-in person. Sessions live in memory: when platformd
// restarts, people sign in again, which Keycloak's own session makes silent.
type session struct {
	User    user
	IDToken string        // for Keycloak's logout (id_token_hint)
	Token   *oauth2.Token // their access token (refreshed as needed), for calls made as them
	Expires time.Time
}

type user struct {
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
}

// A login is a sign-in in progress, from /auth/login to /auth/callback.
type login struct {
	Verifier, Nonce, Next string
	Expires               time.Time
}

type server struct {
	cfg       config
	log       *slog.Logger
	oauth     oauth2.Config
	verifier  *oidc.IDTokenVerifier
	tokens    *oidc.IDTokenVerifier            // the realm's access tokens, any client's (a project's pipelines)
	tokenUser func(raw string) (string, error) // in tests, who a token is (in place of tokens)
	// in tests, a table's columns (in place of the catalog's)
	tableColumns func(ctx context.Context, namespace, table string) ([]flow.Column, error)
	gateway      *llm.Gateway // the LLM gateway (12.1); nil: none
	files        fileStore    // the platform's files in its bucket (dbt's artifacts); nil without STORE_URL
	secure       bool         // cookies only over HTTPS
	now          func() time.Time
	store        store.Store       // nil: no projects
	catalog      *catalog.Client   // nil: no Flow view
	airflow      *pipelines.Client // nil: no pipelines
	// projects' own Airflows (cfg.ProjectAirflows), by project, with the
	// password they were made with
	projectAirflows map[string]projectAirflow
	brand           brand
	index           []byte         // the web app's index.html, with the brand's title and icon
	health          *watch         // nil: no health checks (HEALTH isn't on)
	projectAlerts   *projectAlerts // nil: no project alerts (HEALTH isn't on)
	maintainer      *maintainer    // nil: no table maintenance (no MAINTENANCE_TRINO_PASSWORD)
	audit           *audit.Log     // nil: no audit log (no AUDIT_URL)
	history         *history       // the catalog's history: tags, as-of, a table's changes

	mu       sync.Mutex
	sessions map[string]*session
	logins   map[string]*login
}

func newServer(ctx context.Context, cfg config, log *slog.Logger) (*server, error) {
	pu, err := url.Parse(cfg.PlatformURL)
	if err != nil || pu.Host == "" {
		return nil, fmt.Errorf("PLATFORM_URL %q isn't a URL", cfg.PlatformURL)
	}
	// Browsers reach Keycloak at KEYCLOAK_URL, the tokens' issuer; platformd
	// reaches it at KEYCLOAK_DIRECT_URL, for codes, tokens and keys.
	keys := oidc.NewRemoteKeySet(ctx, cfg.direct()+"/protocol/openid-connect/certs")
	s := &server{
		cfg: cfg, log: log, now: time.Now,
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			RedirectURL: cfg.PlatformURL + "/auth/callback",
			Scopes:      []string{oidc.ScopeOpenID, "profile", "email"},
			Endpoint: oauth2.Endpoint{
				AuthURL:   cfg.issuer() + "/protocol/openid-connect/auth",
				TokenURL:  cfg.direct() + "/protocol/openid-connect/token",
				AuthStyle: oauth2.AuthStyleInHeader,
			},
		},
		verifier: oidc.NewVerifier(cfg.issuer(), keys, &oidc.Config{ClientID: cfg.ClientID}),
		tokens:   tokenVerifier(cfg.issuer(), keys),
		// Only over HTTPS: Chrome and Firefox count http://*.localhost as
		// secure, but Safari doesn't, and drops Secure cookies there.
		secure:   pu.Scheme == "https",
		sessions: map[string]*session{},
		logins:   map[string]*login{},
	}
	if s.brand, err = loadBrand(cfg.BrandName, cfg.BrandTitle, cfg.BrandLogo, cfg.BrandIcon); err != nil {
		return nil, err
	}
	s.brand.defaultIcon, _ = os.ReadFile(filepath.Join(cfg.WebDir, "favicon.svg"))
	if index, err := os.ReadFile(filepath.Join(cfg.WebDir, "index.html")); err == nil {
		s.index = s.brand.page(index)
	} else {
		log.Warn("no web app", "error", err)
	}
	if cfg.StoreURL != "" || cfg.ProjectStore == "kubernetes" {
		if s.store, err = store.Open(cfg.ProjectStore, cfg.StoreURL, cfg.StoreAccessKey, cfg.StoreSecretKey, cfg.StoreBucket); err != nil {
			return nil, err
		}
	}
	if cfg.StoreURL != "" {
		files, err := store.New(cfg.StoreURL, cfg.StoreAccessKey, cfg.StoreSecretKey, cfg.StoreBucket)
		if err != nil {
			return nil, err
		}
		s.files = files
	}
	if cfg.AirflowURL != "" {
		s.airflow = pipelines.New(cfg.AirflowURL, cfg.AirflowUser, cfg.AirflowPassword)
	}
	if cfg.LLMGatewayURL != "" { // the LLM gateway (12.1): the keys' spend, read for the pages and the alerts
		s.gateway = &llm.Gateway{URL: cfg.LLMGatewayURL, MasterKey: cfg.LLMGatewayMasterKey}
	}
	if cfg.CatalogURL != "" {
		s.catalog = catalog.New(cfg.CatalogURL, cfg.CatalogTokenFile, "trino")
	}
	go s.sweep(ctx)
	if err := s.startMaintenance(ctx); err != nil {
		return nil, err
	}
	if err := s.startAudit(); err != nil {
		return nil, err
	}
	s.startHistory()
	if cfg.Health {
		s.startHealth(ctx)
		s.startProjectAlerts(ctx)
		if cfg.MetricsListen != "" {
			go s.serveMetrics(ctx, cfg.MetricsListen)
		}
	}
	return s, nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /brand/logo", s.handleBrandLogo)
	mux.HandleFunc("GET /brand/icon", s.handleBrandIcon)
	mux.HandleFunc("GET /brand/catalog-logo", s.handleBrandCatalogLogo)
	mux.HandleFunc("GET /api/projects", s.handleProjects)
	mux.HandleFunc("GET /api/projects/{name}", s.handleProject)
	mux.HandleFunc("PUT /api/projects/{name}", s.handlePutProject)
	mux.HandleFunc("DELETE /api/projects/{name}", s.handleDeleteProject)
	mux.HandleFunc("GET /api/access", s.handleAccess)
	mux.HandleFunc("GET /api/projects/{name}/flow", s.handleFlow)
	mux.HandleFunc("GET /api/terms", s.handleSemantic)
	mux.HandleFunc("GET /api/products", s.handleProducts)
	mux.HandleFunc("GET /api/governance", s.handleGovernance) // across projects (12.9): administrators, certifiers, data-protection approvers
	mux.HandleFunc("GET /api/assistants", s.handleAssistants)
	mux.HandleFunc("PUT /api/assistants/{name}", s.handlePutAssistant)
	mux.HandleFunc("DELETE /api/assistants/{name}", s.handleDeleteAssistant)
	mux.HandleFunc("POST /api/products/{project}/{product}/requests", s.handleRequestProduct)
	mux.HandleFunc("POST /api/products/{project}/{product}/requests/{consumer}/{answer}", s.handleAnswerRequest)
	mux.HandleFunc("DELETE /api/products/{project}/{product}/consumers/{consumer}", s.handleRemoveConsumer)
	mux.HandleFunc("POST /api/products/{project}/{product}/certification", s.handleCertify)
	mux.HandleFunc("DELETE /api/products/{project}/{product}/certification", s.handleCertify)
	mux.HandleFunc("GET /api/projects/{name}/semantic", s.handleSemantic)
	mux.HandleFunc("PUT /api/projects/{name}/semantic", s.handleSemantic)
	mux.HandleFunc("POST /api/projects/{name}/semantic/query", s.handleSemantic)
	mux.HandleFunc("POST /api/projects/{name}/semantic/tests", s.handleSemantic)
	mux.HandleFunc("POST /api/projects/{name}/sql", s.handleSemantic)
	mux.HandleFunc("GET /api/projects/{name}/pipelines", s.handlePipelines)
	mux.HandleFunc("POST /api/projects/{name}/pipelines/{pipeline}/runs", s.handleStartRun)
	mux.HandleFunc("GET /api/projects/{name}/pipelines/{pipeline}/runs/{run}/log", s.handleRunLog)
	mux.HandleFunc("POST /api/projects/{name}/dbt/artifacts", s.handleDbtArtifacts)
	mux.HandleFunc("GET /api/projects/{name}/dbt/artifacts/{artifact}", s.handleDbtArtifact)
	mux.HandleFunc("GET /api/projects/{name}/deliveries", s.handleDeliveries)
	mux.HandleFunc("GET /api/projects/{name}/deliveries/search", s.handleDeliverySearch)
	mux.HandleFunc("POST /api/projects/{name}/deliveries/{delivery}/approve", s.handleApproveDelivery)
	mux.HandleFunc("POST /api/projects/{name}/deliveries/{delivery}/runs", s.handleDeliveryLog)
	mux.HandleFunc("POST /api/projects/{name}/imports/{import}/reset", s.handleResetImport)
	mux.HandleFunc("PUT /api/projects/{name}/imports/{import}", s.handlePutImport)
	mux.HandleFunc("DELETE /api/projects/{name}/imports/{import}", s.handleDeleteImport)
	mux.HandleFunc("GET /api/connectors", s.handleConnectors)
	mux.HandleFunc("POST /api/webhooks/gitlab/{name}", s.handleGitLabHook)
	mux.HandleFunc("GET /api/projects/{name}/erasures", s.handleErasures)
	mux.HandleFunc("POST /api/projects/{name}/erasures", s.handleErase)
	mux.HandleFunc("GET /api/projects/{name}/llm", s.handleLLM)
	mux.HandleFunc("GET /api/projects/{name}/llm/calls", s.handleLLMCalls) // its model calls (12.8), to its editors
	mux.HandleFunc("GET /api/projects/{name}/agents", s.handleAgents)
	// What an agent reads of its project with its own token (the MCP server's list_runs, list_alerts).
	mux.HandleFunc("GET /api/agent/projects/{name}/runs", s.handleAgentRuns)
	mux.HandleFunc("GET /api/agent/projects/{name}/alerts", s.handleAgentAlerts)
	mux.HandleFunc("POST /api/agent/projects/{name}/ask", s.handleAgentAsk)                // a question to the model, on the project's key (12.3's plain-language tests)
	mux.HandleFunc("GET /api/agent/projects/{name}/tools", s.handleListTools)              // what the caller may call and ask (12.7)
	mux.HandleFunc("POST /api/agent/projects/{name}/tools/{tool}/call", s.handleCallTool)  // a tool of the project's: its editors, the agents given it
	mux.HandleFunc("POST /api/agent/projects/{name}/agents/{agent}/ask", s.handleAskAgent) // an agent as a tool: its members, the agents given it
	mux.HandleFunc("PUT /api/projects/{name}/agents/{agent}", s.handlePutAgent)
	mux.HandleFunc("POST /api/projects/{name}/agents/{agent}/approve", s.handleApproveAgent)
	mux.HandleFunc("DELETE /api/projects/{name}/agents/{agent}", s.handleDeleteAgent)
	mux.HandleFunc("GET /api/projects/{name}/classification", s.handleClassification)
	mux.HandleFunc("POST /api/projects/{name}/classification", s.handleClassify)
	mux.HandleFunc("GET /api/projects/{name}/uploads", s.handleUploads)
	mux.HandleFunc("POST /api/projects/{name}/uploads", s.handleUpload)
	mux.HandleFunc("GET /api/projects/{name}/uploads/{file}", s.handleUploadPreview)
	mux.HandleFunc("DELETE /api/projects/{name}/uploads/{file}", s.handleDeleteUpload)
	mux.HandleFunc("GET /api/projects/{name}/flows", s.handleFlows)
	mux.HandleFunc("GET /api/projects/{name}/flows/{flow}", s.handleFlowView)
	mux.HandleFunc("PUT /api/projects/{name}/flows/{flow}", s.handleSaveFlow)
	mux.HandleFunc("POST /api/projects/{name}/flows/compile", s.handleFlowCompile)
	mux.HandleFunc("POST /api/projects/{name}/flows/preview", s.handleFlowPreview)
	mux.HandleFunc("POST /api/projects/{name}/flows/{flow}/steps/{step}/run", s.handleFlowLLMRun) // the llm step's table, by the flow's DAG
	mux.HandleFunc("GET /api/projects/{name}/knowledge", s.handleKnowledgeList)
	mux.HandleFunc("POST /api/projects/{name}/knowledge/{bank}/build", s.handleKnowledgeBuild)   // editors, the project's pipelines
	mux.HandleFunc("POST /api/projects/{name}/knowledge/{bank}/search", s.handleKnowledgeSearch) // members, and agents by their own token
	mux.HandleFunc("GET /api/notebooks/projects", s.handleNotebookProjects)
	mux.HandleFunc("POST /api/projects/{name}/notebooks/schedules", s.handleScheduleNotebook)
	mux.HandleFunc("POST /api/projects/{name}/notebooks/runs/{dag}/{run}", s.handleNotebookRun)
	mux.HandleFunc("GET /api/projects/{name}/notebooks/runs/{dag}/{run}", s.handleNotebookRunView)
	mux.HandleFunc("GET /api/projects/{name}/connections", s.handleConnections)
	mux.HandleFunc("POST /api/projects/{name}/connections", s.handlePutConnection)
	mux.HandleFunc("PUT /api/projects/{name}/connections/{connection}", s.handlePutConnection)
	mux.HandleFunc("DELETE /api/projects/{name}/connections/{connection}", s.handleDeleteConnection)
	mux.HandleFunc("GET /api/projects/{name}/plans", s.handlePlans)
	mux.HandleFunc("POST /api/projects/{name}/plans", s.handlePlan)
	mux.HandleFunc("POST /api/projects/{name}/plans/{plan}/files", s.handlePlanFiles)
	mux.HandleFunc("POST /api/projects/{name}/plans/{plan}/apply", s.handleApplyPlan)
	mux.HandleFunc("GET /api/projects/{name}/copies", s.handleCopies)
	mux.HandleFunc("GET /api/projects/{name}/asof", s.handleAsOf)
	mux.HandleFunc("GET /api/projects/{name}/tags", s.handleTags)
	mux.HandleFunc("POST /api/projects/{name}/tags", s.handleTags)
	mux.HandleFunc("DELETE /api/projects/{name}/tags/{tag}", s.handleTags)
	mux.HandleFunc("GET /api/projects/{name}/history", s.handleHistory)
	mux.HandleFunc("GET /api/projects/{name}/audit", s.handleProjectAudit)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	mux.HandleFunc("GET /api/projects/{name}/maintenance", s.handleMaintenance)
	mux.HandleFunc("POST /api/projects/{name}/maintenance", s.handleMaintenance)
	mux.HandleFunc("GET /api/projects/{name}/alerts", s.handleProjectAlerts)
	mux.HandleFunc("POST /api/projects/{name}/alerts/mute", s.handleProjectAlerts)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/health/{action}", s.handleHealthAction)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, http.StatusNotFound, "not found")
	})
	mux.Handle("/", s.web())
	return securityHeaders(requestIDs(mux))
}

// handleLogin sends the browser to Keycloak.
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, nonce, verifier := token(), token(), oauth2.GenerateVerifier()
	s.mu.Lock()
	s.logins[state] = &login{Verifier: verifier, Nonce: nonce, Next: safeNext(r.URL.Query().Get("next")), Expires: s.now().Add(loginTTL)}
	s.mu.Unlock()
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// handleCallback finishes a sign-in: the code for tokens, the ID token checked.
func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	l := s.logins[q.Get("state")]
	delete(s.logins, q.Get("state"))
	s.mu.Unlock()
	if l == nil || s.now().After(l.Expires) {
		// An old or reused link: start again.
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return
	}
	if e := q.Get("error"); e != "" {
		s.log.Warn("sign-in refused by Keycloak", "error", e, "description", q.Get("error_description"))
		http.Error(w, "Keycloak refused the sign-in: "+e, http.StatusForbidden)
		return
	}
	tok, err := s.oauth.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(l.Verifier))
	if err != nil {
		s.log.Error("code exchange", "error", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	id, err := s.verifier.Verify(r.Context(), raw)
	if err != nil || id.Nonce != l.Nonce {
		s.log.Error("ID token", "error", err, "nonce_ok", err == nil && id.Nonce == l.Nonce)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	var claims struct {
		Username string   `json:"preferred_username"`
		Name     string   `json:"name"`
		Email    string   `json:"email"`
		Groups   []string `json:"groups"`
	}
	if err := id.Claims(&claims); err != nil || claims.Username == "" {
		http.Error(w, "sign-in failed: no username in the token", http.StatusBadGateway)
		return
	}
	sid := token()
	s.mu.Lock()
	s.sessions[sid] = &session{User: user{claims.Username, claims.Name, claims.Email, claims.Groups}, IDToken: raw, Token: tok,
		Expires: s.now().Add(sessionTTL)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true, Secure: s.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
	s.log.Info("signed in", "user", claims.Username, "groups", claims.Groups, "allowed", s.allowed(claims.Groups))
	if s.allowed(claims.Groups) {
		s.record(r, claims.Username, "signed.in", "platform", "")
	} else {
		s.recordRefusal(r, claims.Username, "signed.in", "platform", "in none of the platform's groups")
	}
	http.Redirect(w, r, l.Next, http.StatusFound)
}

// handleSession says who's signed in, and what they may open.
func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
		return
	}
	if !s.allowed(sess.User.Groups) {
		writeJSON(w, http.StatusForbidden, map[string]any{"user": sess.User,
			"error": "Your account isn't in a group that may use StorScale Platform (" + strings.Join(s.cfg.Groups, ", ") + ")."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": sess.User, "tools": toolsFor(s.cfg.PlatformURL, sess.User.Groups, s.cfg.AdminGroups),
		"admin": s.isAdmin(sess.User), "projects": s.store != nil, "flow": s.catalog != nil, "semantic": s.cfg.SemanticURL != "", "pipelines": s.airflow != nil,
		"brand": s.brand.session()})
}

// handleLogout ends the platform's session and returns Keycloak's logout
// address, which ends the Keycloak session too.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Platform-Request") != "1" { // not a cross-site form post
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	end := url.Values{"client_id": {s.cfg.ClientID}, "post_logout_redirect_uri": {s.cfg.PlatformURL + "/"}}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		if sess := s.sessions[c.Value]; sess != nil {
			end.Set("id_token_hint", sess.IDToken)
			s.log.Info("signed out", "user", sess.User.Username)
			s.record(r, sess.User.Username, "signed.out", "platform", "")
		}
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"redirect": s.cfg.issuer() + "/protocol/openid-connect/logout?" + end.Encode()})
}

func (s *server) session(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[c.Value]
	if sess == nil || s.now().After(sess.Expires) {
		delete(s.sessions, c.Value)
		return nil
	}
	return sess
}

func (s *server) allowed(groups []string) bool {
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(s.cfg.Groups, g) })
}

func (s *server) sweep(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := s.now()
		s.mu.Lock()
		for k, v := range s.sessions {
			if now.After(v.Expires) {
				delete(s.sessions, k)
			}
		}
		for k, v := range s.logins {
			if now.After(v.Expires) {
				delete(s.logins, k)
			}
		}
		s.mu.Unlock()
	}
}

// web serves the app's files; any other path gets index.html (the app routes it).
func (s *server) web() http.Handler {
	files := http.FileServer(http.Dir(s.cfg.WebDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(s.cfg.WebDir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if st, err := os.Stat(p); err != nil || st.IsDir() || filepath.Base(p) == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(s.index)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

// safeNext keeps a post-sign-in redirect on the platform itself.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
