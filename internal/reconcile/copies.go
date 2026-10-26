package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/StorScale/dataworks/internal/project"
)

// Copies copies projects' sources into their tables (use case 8.1), on
// Kubernetes: a Deployment per copying source, from the chart's template
// (templates/copy.yaml), running Debezium Server with its Iceberg sink. It
// reads the database's change log (a first copy, then every change) and
// writes the project's Iceberg tables itself, through Nessie's REST catalog:
// <source>_<table> in the project's namespace, keyed by each table's primary
// key (upserts, deletes applied), new columns added as they come. Its
// offsets and schema history are Iceberg tables too, in a namespace of the
// platform's (StateNamespace), which no project's policies name.
type Copies struct {
	Kube     *Kube
	Nessie   *Nessie
	Template map[string]any // the Deployment, with @NAME@, @PROJECT@, @SOURCE@, @CONFIG@
	// CommitEvery is the most seconds between a change and its commit.
	CommitEvery int
}

// StateNamespace holds the copies' offsets and schema history.
const StateNamespace = "storscale_cdc"

// LoadCopyTemplate reads the chart's template (JSON) from a file.
func LoadCopyTemplate(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t struct {
		Deployment map[string]any `json:"deployment"`
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if t.Deployment == nil {
		return nil, fmt.Errorf("%s: no deployment", path)
	}
	return t.Deployment, nil
}

func copyLabels(p *project.Project, s project.Source) map[string]string {
	return map[string]string{"app.kubernetes.io/part-of": "storscale-platform", "app.kubernetes.io/managed-by": "platform-operator",
		"app.kubernetes.io/name": "storscale-copy", "platform.storscale.io/project": p.Metadata.Name,
		"platform.storscale.io/source": s.Name}
}

// identifier is a name the source database takes for a slot or publication:
// lowercase, at most 63 characters (a hash in the end when longer).
func identifier(name string) string {
	if len(name) <= 63 {
		return name
	}
	h := sha256.Sum256([]byte(name))
	return name[:54] + "_" + hex.EncodeToString(h[:])[:8]
}

// properties are Debezium Server's settings (application.properties) for a
// source: its connector, the source's tables, and the Iceberg sink.
func (c *Copies) properties(p *project.Project, s project.Source, conn map[string]string) (string, error) {
	host, port, db := conn["host"], conn["port"], conn["database"]
	if host == "" || conn["username"] == "" || conn["password"] == "" {
		return "", errors.New("copying needs its connection's host, username and password (a url alone won't do)")
	}
	cat := p.SourceCatalog(s)
	set := map[string]string{
		// The source.
		"debezium.source.database.hostname":  host,
		"debezium.source.database.user":      conn["username"],
		"debezium.source.database.password":  conn["password"],
		"debezium.source.topic.prefix":       cat,
		"debezium.source.table.include.list": strings.Join(s.Copy.Tables, ","),
		"debezium.source.snapshot.mode":      "initial",
		// Numbers exact (decimals as decimals, not doubles: money).
		"debezium.source.decimal.handling.mode":                           "precise",
		"debezium.source.offset.flush.interval.ms":                        "0",
		"debezium.source.offset.storage":                                  "io.debezium.server.iceberg.offset.IcebergOffsetBackingStore",
		"debezium.source.offset.storage.iceberg.table-namespace":          StateNamespace,
		"debezium.source.offset.storage.iceberg.table-name":               cat + "_offsets",
		"debezium.source.schema.history.internal":                         "io.debezium.server.iceberg.history.IcebergSchemaHistory",
		"debezium.source.schema.history.internal.iceberg.table-namespace": StateNamespace,
		"debezium.source.schema.history.internal.iceberg.table-name":      cat + "_history",
		// Into Iceberg: the project's namespace, through Nessie's REST catalog.
		"debezium.sink.type":                         "iceberg",
		"debezium.sink.iceberg.type":                 "rest",
		"debezium.sink.iceberg.uri":                  "http://nessie:19120/iceberg/",
		"debezium.sink.iceberg.warehouse":            "warehouse",
		"debezium.sink.iceberg.catalog-name":         "storscale",
		"debezium.sink.iceberg.io-impl":              "org.apache.iceberg.aws.s3.S3FileIO",
		"debezium.sink.iceberg.s3.endpoint":          "http://buckets:9000",
		"debezium.sink.iceberg.s3.path-style-access": "true",
		"debezium.sink.iceberg.client.region":        "us-east-1",
		"debezium.sink.iceberg.table-namespace":      p.Spec.Tables.Namespace,
		"debezium.sink.iceberg.table-prefix":         s.Name + "_",
		// <prefix>.<schema>.<table>: the table's own name (an empty
		// replacement is refused, as no setting at all).
		"debezium.sink.iceberg.destination-regexp":                "^.*[.]([^.]+)$",
		"debezium.sink.iceberg.destination-regexp-replace":        "$1",
		"debezium.sink.iceberg.destination-lowercase-table-names": "true",
		"debezium.sink.iceberg.upsert":                            "true",
		"debezium.sink.iceberg.upsert-keep-deletes":               "false",
		"debezium.sink.iceberg.allow-field-addition":              "true",
		"debezium.sink.iceberg.format-version":                    "2", // equality deletes, which Trino reads
		"debezium.sink.batch.batch-size-wait":                     "MaxBatchSizeWait",
		"debezium.sink.batch.batch-size-wait.max-wait-ms":         strconv.Itoa(max(c.CommitEvery, 5) * 1000),
		"debezium.sink.batch.batch-size-wait.wait-interval-ms":    "5000",
		"quarkus.log.level":                                       "INFO",
		"quarkus.log.console.json":                                "false",
	}
	or := func(v, def string) string {
		if v != "" {
			return v
		}
		return def
	}
	switch s.Kind {
	case "postgresql":
		if db == "" {
			return "", errors.New("a PostgreSQL source names its database")
		}
		set["debezium.source.connector.class"] = "io.debezium.connector.postgresql.PostgresConnector"
		set["debezium.source.database.port"] = or(port, "5432")
		set["debezium.source.database.dbname"] = db
		set["debezium.source.plugin.name"] = "pgoutput"
		// Its own replication slot and publication, of the tables it copies.
		set["debezium.source.slot.name"] = identifier("storscale_" + cat)
		set["debezium.source.publication.name"] = identifier("storscale_" + cat)
		set["debezium.source.publication.autocreate.mode"] = "filtered"
		if m := conn["sslmode"]; m != "" {
			set["debezium.source.database.sslmode"] = m
		}
	case "mysql":
		set["debezium.source.connector.class"] = "io.debezium.connector.mysql.MySqlConnector"
		set["debezium.source.database.port"] = or(port, "3306")
		h := fnv.New32a()
		h.Write([]byte(cat))
		set["debezium.source.database.server.id"] = strconv.Itoa(5400 + int(h.Sum32()%1000000)) // its own, among the server's replicas
		dbs := map[string]bool{}
		for _, t := range s.Copy.Tables {
			d, _, _ := strings.Cut(t, ".")
			dbs[d] = true
		}
		set["debezium.source.database.include.list"] = strings.Join(sortedKeys(dbs), ",")
	case "sqlserver":
		if db == "" {
			return "", errors.New("a SQL Server source names its database")
		}
		set["debezium.source.connector.class"] = "io.debezium.connector.sqlserver.SqlServerConnector"
		set["debezium.source.database.port"] = or(port, "1433")
		set["debezium.source.database.names"] = db
		set["debezium.source.database.encrypt"] = "true"
		if conn["trustServerCertificate"] == "true" {
			set["debezium.source.database.trustServerCertificate"] = "true"
		}
	default:
		return "", fmt.Errorf("a %s source can't be copied", s.Kind)
	}
	var b strings.Builder
	for _, k := range sortedKeys(set) {
		b.WriteString(k + "=" + propValue(set[k]) + "\n")
	}
	return b.String(), nil
}

// Apply makes the project's copying sources' Deployments, and removes those
// of sources that no longer copy.
func (c *Copies) Apply(ctx context.Context, p *project.Project, conns func(name string) (map[string]string, error)) error {
	var errs []error
	want := map[string]bool{}
	for _, s := range p.Spec.Sources {
		if s.Copy == nil {
			continue
		}
		want[p.CopyName(s)] = true
		if err := c.apply(ctx, p, s, conns); err != nil {
			errs = append(errs, fmt.Errorf("copying %s: %w", s.Name, err))
		}
	}
	have, err := c.Kube.List(ctx, "deployments", "app.kubernetes.io/name=storscale-copy,platform.storscale.io/project="+p.Metadata.Name)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, name := range have {
		if !want[name] {
			errs = append(errs, c.remove(ctx, name))
		}
	}
	return errors.Join(errs...)
}

// Remove removes every copy of the project's (its project went).
func (c *Copies) Remove(ctx context.Context, p *project.Project) error {
	have, err := c.Kube.List(ctx, "deployments", "app.kubernetes.io/name=storscale-copy,platform.storscale.io/project="+p.Metadata.Name)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range have {
		errs = append(errs, c.remove(ctx, name))
	}
	return errors.Join(errs...)
}

// remove stops a copy. Its tables stay (they're the project's), and so does
// its place in the source's log (a PostgreSQL replication slot is the
// database administrator's to drop: until then, the database keeps its log).
func (c *Copies) remove(ctx context.Context, name string) error {
	if err := c.Kube.Delete(ctx, "deployments", name); err != nil {
		return err
	}
	return c.Kube.Delete(ctx, "secrets", name)
}

func (c *Copies) apply(ctx context.Context, p *project.Project, s project.Source, conns func(name string) (map[string]string, error)) error {
	conn, err := conns(s.Connection)
	if err == nil && conn == nil {
		err = fmt.Errorf("no connection %q", s.Connection)
	}
	if err != nil {
		return err
	}
	props, err := c.properties(p, s, conn)
	if err != nil {
		return err
	}
	if err := c.Nessie.ensureNamespace(ctx, StateNamespace); err != nil {
		return err
	}
	name := p.CopyName(s)
	if err := c.Kube.PutSecret(ctx, name, copyLabels(p, s), map[string]string{"application.properties": props}); err != nil {
		return err
	}
	h := sha256.Sum256([]byte(props))
	repl := strings.NewReplacer("@NAME@", name, "@PROJECT@", p.Metadata.Name, "@SOURCE@", s.Name,
		"@CONFIG@", hex.EncodeToString(h[:])[:16])
	if err := c.Kube.Apply(ctx, "deployments", fill(c.Template, repl).(map[string]any)); err != nil {
		return err
	}
	ready, err := c.Kube.Ready(ctx, name)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("starting (its first copy of each table comes first)")
	}
	return nil
}

// PutSecret makes a Secret hold this data (the operator's own Secrets).
func (k *Kube) PutSecret(ctx context.Context, name string, labels, data map[string]string) error {
	var sec map[string]any
	found, err := k.do(ctx, "GET", k.path("secrets", name), "", nil, &sec)
	if err != nil {
		return err
	}
	enc := map[string]string{}
	for key, v := range data {
		enc[key] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	if !found {
		body := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
			"metadata": map[string]any{"name": name, "labels": labels}, "data": enc}
		_, err := k.do(ctx, "POST", k.path("secrets", ""), "application/json", body, nil)
		return err
	}
	if old, _ := sec["data"].(map[string]any); sameData(old, enc) {
		return nil
	}
	sec["data"] = enc // its resourceVersion kept: a write over another's fails
	_, err = k.do(ctx, "PUT", k.path("secrets", name), "application/json", sec, nil)
	return err
}

func sameData(old map[string]any, enc map[string]string) bool {
	if len(old) != len(enc) {
		return false
	}
	for k, v := range enc {
		if s, _ := old[k].(string); s != v {
			return false
		}
	}
	return true
}

// List names the objects of a kind with these labels (a label selector).
func (k *Kube) List(ctx context.Context, kind, selector string) ([]string, error) {
	var l struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if _, err := k.do(ctx, "GET", k.path(kind, "")+"?labelSelector="+url.QueryEscape(selector), "", nil, &l); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range l.Items {
		out = append(out, it.Metadata.Name)
	}
	sort.Strings(out)
	return out, nil
}
