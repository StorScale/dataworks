package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Models in the catalog's lineage (use case 15.1, R15.6): catalog-sync puts
// each project's registered models in OpenMetadata, with what made them and
// what they make, and asks here for them, since only platformd reaches
// MLflow. For each model: its card, its approved version, who registered and
// who approved it, and the run that trained it, with the catalog's Nessie
// commit when it started (the data it read: every table at its snapshot then);
// and the tables it read (buckets_lake's lake.tables tag), each at its
// Iceberg snapshot then; for each scoring, its model, its input and output
// tables, and the input's Iceberg snapshot at that commit.

// catalogModelVersion is a model's approved version, as the catalog shows it.
type catalogModelVersion struct {
	Version      string            `json:"version"`
	Source       string            `json:"source,omitempty"` // where MLflow keeps it
	Created      int64             `json:"created"`
	RegisteredBy string            `json:"registeredBy,omitempty"`
	ApprovedBy   string            `json:"approvedBy,omitempty"`
	ApprovedAt   string            `json:"approvedAt,omitempty"`
	RunID        string            `json:"runId,omitempty"`
	RunBy        string            `json:"runBy,omitempty"`      // the run's storscale.user
	DataCommit   string            `json:"dataCommit,omitempty"` // the run's storscale.data_commit
	Started      int64             `json:"started,omitempty"`    // the run's start, ms since the epoch
	Algorithm    string            `json:"algorithm,omitempty"`  // the estimator, as autologging names it
	Params       map[string]string `json:"params,omitempty"`     // the run's parameters (at most 50)
	// What the run read (its lake.tables, as buckets_lake.sql() recorded
	// it), each at its snapshot at DataCommit.
	Tables []catalogTrainingTable `json:"tables,omitempty"`
}

// catalogTrainingTable is a table a model's training read: catalog.schema.table,
// and its Iceberg snapshot at the run's data commit (0: not there, not known,
// or not a table of the Nessie catalog).
type catalogTrainingTable struct {
	Table    string `json:"table"`
	Snapshot int64  `json:"snapshot,omitempty"`
}

// lakeTablesTag is the tag buckets_lake puts on a run: the tables its
// notebook read, catalog.schema.table, comma-separated.
const lakeTablesTag = "lake.tables"

// maxTrainingTables is the most of a run's tables the catalog is told of.
const maxTrainingTables = 100

type catalogModel struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"` // its card (R15.5)
	Updated     int64                `json:"updated"`
	Versions    int                  `json:"versions"`
	Approved    *catalogModelVersion `json:"approved,omitempty"` // none: no version approved yet
}

type catalogScoring struct {
	Name     string   `json:"name"`
	Model    string   `json:"model"`
	Table    string   `json:"table"`
	Columns  []string `json:"columns,omitempty"`
	Output   string   `json:"output"`
	Schedule string   `json:"schedule"`
	// The input's Iceberg snapshot at the approved version's data commit
	// (0: the table wasn't there, or isn't known).
	InputSnapshot int64 `json:"inputSnapshot,omitempty"`
}

type catalogProject struct {
	Name      string           `json:"name"`
	Catalog   string           `json:"catalog"`
	Namespace string           `json:"namespace"`
	Error     string           `json:"error,omitempty"` // its models couldn't be read: keep what the catalog has
	Models    []catalogModel   `json:"models"`
	Scorings  []catalogScoring `json:"scorings"`
}

// catalogSyncAccount is who catalog-sync is: the operator's service account
// (it signs in to OpenMetadata, as an administrator there, with it too).
func catalogSyncAccount() string {
	return "service-account-" + firstOf(os.Getenv("OPERATOR_CLIENT_ID"), "platform-operator")
}

// handleCatalogModels is every project's registered models and scorings, for
// catalog-sync (the operator's service account, by its Keycloak token): each
// model's card, its approved version with who registered and approved it, the
// run that trained it and the catalog's commit then; each scoring's tables
// and its input's snapshot at that commit. A project whose models can't be
// read says so (error), so the catalog keeps what it has of them.
func (s *server) handleCatalogModels(w http.ResponseWriter, r *http.Request) {
	who, err := s.bearer(r)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, err.Error())
		return
	}
	if who != catalogSyncAccount() {
		s.fail(w, r, http.StatusForbidden, "the catalog's own: catalog-sync's")
		return
	}
	m := s.mlflow()
	if m == nil || s.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "projects": []any{}})
		return
	}
	specs, err := s.store.Projects(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	out := []catalogProject{}
	for _, p := range specs {
		if p == nil {
			continue
		}
		cp := catalogProject{Name: p.Metadata.Name, Catalog: "iceberg", Namespace: strings.ReplaceAll(p.Metadata.Name, "-", "_"),
			Models: []catalogModel{}, Scorings: []catalogScoring{}}
		if t := p.Spec.Tables; t != nil {
			cp.Catalog, cp.Namespace = firstOf(t.Catalog, cp.Catalog), firstOf(t.Namespace, cp.Namespace)
		}
		models, err := s.catalogModelsOf(r.Context(), m, cp.Name)
		if err != nil {
			cp.Error = err.Error()
		}
		cp.Models = models
		commits := map[string]string{} // model -> its approved version's data commit
		for _, x := range models {
			if x.Approved != nil {
				commits[x.Name] = x.Approved.DataCommit
			}
		}
		for _, sc := range p.Spec.Scoring {
			c := catalogScoring{Name: sc.Name, Model: sc.Model, Table: sc.Table, Columns: sc.Columns, Output: sc.Output, Schedule: sc.Schedule}
			if commit := commits[sc.Model]; commit != "" && cp.Catalog == "iceberg" {
				c.InputSnapshot = s.snapshotAt(r.Context(), commit, cp.Namespace+"."+sc.Table)
			}
			cp.Scorings = append(cp.Scorings, c)
		}
		out = append(out, cp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "projects": out})
}

// catalogModelsOf are a project's registered models, each with its approved
// version and that version's run; none when the project has no workspace yet.
func (s *server) catalogModelsOf(ctx context.Context, m *mlflowProxy, name string) ([]catalogModel, error) {
	out := []catalogModel{}
	token := ""
	for {
		var page struct {
			Models []struct {
				Name        string                            `json:"name"`
				Description string                            `json:"description"`
				Updated     int64                             `json:"last_updated_timestamp"`
				Aliases     []struct{ Alias, Version string } `json:"aliases"`
			} `json:"registered_models"`
			Next string `json:"next_page_token"`
		}
		path := "/api/2.0/mlflow/registered-models/search?max_results=200"
		if token != "" {
			path += "&page_token=" + url.QueryEscape(token)
		}
		err := m.call(ctx, name, "GET", path, nil, &page)
		var me *mlflowError
		if asMLflow(err, &me) && (me.code == "RESOURCE_DOES_NOT_EXIST" || me.status == 404) {
			return out, nil // no workspace: nothing registered
		}
		if err != nil {
			return out, err
		}
		for _, x := range page.Models {
			cm := catalogModel{Name: x.Name, Description: x.Description, Updated: x.Updated}
			var count struct {
				Versions []struct{} `json:"model_versions"`
			}
			filter := url.QueryEscape("name='" + strings.ReplaceAll(x.Name, "'", "''") + "'")
			if err := m.call(ctx, name, "GET", "/api/2.0/mlflow/model-versions/search?max_results=200&filter="+filter, nil, &count); err != nil {
				return out, err
			}
			cm.Versions = len(count.Versions)
			for _, a := range x.Aliases {
				if a.Alias != "approved" {
					continue
				}
				v, err := s.catalogVersion(ctx, m, name, x.Name, a.Version)
				if err != nil {
					return out, err
				}
				cm.Approved = v
			}
			out = append(out, cm)
		}
		if token = page.Next; token == "" {
			return out, nil
		}
	}
}

// catalogVersion is a model's version, with its run's stamps and parameters.
func (s *server) catalogVersion(ctx context.Context, m *mlflowProxy, workspace, model, version string) (*catalogModelVersion, error) {
	var got struct {
		Version struct {
			Source  string                        `json:"source"`
			RunID   string                        `json:"run_id"`
			Created int64                         `json:"creation_timestamp"`
			Tags    []struct{ Key, Value string } `json:"tags"`
		} `json:"model_version"`
	}
	if err := m.call(ctx, workspace, "GET", "/api/2.0/mlflow/model-versions/get?name="+url.QueryEscape(model)+"&version="+url.QueryEscape(version), nil, &got); err != nil {
		return nil, fmt.Errorf("%s version %s: %w", model, version, err)
	}
	v := &catalogModelVersion{Version: version, Source: got.Version.Source, Created: got.Version.Created, RunID: got.Version.RunID}
	for _, t := range got.Version.Tags {
		switch t.Key {
		case "storscale.registered_by":
			v.RegisteredBy = t.Value
		case "storscale.approved_by":
			v.ApprovedBy = t.Value
		case "storscale.approved_at":
			v.ApprovedAt = t.Value
		}
	}
	if v.RunID == "" {
		return v, nil
	}
	var run struct {
		Run struct {
			Info struct {
				Started int64 `json:"start_time"`
			} `json:"info"`
			Data struct {
				Tags   []struct{ Key, Value string } `json:"tags"`
				Params []struct{ Key, Value string } `json:"params"`
			} `json:"data"`
		} `json:"run"`
	}
	err := m.call(ctx, workspace, "GET", "/api/2.0/mlflow/runs/get?run_id="+url.QueryEscape(v.RunID), nil, &run)
	var me *mlflowError
	if asMLflow(err, &me) && (me.code == "RESOURCE_DOES_NOT_EXIST" || me.status == 404) {
		return v, nil // the run was deleted: the version stays
	}
	if err != nil {
		return nil, fmt.Errorf("%s version %s's run: %w", model, version, err)
	}
	v.Started = run.Run.Info.Started
	estimator, read := "", ""
	for _, t := range run.Run.Data.Tags {
		switch t.Key {
		case "storscale.user":
			v.RunBy = t.Value
		case "storscale.data_commit":
			v.DataCommit = t.Value
		case "estimator_class":
			v.Algorithm = t.Value
		case "estimator_name":
			estimator = t.Value
		case lakeTablesTag:
			read = t.Value
		}
	}
	v.Algorithm = firstOf(estimator, v.Algorithm)
	v.Tables = s.trainingTables(ctx, read, v.DataCommit)
	for _, p := range run.Run.Data.Params {
		if len(v.Params) >= 50 {
			break
		}
		if v.Params == nil {
			v.Params = map[string]string{}
		}
		v.Params[p.Key] = p.Value
	}
	return v, nil
}

// trainingTables are a run's lake.tables, each with its snapshot at the
// run's data commit when it's a table of the Nessie catalog (iceberg).
func (s *server) trainingTables(ctx context.Context, tag, commit string) []catalogTrainingTable {
	var out []catalogTrainingTable
	seen := map[string]bool{}
	for _, name := range strings.Split(tag, ",") {
		name = strings.TrimSpace(name)
		parts := strings.Split(name, ".")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || seen[name] {
			continue
		}
		if len(out) == maxTrainingTables {
			break
		}
		seen[name] = true
		t := catalogTrainingTable{Table: name}
		if commit != "" && parts[0] == "iceberg" {
			t.Snapshot = s.snapshotAt(ctx, commit, parts[1]+"."+parts[2])
		}
		out = append(out, t)
	}
	return out
}

// snapshotAt is a table's Iceberg snapshot at a commit of the catalog (key:
// namespace.table), as Nessie keeps it; 0 when it wasn't there or Nessie
// can't say.
func (s *server) snapshotAt(ctx context.Context, commit, key string) int64 {
	if s.history == nil || s.history.nessie == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n := s.history.nessie
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(n.Base, "/")+"/trees/"+url.PathEscape("main@"+commit)+"/contents/"+url.PathEscape(key), nil)
	if err != nil {
		return 0
	}
	res, err := n.HTTP.Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0
	}
	var c struct {
		Content struct {
			Type       string `json:"type"`
			SnapshotID int64  `json:"snapshotId"`
		} `json:"content"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&c) != nil || c.Content.Type != "ICEBERG_TABLE" {
		return 0
	}
	return c.Content.SnapshotID
}
