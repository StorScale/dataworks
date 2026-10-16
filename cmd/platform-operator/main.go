// platform-operator applies StorScale Platform's projects to the systems they
// live in: Keycloak, Apache Ranger, Buckets, Nessie and Airflow. It reads projects from
// the store (a Buckets bucket, which platformd writes to; on Kubernetes,
// Project resources) and writes back each project's status.
//
// Configuration comes from the environment:
//
//	PROJECT_STORE                        bucket (the default), or kubernetes: Project resources
//	                                     in the pod's namespace, with a copy kept in the bucket
//	STORE_URL, STORE_BUCKET              the store: Buckets, and the bucket (storscale-platform)
//	BUCKETS_ACCESS_KEY, BUCKETS_SECRET_KEY  Buckets credentials that may make buckets and policies
//	KEYCLOAK_DIRECT_URL, KEYCLOAK_REALM  Keycloak, as the operator reaches it, and the realm
//	OPERATOR_CLIENT_ID, OPERATOR_CLIENT_SECRET  the operator's Keycloak client (a service account)
//	RANGER_URL, RANGER_USER, RANGER_PASSWORD, RANGER_SERVICE  Ranger's admin API, and Trino's service
//	NESSIE_URL, NESSIE_WAREHOUSE         Nessie, and its warehouse
//	AIRFLOW_URL, AIRFLOW_USER, AIRFLOW_PASSWORD  Airflow's API and the platform's account in it,
//	                                     for projects' pipelines repos (none: no repos)
//	PIPELINES_DEFAULT_REF                the branch a repo is followed on when its project names none (main)
//	GIT_KNOWN_HOSTS                      the git hosts' SSH keys, a file in Airflow's containers (none:
//	                                     a host's key is taken the first time it's seen)
//	AIRFLOW_PROJECT_TEMPLATE             on Kubernetes, the chart's project Airflow (a file): with it,
//	                                     each project with a repo gets an Airflow of its own
//	AIRFLOW_DB_HOST, POSTGRES_ADMIN_USER, POSTGRES_ADMIN_PASSWORD  the server their databases are on
//	                                     (airflow-db:5432), as its admin (postgres)
//	PIPELINES_DEFAULT_MEMORY             a project's Airflow's memory when it names none (3Gi)
//	PLATFORM_URL                         the platform's address (the pipelines' Keycloak, for them)
//	PLATFORM_LISTEN                      health checks (:8081), and GET /airflow/bundles: the DAG
//	                                     bundles Airflow should run (no credentials in it)
//
// `platform-operator healthcheck` checks a running operator.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/reconcile"
	"github.com/StorScale/storscale-platform/internal/store"
)

func main() {
	listen := env("PLATFORM_LISTEN", ":8081")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 3 * time.Second}
		res, err := c.Get("http://127.0.0.1" + listen[strings.LastIndex(listen, ":"):] + "/healthz")
		if err != nil || res.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	missing := []string{}
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			missing = append(missing, k)
		}
		return v
	}
	storeURL, accessKey, secretKey := need("STORE_URL"), need("BUCKETS_ACCESS_KEY"), need("BUCKETS_SECRET_KEY")
	kcURL, kcSecret := need("KEYCLOAK_DIRECT_URL"), need("OPERATOR_CLIENT_SECRET")
	rangerURL, rangerPassword := need("RANGER_URL"), need("RANGER_PASSWORD")
	nessieURL := need("NESSIE_URL")
	if len(missing) > 0 {
		log.Error("configuration", "error", "set "+strings.Join(missing, ", "))
		os.Exit(2)
	}
	// Projects: in the bucket (Compose), or Project resources with a copy in
	// the bucket for the services that read them there (Kubernetes).
	st, err := store.Open(os.Getenv("PROJECT_STORE"), storeURL, accessKey, secretKey, env("STORE_BUCKET", "storscale-platform"))
	if err != nil {
		log.Error("store", "error", err)
		os.Exit(2)
	}
	r := &reconcile.Reconciler{
		Keycloak: &reconcile.Keycloak{URL: strings.TrimRight(kcURL, "/"), Realm: env("KEYCLOAK_REALM", "lakehouse"),
			ClientID: env("OPERATOR_CLIENT_ID", "platform-operator"), Secret: kcSecret},
		Ranger: &reconcile.Ranger{URL: strings.TrimRight(rangerURL, "/"), User: env("RANGER_USER", "admin"),
			Password: rangerPassword, Service: env("RANGER_SERVICE", "lakehouse")},
		Buckets:            &reconcile.Buckets{URL: strings.TrimRight(storeURL, "/"), AccessKey: accessKey, SecretKey: secretKey},
		Nessie:             &reconcile.Nessie{URL: strings.TrimRight(nessieURL, "/"), Warehouse: env("NESSIE_WAREHOUSE", "warehouse")},
		Store:              st,
		Log:                log,
		RangerUserPassword: rangerPassword,
	}
	if u := os.Getenv("AIRFLOW_URL"); u != "" {
		r.Airflow = &reconcile.Airflow{API: pipelines.New(u, env("AIRFLOW_USER", "storscale-platform"), os.Getenv("AIRFLOW_PASSWORD")),
			DefaultRef: env("PIPELINES_DEFAULT_REF", "main"), KnownHosts: os.Getenv("GIT_KNOWN_HOSTS")}
	}
	// On Kubernetes, each project with a repo gets an Airflow of its own, from
	// the chart's template (AIRFLOW_PROJECT_TEMPLATE), with a database on
	// Airflow's server (AIRFLOW_DB_HOST, as POSTGRES_ADMIN_PASSWORD's user).
	if tpl := os.Getenv("AIRFLOW_PROJECT_TEMPLATE"); tpl != "" && r.Airflow != nil {
		t, err := reconcile.LoadTemplate(tpl)
		if err != nil {
			log.Error("project Airflow template", "error", err)
			os.Exit(2)
		}
		kube, err := reconcile.NewKube()
		if err != nil {
			log.Error("kubernetes", "error", err)
			os.Exit(2)
		}
		pg := url.URL{Scheme: "postgres", User: url.UserPassword(env("POSTGRES_ADMIN_USER", "postgres"), os.Getenv("POSTGRES_ADMIN_PASSWORD")),
			Host: env("AIRFLOW_DB_HOST", "airflow-db:5432"), Path: "/postgres", RawQuery: "sslmode=disable&connect_timeout=10"}
		r.ProjectAirflows = &reconcile.ProjectAirflows{Kube: kube, Postgres: &reconcile.Postgres{URL: pg.String()}, Template: t,
			DefaultMemory: env("PIPELINES_DEFAULT_MEMORY", "3Gi"), PlatformURL: os.Getenv("PLATFORM_URL")}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
		// Airflow's processes start with this list (stack/tools/airflow_bundles.py).
		mux.HandleFunc("GET /airflow/bundles", func(w http.ResponseWriter, req *http.Request) {
			if r.Airflow == nil {
				http.Error(w, "no Airflow configured", http.StatusNotFound)
				return
			}
			bundles, err := r.Bundles(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(bundles)
		})
		hs := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { <-ctx.Done(); hs.Close() }()
		hs.ListenAndServe()
	}()
	log.Info("platform-operator running")
	r.Run(ctx, 3*time.Second, time.Minute, 10*time.Second)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
