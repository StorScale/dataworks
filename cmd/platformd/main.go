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
//	HEALTH                on: check the platform's services every few seconds (the Health page),
//	                      and alert the administrators (where the Secret storscale-alerts says)
//	METRICS_LISTEN        the checks' metrics, for Prometheus (:9102): never through the gateway
//	INSTALL_TARGET        which of a project's targets this install is (production): its repo's
//	                      storscale/targets/<target>.yaml, laid over storscale/project.yaml
//
// `platformd healthcheck` checks a running platformd, for container health checks.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
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
	PlatformURL, KeycloakURL, KeycloakDirectURL, Realm string
	ClientID, ClientSecret                             string
	Groups, AdminGroups                                []string
	// DeliveryApproverGroups approve deliveries that send personal data (use
	// case 9.1), beside the project's editors; default the administrators.
	DeliveryApproverGroups []string
	// DeliveryKeepFor is how long a personal delivery's read is kept, so the
	// log can be searched by a person's ID (9.2): 7y, or days as 400d.
	DeliveryKeepFor string
	// ProductCertifierGroups certify data products (use case 10.3): the
	// governance group; default the administrators.
	ProductCertifierGroups []string
	// Protection is what the catalog's tags mean (12.5), as the operator
	// applies it (PROTECTION_TAGS), and whether suggested tags are applied
	// too: for the Governance page (12.9), which shows it.
	Protection       project.ProtectionRules
	ProtectSuggested bool
	// Rates is the install's rate card (USAGE_RATES, 13.1), and UsageGroups
	// see every project's usage and cost (USAGE_VIEWER_GROUPS: finance;
	// default the administrators).
	Rates       rates
	UsageGroups []string
	// MLflowURL is the platform's MLflow (15.1), reached only through
	// platformd; empty: none.
	MLflowURL string
	// ServingURL is where the projects' model servers answer (15.1): on
	// Kubernetes with {server} for a serving's own (http://{server}:8080),
	// in Compose the shared one; empty: models aren't served.
	ServingURL string
	// ErasureDeadlineDays is how long the company has to answer a request
	// to delete a person's data (use case 6.9): the CCPA's 45, by default.
	ErasureDeadlineDays int
	// LLMGatewayURL is the LLM gateway (use case 12.1), and its master key
	// (to read the projects' keys' spend); empty: none.
	LLMGatewayURL, LLMGatewayMasterKey string
	// OperatorURL is the platform operator's (a GitLab webhook asks it to
	// look at a project's merge requests at once).
	OperatorURL string
	// GitLabURL, GitLabGroup and GitLabToken are the install's GitLab for
	// project templates and shared libraries (U.2): a project made from a
	// template gets a copy of its repo in the group (its full path), and
	// the token (api scope; may make projects there, read the templates' and
	// libraries' repos) reads libraries for the projects: GITLAB_TOKEN, or
	// GITLAB_TOKEN_FILE (a mounted Secret's, read at each use, so a new
	// token needs no restart). Empty: templates without repos, and no
	// libraries.
	GitLabURL, GitLabGroup, GitLabToken, GitLabTokenFile string
	// InstallTarget is which of a project's targets (its repo's
	// storscale/targets/<target>.yaml) this install is: development, production.
	InstallTarget                                         string
	Listen, WebDir                                        string
	StoreURL, StoreBucket, StoreAccessKey, StoreSecretKey string
	ProjectStore                                          string // bucket (the default) or kubernetes
	CatalogURL, CatalogTokenFile                          string
	SemanticURL                                           string
	AirflowURL, AirflowUser, AirflowPassword              string
	ProjectAirflows                                       bool   // a project with a repo has its own Airflow (Kubernetes)
	Health                                                bool   // check the platform's services, and alert (HEALTH=on)
	MetricsListen                                         string // the checks' metrics, for Prometheus (:9102)
	BrandName, BrandTitle, BrandLogo, BrandIcon           string // the top bar's corner, the tab (brand.go)
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
		PlatformURL:            strings.TrimRight(get("PLATFORM_URL"), "/"),
		KeycloakURL:            strings.TrimRight(get("KEYCLOAK_URL"), "/"),
		Realm:                  or("KEYCLOAK_REALM", "lakehouse"),
		ClientID:               or("OIDC_CLIENT_ID", "platform"),
		ClientSecret:           get("OIDC_CLIENT_SECRET"),
		Groups:                 list("PLATFORM_GROUPS", "analysts,engineers"),
		AdminGroups:            list("PLATFORM_ADMIN_GROUPS", "engineers"),
		DeliveryApproverGroups: list("DELIVERY_APPROVER_GROUPS", or("PLATFORM_ADMIN_GROUPS", "engineers")),
		DeliveryKeepFor:        or("DELIVERY_KEEP_FOR", "7y"),
		ProductCertifierGroups: list("PRODUCT_CERTIFIER_GROUPS", or("PLATFORM_ADMIN_GROUPS", "engineers")),
		ErasureDeadlineDays:    atoi(or("ERASURE_DEADLINE_DAYS", "45"), 45),
		LLMGatewayURL:          strings.TrimRight(get("LLM_GATEWAY_URL"), "/"), LLMGatewayMasterKey: get("LLM_GATEWAY_MASTER_KEY"),
		OperatorURL:   or("OPERATOR_URL", "http://platform-operator:8081"),
		InstallTarget: or("INSTALL_TARGET", "production"),
		Listen:        or("PLATFORM_LISTEN", ":8080"),
		WebDir:        or("PLATFORM_WEB_DIR", "/usr/share/storscale/web"),
		StoreURL:      get("STORE_URL"), StoreBucket: or("STORE_BUCKET", "storscale-platform"),
		ProjectStore:   get("PROJECT_STORE"),
		StoreAccessKey: get("STORE_ACCESS_KEY"), StoreSecretKey: get("STORE_SECRET_KEY"),
		CatalogURL: get("CATALOG_URL"), CatalogTokenFile: or("CATALOG_TOKEN_FILE", "/catalog/token"),
		SemanticURL: strings.TrimRight(get("SEMANTIC_URL"), "/"),
		AirflowURL:  get("AIRFLOW_URL"), AirflowUser: get("AIRFLOW_USER"), AirflowPassword: get("AIRFLOW_PASSWORD"),
		ProjectAirflows: get("PROJECT_AIRFLOWS") == "true",
		Health:          get("HEALTH") == "on", MetricsListen: or("METRICS_LISTEN", ":9102"),
		BrandName: or("BRAND_NAME", "StorScale"), BrandTitle: get("BRAND_TITLE"),
		BrandLogo: get("BRAND_LOGO"), BrandIcon: get("BRAND_ICON"),
	}
	c.KeycloakDirectURL = strings.TrimRight(or("KEYCLOAK_DIRECT_URL", c.KeycloakURL), "/")
	c.ProtectSuggested = or("PROTECTION_APPLY_SUGGESTED", "true") != "false"
	protection, err := project.ParseProtection(get("PROTECTION_TAGS"))
	if err != nil {
		return c, fmt.Errorf("PROTECTION_TAGS: %w", err)
	}
	c.Protection = protection
	if c.Rates, err = parseRates(get("USAGE_RATES")); err != nil {
		return c, fmt.Errorf("USAGE_RATES: %w", err)
	}
	c.UsageGroups = list("USAGE_VIEWER_GROUPS", or("PLATFORM_ADMIN_GROUPS", "engineers"))
	c.MLflowURL = strings.TrimRight(get("MLFLOW_URL"), "/")
	c.GitLabURL, c.GitLabGroup, c.GitLabToken = strings.TrimRight(get("GITLAB_URL"), "/"), strings.Trim(get("GITLAB_GROUP"), "/"), get("GITLAB_TOKEN")
	c.GitLabTokenFile = get("GITLAB_TOKEN_FILE")
	c.ServingURL = strings.TrimRight(get("SERVING_URL"), "/")
	var missing []string
	for k, v := range map[string]string{"PLATFORM_URL": c.PlatformURL, "KEYCLOAK_URL": c.KeycloakURL, "OIDC_CLIENT_SECRET": c.ClientSecret} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, errors.New("set " + strings.Join(missing, ", "))
	}
	if _, ok := keepUntil(c.DeliveryKeepFor, time.Now()); !ok {
		return c, errors.New("DELIVERY_KEEP_FOR: as 7y or 400d")
	}
	return c, nil
}

func atoi(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
