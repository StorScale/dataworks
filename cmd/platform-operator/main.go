// platform-operator applies DataWorks' projects to the systems they
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
//	TRINO_DB_HOST, TRINO_DB_PASSWORD     Trino's limits (its resource groups' database, trino-db:5432,
//	                                     as its user trino); none: Trino has no limits but its own
//	LIMITS_PROJECT_QUERIES, _QUEUED, _QUERY_MEMORY, _STORAGE  a project's limits when it names none
//	                                     (4, 40, 50%, none); LIMITS_PERSON_QUERIES, _QUEUED, a
//	                                     person's (4, 100); LIMITS_TOTAL_QUERIES, _QUEUED, the install's
//	                                     (20, 1000); TRINO_ENVIRONMENT, Trino's node.environment (docker)
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/llm"
	"github.com/StorScale/dataworks/internal/pipelines"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/reconcile"
	"github.com/StorScale/dataworks/internal/store"
	"github.com/StorScale/dataworks/internal/trino"
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
	var gateway *llm.Gateway // the LLM gateway (12.1), when the install has one
	if u := os.Getenv("LLM_GATEWAY_URL"); u != "" {
		gateway = &llm.Gateway{URL: u, MasterKey: os.Getenv("LLM_GATEWAY_MASTER_KEY")}
	}
	protection, err := project.ParseProtection(os.Getenv("PROTECTION_TAGS"))
	if err != nil {
		log.Error("settings", "error", err)
		os.Exit(1)
	}
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
		PlatformURL:        os.Getenv("PLATFORM_URL"),
		Protection:         protection,
		Gateway:            gateway,
		ProtectSuggested:   env("PROTECTION_APPLY_SUGGESTED", "true") != "false",
		Log:                log,
		RangerUserPassword: rangerPassword,
	}
	// The registered assistants are files in the platform's bucket (platformd's), whatever the project store is.
	if storeURL != "" {
		if b, err := store.New(storeURL, accessKey, secretKey, env("STORE_BUCKET", "storscale-platform")); err == nil {
			r.Files = b
		}
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
	// A project's models answering as an API (R15.4): on Kubernetes a server
	// for each, from the chart's template (SERVING_TEMPLATE); in Compose the
	// shared one, signed in as SERVING_ACCOUNT's service account.
	if tpl := os.Getenv("SERVING_TEMPLATE"); tpl != "" {
		t, err := reconcile.LoadTemplate(tpl)
		if err != nil {
			log.Error("serving template", "error", err)
			os.Exit(2)
		}
		kube, err := reconcile.NewKube()
		if err != nil {
			log.Error("kubernetes", "error", err)
			os.Exit(2)
		}
		r.ProjectServings = &reconcile.ProjectServings{Kube: kube, Template: t, DefaultMemory: env("SERVING_DEFAULT_MEMORY", "1Gi")}
	}
	r.ServingAccount = os.Getenv("SERVING_ACCOUNT")
	num := func(k string, def int) int {
		n, err := strconv.Atoi(os.Getenv(k))
		if err != nil || n <= 0 {
			return def
		}
		return n
	}
	r.DefaultStorage = os.Getenv("LIMITS_PROJECT_STORAGE")
	if pw := os.Getenv("TRINO_DB_PASSWORD"); pw != "" {
		db := url.URL{Scheme: "postgres", User: url.UserPassword("trino", pw), Host: env("TRINO_DB_HOST", "trino-db:5432"),
			Path: "/trino", RawQuery: "sslmode=disable&connect_timeout=10"}
		r.TrinoLimits = &reconcile.TrinoLimits{URL: db.String(), Environment: env("TRINO_ENVIRONMENT", "docker"),
			Project: project.Limits{Queries: num("LIMITS_PROJECT_QUERIES", 4), Queued: num("LIMITS_PROJECT_QUEUED", 40),
				QueryMemory: env("LIMITS_PROJECT_QUERY_MEMORY", "50%")},
			Person: project.Limits{Queries: num("LIMITS_PERSON_QUERIES", 4), Queued: num("LIMITS_PERSON_QUEUED", 100)},
			Total:  project.Limits{Queries: num("LIMITS_TOTAL_QUERIES", 20), Queued: num("LIMITS_TOTAL_QUEUED", 1000)}}
	}
	// The projects' sources: Trino's catalogs, made as the operator's Trino
	// account; their connections in a Secret mounted into Trino (Kubernetes,
	// SOURCES_SECRET) or a directory it shares (Compose, SOURCES_PATH).
	if pw := os.Getenv("OPERATOR_TRINO_PASSWORD"); pw != "" {
		tc, err := trino.New(env("TRINO_URL", "https://trino:8443"), env("OPERATOR_TRINO_USER", "platform-operator"), pw, os.Getenv("TRINO_CA"))
		if err != nil {
			log.Error("trino", "error", err)
			os.Exit(2)
		}
		var files reconcile.SourceFiles
		if name := os.Getenv("SOURCES_SECRET"); name != "" {
			kube, err := reconcile.NewKube()
			if err != nil {
				log.Error("kubernetes", "error", err)
				os.Exit(2)
			}
			files = &reconcile.SecretFiles{Kube: kube, Name: name}
		} else {
			files = &reconcile.DirFiles{Dir: env("SOURCES_PATH", "/sources")}
		}
		// A connection file in a Secret reaches Trino's pods after the kubelet's sync period: a new catalog waits for it.
		wait := 10 * time.Second
		if _, inSecret := files.(*reconcile.SecretFiles); inSecret {
			wait = 2 * time.Minute
		}
		r.Sources = &reconcile.TrinoSources{Trino: tc, Files: files, Dir: env("TRINO_SOURCES_DIR", "/etc/trino/sources"), FileWait: wait}
		// Projects' merge requests' data branches, their catalogs made as the same account.
		r.Reviewer = &reconcile.Reviewer{Trino: tc, Nessie: r.Nessie}
		r.Adopter = &reconcile.Adopter{Trino: tc, Iceberg: &catalog.Iceberg{HTTP: &http.Client{Timeout: time.Minute},
			Base: strings.TrimRight(nessieURL, "/") + "/iceberg", Warehouse: env("NESSIE_WAREHOUSE", "warehouse")}}
	}
	// On Kubernetes, the projects' sources copied into their tables, a pod
	// each, from the chart's template (COPY_TEMPLATE).
	if tpl := os.Getenv("COPY_TEMPLATE"); tpl != "" {
		t, err := reconcile.LoadCopyTemplate(tpl)
		if err != nil {
			log.Error("copy template", "error", err)
			os.Exit(2)
		}
		kube, err := reconcile.NewKube()
		if err != nil {
			log.Error("kubernetes", "error", err)
			os.Exit(2)
		}
		r.Copies = &reconcile.Copies{Kube: kube, Nessie: r.Nessie, Template: t, CommitEvery: num("COPY_COMMIT_EVERY", 30)}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// On Kubernetes, Buckets' drives grown to each pool's size (BUCKETS_DRIVES:
	// {"pool-0": "40Gi"}), every five minutes: the chart keeps the claim
	// templates they were made with, which Kubernetes never lets change.
	if sizes := os.Getenv("BUCKETS_DRIVES"); sizes != "" {
		d := &reconcile.Drives{Cluster: env("BUCKETS_CLUSTER", "buckets"), Log: log}
		if err := json.Unmarshal([]byte(sizes), &d.Sizes); err != nil {
			log.Error("BUCKETS_DRIVES", "error", err)
			os.Exit(2)
		}
		kube, err := reconcile.NewKube()
		if err != nil {
			log.Error("kubernetes", "error", err)
			os.Exit(2)
		}
		d.Kube = kube
		go func() {
			for {
				if _, err := d.Grow(ctx); err != nil {
					log.Warn("buckets drives", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Minute):
				}
			}
		}()
	}
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
		// platformd, at a GitLab merge request's event: apply the project now (its reviews).
		mux.HandleFunc("POST /projects/{name}/kick", func(w http.ResponseWriter, req *http.Request) {
			r.Kick(req.PathValue("name"))
			w.WriteHeader(http.StatusAccepted)
		})
		// Every Airflow makes a DAG of each approved delivery it runs (storscale_deliveries.py, the bundle storscale).
		mux.HandleFunc("GET /airflow/deliveries", func(w http.ResponseWriter, req *http.Request) {
			ds, err := r.Deliveries(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ds)
		})
		// The buckets' sizes, for usage per project (13.1): platformd's daily sample.
		mux.HandleFunc("GET /buckets/sizes", func(w http.ResponseWriter, req *http.Request) {
			sizes, err := r.Buckets.BucketSizes(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(sizes)
		})
		// Every Airflow makes a DAG of each agent it runs on a schedule (storscale_agents.py, the bundle storscale).
		mux.HandleFunc("GET /airflow/agents", func(w http.ResponseWriter, req *http.Request) {
			as, err := r.Agents(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(as)
		})
		// Every Airflow makes a DAG of each scoring (storscale_scoring.py, the bundle storscale).
		mux.HandleFunc("GET /airflow/scoring", func(w http.ResponseWriter, req *http.Request) {
			ss, err := r.Scorings(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ss)
		})
		// The shared model server (Compose) serves each project's servings.
		mux.HandleFunc("GET /serving", func(w http.ResponseWriter, req *http.Request) {
			ss, err := r.Servings(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ss)
		})
		// Every Airflow makes a DAG of each knowledge bank it builds on a schedule (storscale_knowledge.py, the bundle storscale).
		mux.HandleFunc("GET /airflow/knowledge", func(w http.ResponseWriter, req *http.Request) {
			bs, err := r.KnowledgeBanks(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(bs)
		})
		// The platform's Airflow makes a DAG of each project's import (stack/airflow/dags/storscale_imports.py).
		mux.HandleFunc("GET /airflow/imports", func(w http.ResponseWriter, req *http.Request) {
			imports, err := r.Imports(req.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(imports)
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
