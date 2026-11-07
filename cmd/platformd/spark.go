package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/StorScale/dataworks/internal/catalog"
	"github.com/StorScale/dataworks/internal/trino"
)

// Spark's credentials (notebooks' Distributed Spark profile): Spark reads and
// writes the tables' files itself, not through Trino, so Ranger can't filter
// rows or mask columns for it. People's own Buckets credentials never reach
// the warehouse. Instead the platform asks Trino, as the person, which tables
// they read whole (a plain SELECT * of it is just the table: no row filter,
// no mask) and which of those they may also change (insert and delete), and
// gives a Spark session credentials for those tables' files alone: Buckets'
// AssumeRole on the platform's Spark account (the warehouse), cut down by a
// session policy to those folders, for a few hours.

const (
	sparkCatalog      = "iceberg"
	sparkPolicyMax    = 2048 // bytes: the most of a session policy Buckets (MinIO) takes
	sparkChecksAtOnce = 8
)

type sparkTable struct {
	Name    string   `json:"name"` // namespace.table
	Write   bool     `json:"write"`
	folders []string // s3://bucket/path
}

type sparkSkipped struct {
	Name string `json:"name"`
	Why  string `json:"why"`
}

// sparkFull is whether a plan for SELECT * of a table, as Trino plans it for
// the person, is only the table: any filter (a row filter, even one pushed into
// the scan as a constraint) or projection (a column mask) means they don't read
// it whole. Anything this doesn't know counts as not whole.
func sparkFull(plan string) (bool, string) {
	var root any
	if err := json.Unmarshal([]byte(plan), &root); err != nil {
		return false, "its plan isn't one this reads"
	}
	why := ""
	var walk func(n any)
	walk = func(n any) {
		m, ok := n.(map[string]any)
		if !ok || why != "" {
			return
		}
		name, has := m["name"].(string)
		if !has { // a fragment's map (a distributed plan): each fragment's root
			for _, v := range m {
				walk(v)
			}
			return
		}
		switch name {
		case "Output":
		case "TableScan":
			if d, _ := json.Marshal(m["descriptor"]); strings.Contains(strings.ToLower(string(d)), "constraint") {
				why = "a row filter applies to you"
			}
		case "Filter", "ScanFilter":
			why = "a row filter applies to you"
		case "Project", "ScanProject", "ScanFilterProject":
			why = "a column mask or row filter applies to you"
		default:
			why = "its plan has " + name
		}
		if kids, _ := m["children"].([]any); why == "" {
			for _, k := range kids {
				walk(k)
			}
		}
	}
	walk(root)
	return why == "", why
}

// sparkEmpty is whether a plan is only Output over Values: Trino's plan for an
// empty table, which says nothing of filters or masks.
func sparkEmpty(plan string) bool {
	var m map[string]any
	if json.Unmarshal([]byte(plan), &m) != nil || m["name"] != "Output" {
		return false
	}
	kids, _ := m["children"].([]any)
	if len(kids) != 1 {
		return false
	}
	k, _ := kids[0].(map[string]any)
	grand, _ := k["children"].([]any)
	return k["name"] == "Values" && len(grand) == 0
}

// sparkCheck is what the person may do with one table from Spark: read it
// whole, and also change it; or why not. fresh: they've just made it, so it's
// empty, and an empty table's plan (Values) counts as whole.
func sparkCheck(ctx context.Context, tc *trino.Client, ns, table string, fresh bool) (read, write bool, why string) {
	name := trino.Quote(sparkCatalog) + "." + trino.Quote(ns) + "." + trino.Quote(table)
	rows, err := tc.Exec(ctx, "EXPLAIN (TYPE LOGICAL, FORMAT JSON) SELECT * FROM "+name)
	if err != nil {
		return false, false, "you can't read all of it: " + err.Error()
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return false, false, "Trino gave no plan for it"
	}
	plan, _ := rows[0][0].(string)
	if ok, why := sparkFull(plan); !ok && !(fresh && sparkEmpty(plan)) {
		return false, false, why
	}
	for _, stmt := range []string{"INSERT INTO " + name + " SELECT * FROM " + name + " WHERE false",
		"DELETE FROM " + name + " WHERE false"} {
		if _, err := tc.Exec(ctx, "EXPLAIN (TYPE VALIDATE) "+stmt); err != nil {
			return true, false, ""
		}
	}
	return true, true, ""
}

// sparkPolicy is the session policy for these tables' folders: their objects
// read, and changed where the table may be.
func sparkPolicy(tables []sparkTable) ([]byte, error) {
	var read, write []string
	for _, t := range tables {
		for _, f := range t.folders {
			path, ok := strings.CutPrefix(f, "s3://")
			if !ok {
				path, ok = strings.CutPrefix(f, "s3a://")
			}
			if !ok || !strings.Contains(path, "/") {
				return nil, fmt.Errorf("table %s: its folder %s isn't in a bucket", t.Name, f)
			}
			arn := "arn:aws:s3:::" + strings.TrimRight(path, "/") + "/*"
			read = append(read, arn)
			if t.Write {
				write = append(write, arn)
			}
		}
	}
	type statement struct {
		Effect   string   `json:"Effect"`
		Action   []string `json:"Action"`
		Resource []string `json:"Resource"`
	}
	policy := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{Version: "2012-10-17", Statement: []statement{{"Allow", []string{"s3:GetObject"}, read}}}
	if len(write) > 0 {
		policy.Statement = append(policy.Statement, statement{"Allow",
			[]string{"s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}, write})
	}
	return json.Marshal(policy)
}

// sparkNew is a table a Spark session makes: Trino makes it, as the person
// (so Ranger decides whether they may), before the session's credentials are
// cut, so its folder is among theirs. Spark can't: it would commit to Nessie
// itself, past Ranger, into a folder the credentials couldn't name in advance.
type sparkNew struct {
	Name    string `json:"name"` // namespace.table
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"` // Trino's: bigint, varchar, decimal(10,2), array(row(a integer)), ...
	} `json:"columns"`
}

var (
	sparkIdentRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,127}$`)
	sparkTypeRE  = regexp.MustCompile(`^[a-z][a-z0-9_(), ]{0,500}$`) // Trino types: no quotes, no statements
)

// sparkCreate is the CREATE TABLE for a new table, or why it's not one.
func sparkCreate(t sparkNew) (string, error) {
	ns, table, ok := strings.Cut(t.Name, ".")
	if !ok || !sparkIdentRE.MatchString(ns) || !sparkIdentRE.MatchString(table) {
		return "", fmt.Errorf("%q: a new table is namespace.table, lowercase letters, digits and underscores", t.Name)
	}
	if len(t.Columns) == 0 {
		return "", fmt.Errorf("%s: a new table needs its columns", t.Name)
	}
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		ty := strings.ToLower(strings.TrimSpace(c.Type))
		if c.Name == "" || !sparkTypeRE.MatchString(ty) || strings.Count(ty, "(") != strings.Count(ty, ")") {
			return "", fmt.Errorf("%s: column %q of type %q isn't one Trino takes here", t.Name, c.Name, c.Type)
		}
		cols[i] = trino.Quote(c.Name) + " " + ty
	}
	return "CREATE TABLE IF NOT EXISTS " + trino.Quote(sparkCatalog) + "." + trino.Quote(ns) + "." + trino.Quote(table) +
		" (" + strings.Join(cols, ", ") + ")", nil
}

// handleSparkCredentials gives a notebook's Spark session credentials for the
// tables the person reads whole (or those of them asked for), as the person.
func (s *server) handleSparkCredentials(w http.ResponseWriter, r *http.Request) {
	who, token, ok := s.person(w, r)
	if !ok {
		return
	}
	var req struct {
		Tables []string   `json:"tables"` // namespace.table; none: every table they read whole
		Create []sparkNew `json:"create"` // tables to make first (if they aren't there), as the person
		Hours  float64    `json:"hours"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.fail(w, r, http.StatusBadRequest, "the request isn't JSON: "+err.Error())
			return
		}
	}
	access, secret := os.Getenv("SPARK_S3_ACCESS_KEY"), os.Getenv("SPARK_S3_SECRET_KEY")
	if access == "" || secret == "" || s.cfg.StoreURL == "" {
		s.fail(w, r, http.StatusServiceUnavailable, "this install gives Spark no credentials (no SPARK_S3_ACCESS_KEY or STORE_URL)")
		return
	}
	hours := req.Hours
	if hours <= 0 || hours > 12 {
		hours = 12
	}
	ctx := r.Context()
	tc, err := s.trinoAs(token)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	var made []string
	fresh := map[string]bool{}
	for _, t := range req.Create {
		stmt, err := sparkCreate(t)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := tc.Exec(ctx, stmt); err != nil {
			s.fail(w, r, http.StatusForbidden, "Trino didn't make "+t.Name+" as you: "+err.Error())
			return
		}
		made = append(made, t.Name)
		fresh[t.Name] = true
		if len(req.Tables) > 0 { // named tables: the new ones too
			req.Tables = append(req.Tables, t.Name)
		}
	}
	if len(made) > 0 {
		s.record(r, who, "spark.create", "spark", strings.Join(made, ", "))
	}
	rows, err := tc.Exec(ctx, "SELECT table_schema, table_name FROM "+trino.Quote(sparkCatalog)+".information_schema.tables"+
		" WHERE table_type = 'BASE TABLE' AND table_schema <> 'information_schema'")
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "Trino didn't list your tables: "+err.Error())
		return
	}
	visible := map[string][2]string{}
	for _, row := range rows {
		if len(row) == 2 {
			ns, _ := row[0].(string)
			t, _ := row[1].(string)
			visible[ns+"."+t] = [2]string{ns, t}
		}
	}
	names := req.Tables
	skipped := []sparkSkipped{}
	if len(names) == 0 {
		for n := range visible {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	iceberg := &catalog.Iceberg{HTTP: catalog.NessieHTTP(30 * time.Second),
		Base: strings.TrimRight(envOr("NESSIE_URL", "http://nessie:19120"), "/") + "/iceberg", Warehouse: envOr("NESSIE_WAREHOUSE", "warehouse")}
	results := make([]*sparkTable, len(names))
	whys := make([]string, len(names))
	var wg sync.WaitGroup
	sem := make(chan struct{}, sparkChecksAtOnce)
	for i, n := range names {
		key, ok := visible[n]
		if !ok {
			whys[i] = "you don't see it in Trino"
			continue
		}
		wg.Add(1)
		go func(i int, ns, table string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			read, write, why := sparkCheck(ctx, tc, ns, table, fresh[ns+"."+table])
			if !read {
				whys[i] = why
				return
			}
			folders, err := iceberg.Folders(ctx, ns, table)
			if err != nil {
				whys[i] = "the catalog didn't say where its files are: " + err.Error()
				return
			}
			results[i] = &sparkTable{Name: ns + "." + table, Write: write, folders: folders}
		}(i, key[0], key[1])
	}
	wg.Wait()
	var tables []sparkTable
	for i, t := range results {
		if t != nil {
			tables = append(tables, *t)
		} else {
			skipped = append(skipped, sparkSkipped{Name: names[i], Why: whys[i]})
		}
	}
	if len(tables) == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "there's no table you read whole, so Spark has nothing to read: " +
			"use Trino (buckets_lake.sql) for the tables with row filters or masks", "skipped": skipped})
		return
	}
	policy, err := sparkPolicy(tables)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if len(policy) > sparkPolicyMax {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": fmt.Sprintf(
			"your %d tables don't fit in one session's credentials (Buckets takes %d bytes of policy, these are %d): "+
				"name the ones this session needs: buckets_lake.spark(tables=[\"namespace.table\", ...])",
			len(tables), sparkPolicyMax, len(policy)), "skipped": skipped})
		return
	}
	creds, err := credentials.NewSTSAssumeRole(s.cfg.StoreURL, credentials.STSAssumeRoleOptions{
		AccessKey: access, SecretKey: secret, Policy: string(policy), Location: "us-east-1",
		DurationSeconds: int(hours * 3600), RoleSessionName: who})
	if err == nil {
		var v credentials.Value
		if v, err = creds.Get(); err == nil {
			names := make([]string, len(tables))
			for i, t := range tables {
				names[i] = t.Name
				if t.Write {
					names[i] += " (write)"
				}
			}
			s.record(r, who, "spark.credentials", "spark", strings.Join(names, ", "))
			// The session's catalog (platformd's, sparkcatalog.go): these tables alone, as long as the credentials.
			grant := sparkGrant{User: who, Tables: map[string]bool{}, Expires: v.Expiration.Unix()}
			for _, t := range tables {
				grant.Tables[t.Name] = t.Write
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"accessKeyId": v.AccessKeyID, "secretAccessKey": v.SecretAccessKey, "sessionToken": v.SessionToken,
				"expiration": v.Expiration.UTC().Format(time.RFC3339), "tables": tables, "skipped": skipped,
				"catalogToken": grant.sign(sparkCatalogKey())})
			return
		}
	}
	s.fail(w, r, http.StatusBadGateway, "Buckets gave no credentials: "+err.Error())
}
