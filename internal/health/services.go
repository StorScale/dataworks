package health

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Services are the platform's own, as Compose and the chart both name them.
func Services() []Check {
	// On Kubernetes the tools' databases are one Postgres (postgres), each
	// name a Service of its; Ranger's is its own.
	db := func(name, label string, features ...string) Check {
		sel := "app.kubernetes.io/name=postgres"
		if name == "ranger-db" {
			sel = ""
		}
		return Check{Name: name, Label: label, URL: "tcp://" + name + ":5432", Features: features, Selector: sel}
	}
	return []Check{
		{Name: "gateway", Label: "Gateway", URL: "http://gateway:8800/healthz", Features: []string{"every page and tool"}},
		{Name: "keycloak", Label: "Sign-in (Keycloak)", URL: "http://keycloak:9000/health/ready", Features: []string{"signing in", "every tool"}},
		db("keycloak-db", "Sign-in's database", "signing in"),
		{Name: "buckets", Label: "Storage (Buckets)", URL: "http://buckets:9000/minio/health/live", Features: []string{"tables", "files", "deliveries"},
			Selector: "buckets.io/cluster=buckets"},
		{Name: "nessie", Label: "Table catalog (Nessie)", URL: "http://nessie:19120/api/v2/config", Features: []string{"tables"}},
		{Name: "trino", Label: "Query engine (Trino)", URL: "http://trino:8080/v1/info", OK: trinoOK, Features: []string{"SQL", "dashboards", "metrics", "pipelines' queries"}},
		db("trino-db", "Query engine's limits", "SQL", "dashboards", "metrics"),
		{Name: "ranger-admin", Label: "Access rules (Ranger)", URL: "http://ranger-admin:6080/service/public/v2/api/servicedef", OK: answersSignIn,
			Features: []string{"changes to access rules"}},
		db("ranger-db", "Access rules' database", "changes to access rules"),
		{Name: "semanticd", Label: "Metrics and SQL service", URL: "http://semanticd:8000/healthz", Features: []string{"the SQL page", "metrics", "AI assistants"}},
		{Name: "platform-operator", Label: "Project operator", URL: "http://platform-operator:8081/healthz", Features: []string{"changes to projects and their access"}},
		{Name: "superset", Label: "Dashboards (Superset)", URL: "http://superset:8088/dashboards/health", Features: []string{"dashboards"}},
		db("superset-db", "Dashboards' database", "dashboards"),
		{Name: "airflow", Label: "Pipelines (Airflow)", URL: "http://airflow:8090/pipelines/api/v2/monitor/health", OK: airflowOK("metadatabase"),
			Features: []string{"pipelines"}},
		{Name: "airflow-scheduler", Label: "Pipelines' scheduler", URL: "http://airflow:8090/pipelines/api/v2/monitor/health", OK: airflowOK("scheduler"),
			Features: []string{"scheduled and started pipeline runs"}},
		{Name: "airflow-dag-processor", Label: "Pipelines' loader", URL: "http://airflow:8090/pipelines/api/v2/monitor/health", OK: airflowOK("dag_processor"),
			Features: []string{"new and changed pipelines"}},
		db("airflow-db", "Pipelines' database", "pipelines"),
		{Name: "jupyterhub", Label: "Notebooks (JupyterHub)", URL: "http://jupyterhub:8000/notebooks/hub/health", Features: []string{"notebooks"}},
		{Name: "openmetadata", Label: "Catalog (OpenMetadata)", URL: "http://openmetadata:8585/api/v1/system/version", Features: []string{"lineage", "data checks", "search"}},
		{Name: "openmetadata-search", Label: "Catalog search", URL: "http://openmetadata-search:9200/_cluster/health", OK: searchOK, Features: []string{"search", "the catalog"}},
		db("openmetadata-db", "Catalog's database", "the catalog"),
		{Name: "prometheus", Label: "Monitoring (Prometheus)", URL: "http://prometheus:9090/-/ready", Features: []string{"monitoring", "storage alerts"}},
		{Name: "grafana", Label: "Monitoring dashboards (Grafana)", URL: "http://grafana:3000/monitoring/api/health", Features: []string{"monitoring dashboards"}},
	}
}

func trinoOK(status int, body string) (bool, string) {
	if status != 200 {
		return false, fmt.Sprintf("its health check answers HTTP %d", status)
	}
	if strings.Contains(body, `"starting":true`) {
		return false, "it's starting"
	}
	return true, ""
}

// answersSignIn: Ranger's API is up when it asks for a sign-in.
func answersSignIn(status int, _ string) (bool, string) {
	if status == 401 || status/100 == 2 {
		return true, ""
	}
	return false, fmt.Sprintf("its API answers HTTP %d", status)
}

// airflowOK reads one part of Airflow's health: metadatabase, scheduler, dag_processor.
func airflowOK(part string) func(int, string) (bool, string) {
	return func(status int, body string) (bool, string) {
		if status != 200 {
			return false, fmt.Sprintf("Airflow's health check answers HTTP %d", status)
		}
		var h map[string]struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(body), &h) != nil {
			return false, "Airflow's health check answers something else"
		}
		if s := h[part].Status; s != "healthy" {
			if s == "" {
				s = "not reporting"
			}
			return false, "Airflow says it's " + s
		}
		return true, ""
	}
}

func searchOK(status int, body string) (bool, string) {
	if status != 200 {
		return false, fmt.Sprintf("its health check answers HTTP %d", status)
	}
	if strings.Contains(body, `"status":"red"`) {
		return false, "its index is red: some data isn't available"
	}
	return true, ""
}
