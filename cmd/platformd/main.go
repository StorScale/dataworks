// platformd is StorScale Platform's backend for its web app: it signs people
// in with Keycloak (OpenID Connect, authorization code with PKCE), keeps their
// sessions, tells the app who they are and which tools they may open, and
// serves the app itself.
//
// Configuration comes from the environment:
//
//	PLATFORM_URL          the platform's address, as browsers see it (http://storscale.localhost:8800)
//	KEYCLOAK_URL          Keycloak, as browsers see it: the tokens' issuer is KEYCLOAK_URL/realms/REALM
//	KEYCLOAK_DIRECT_URL   Keycloak, as platformd reaches it (http://keycloak:8080/sso); default KEYCLOAK_URL
//	KEYCLOAK_REALM        the realm (lakehouse)
//	OIDC_CLIENT_ID        platformd's Keycloak client (platform)
//	OIDC_CLIENT_SECRET    and its secret
//	PLATFORM_GROUPS       the Keycloak groups that may use the platform (analysts,engineers)
//	PLATFORM_ADMIN_GROUPS the groups that also see administrators' tools (engineers)
//	PLATFORM_LISTEN       the address to listen on (:8080)
//	PLATFORM_WEB_DIR      the web app's files (/usr/share/storscale/web)
//	STORE_URL, STORE_BUCKET, STORE_ACCESS_KEY, STORE_SECRET_KEY
//	                      the project store (Buckets); without it, there are no projects
//	CATALOG_URL, CATALOG_TOKEN_FILE
//	                      the catalog (OpenMetadata), and the token catalog-sync shares; without
//	                      them, there's no Flow view
//	SEMANTIC_URL          the semantic layer (semanticd), called as the signed-in person
//	AIRFLOW_URL, AIRFLOW_USER, AIRFLOW_PASSWORD
//	                      Airflow, for the projects' pipelines, as the platform's Airflow account
//
// `platformd healthcheck` checks a running platformd, for container health checks.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		log.Error("configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := newServer(ctx, cfg, log)
	if err != nil {
		log.Error("start", "error", err)
		os.Exit(1)
	}
	hs := &http.Server{Addr: cfg.Listen, Handler: srv.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()
	log.Info("platformd listening", "addr", cfg.Listen, "platform", cfg.PlatformURL, "issuer", cfg.issuer())
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := os.Getenv("PLATFORM_LISTEN")
	if addr == "" || strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
		if addr == "127.0.0.1" {
			addr += ":8080"
		}
	}
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

type config struct {
	PlatformURL, KeycloakURL, KeycloakDirectURL, Realm    string
	ClientID, ClientSecret                                string
	Groups, AdminGroups                                   []string
	Listen, WebDir                                        string
	StoreURL, StoreBucket, StoreAccessKey, StoreSecretKey string
	ProjectStore                                          string // bucket (the default) or kubernetes
	CatalogURL, CatalogTokenFile                          string
	SemanticURL                                           string
	AirflowURL, AirflowUser, AirflowPassword              string
	BrandName, BrandLogo                                  string // the top bar's corner (brand.go)
}

func (c config) issuer() string { return c.KeycloakURL + "/realms/" + c.Realm }

func (c config) direct() string { return c.KeycloakDirectURL + "/realms/" + c.Realm }

func configFromEnv(get func(string) string) (config, error) {
	or := func(k, def string) string {
		if v := get(k); v != "" {
			return v
		}
		return def
	}
	list := func(k, def string) []string {
		var out []string
		for _, g := range strings.Split(or(k, def), ",") {
			if g = strings.TrimSpace(g); g != "" {
				out = append(out, g)
			}
		}
		return out
	}
	c := config{
		PlatformURL:  strings.TrimRight(get("PLATFORM_URL"), "/"),
		KeycloakURL:  strings.TrimRight(get("KEYCLOAK_URL"), "/"),
		Realm:        or("KEYCLOAK_REALM", "lakehouse"),
		ClientID:     or("OIDC_CLIENT_ID", "platform"),
		ClientSecret: get("OIDC_CLIENT_SECRET"),
		Groups:       list("PLATFORM_GROUPS", "analysts,engineers"),
		AdminGroups:  list("PLATFORM_ADMIN_GROUPS", "engineers"),
		Listen:       or("PLATFORM_LISTEN", ":8080"),
		WebDir:       or("PLATFORM_WEB_DIR", "/usr/share/storscale/web"),
		StoreURL:     get("STORE_URL"), StoreBucket: or("STORE_BUCKET", "storscale-platform"),
		ProjectStore:   get("PROJECT_STORE"),
		StoreAccessKey: get("STORE_ACCESS_KEY"), StoreSecretKey: get("STORE_SECRET_KEY"),
		CatalogURL: get("CATALOG_URL"), CatalogTokenFile: or("CATALOG_TOKEN_FILE", "/catalog/token"),
		SemanticURL: strings.TrimRight(get("SEMANTIC_URL"), "/"),
		AirflowURL:  get("AIRFLOW_URL"), AirflowUser: get("AIRFLOW_USER"), AirflowPassword: get("AIRFLOW_PASSWORD"),
		BrandName: or("BRAND_NAME", "StorScale"), BrandLogo: get("BRAND_LOGO"),
	}
	c.KeycloakDirectURL = strings.TrimRight(or("KEYCLOAK_DIRECT_URL", c.KeycloakURL), "/")
	var missing []string
	for k, v := range map[string]string{"PLATFORM_URL": c.PlatformURL, "KEYCLOAK_URL": c.KeycloakURL, "OIDC_CLIENT_SECRET": c.ClientSecret} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, errors.New("set " + strings.Join(missing, ", "))
	}
	return c, nil
}
