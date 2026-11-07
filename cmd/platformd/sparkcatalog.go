package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/StorScale/dataworks/internal/catalog"
)

// Spark's catalog (notebooks' Distributed Spark): Nessie answers the platform's
// catalog account alone, so a Spark session reaches the catalog through
// platformd, at /internal/spark-catalog/: Iceberg's REST API, for the tables
// its credentials were given for and nothing else. It loads those tables,
// commits to those it may change, and lists only them; it makes, drops and
// renames nothing (buckets_lake.spark(create=...) has Trino make a table, as
// the person), and stays on the catalog's main branch.
//
// A session's grant is a token platformd signs (HMAC, with a key of its own
// derived from the Spark account's secret, which never leaves platformd): who,
// which tables (and which it may write), until when, as the session's
// credentials. Spark sends it as its catalog's Authorization header.

const sparkCatalogPath = "/internal/spark-catalog/"

type sparkGrant struct {
	User    string          `json:"u"`
	Tables  map[string]bool `json:"t"` // namespace.table -> may write
	Expires int64           `json:"e"` // Unix seconds
}

func sparkCatalogKey() []byte {
	secret := os.Getenv("SPARK_S3_SECRET_KEY")
	if secret == "" {
		return nil
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("dataworks: Spark's catalog grants"))
	return m.Sum(nil)
}

// sign is the grant's token: its JSON and its HMAC, each base64url.
func (g sparkGrant) sign(key []byte) string {
	body, _ := json.Marshal(g)
	m := hmac.New(sha256.New, key)
	m.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func parseSparkGrant(token string, key []byte, now time.Time) (*sparkGrant, error) {
	body64, sig64, ok := strings.Cut(token, ".")
	if !ok || len(key) == 0 {
		return nil, errors.New("no grant")
	}
	body, err1 := base64.RawURLEncoding.DecodeString(body64)
	sig, err2 := base64.RawURLEncoding.DecodeString(sig64)
	if err1 != nil || err2 != nil {
		return nil, errors.New("not a grant")
	}
	m := hmac.New(sha256.New, key)
	m.Write(body)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return nil, errors.New("not a grant of this platform's")
	}
	var g sparkGrant
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, errors.New("not a grant")
	}
	if now.Unix() >= g.Expires {
		return nil, errors.New("the session's grant has expired: start a new session (buckets_lake.spark())")
	}
	return &g, nil
}

// icebergError is an error as Iceberg's REST clients read it.
func icebergError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "type": kind, "code": code}})
}

// The table properties that say where a table's files go: a session doesn't
// move them (another engine would then write the table's files there).
var sparkPathProperties = map[string]bool{"write.data.path": true, "write.metadata.path": true,
	"write.object-storage.path": true, "write.folder-storage.path": true, "location": true}

type sparkUpdates struct {
	Requirements []json.RawMessage `json:"requirements"`
	Updates      []struct {
		Action  string            `json:"action"`
		Updates map[string]string `json:"updates"`  // set-properties
		Removed []string          `json:"removals"` // remove-properties
	} `json:"updates"`
}

// sparkCommitAllowed is whether a commit's updates leave the table where it is.
func sparkCommitAllowed(u sparkUpdates) error {
	for _, up := range u.Updates {
		switch up.Action {
		case "set-location", "assign-uuid":
			return fmt.Errorf("a Spark session doesn't %s a table", up.Action)
		case "set-properties":
			for k := range up.Updates {
				if sparkPathProperties[k] {
					return fmt.Errorf("a Spark session doesn't set %s: it says where the table's files go", k)
				}
			}
		case "remove-properties":
			for _, k := range up.Removed {
				if sparkPathProperties[k] {
					return fmt.Errorf("a Spark session doesn't remove %s: it says where the table's files go", k)
				}
			}
		}
	}
	return nil
}

// sparkNamespaces are the grant's namespaces, sorted.
func (g *sparkGrant) namespaces() []string {
	seen := map[string]bool{}
	for t := range g.Tables {
		ns, _, _ := strings.Cut(t, ".")
		seen[ns] = true
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (g *sparkGrant) tablesIn(ns string) []string {
	var out []string
	for t := range g.Tables {
		if n, name, _ := strings.Cut(t, "."); n == ns {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (s *server) handleSparkCatalog(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	g, err := parseSparkGrant(strings.TrimSpace(auth), sparkCatalogKey(), time.Now())
	if err != nil {
		icebergError(w, http.StatusUnauthorized, "NotAuthorizedException", "Spark's catalog: "+err.Error())
		return
	}
	nessie := strings.TrimRight(envOr("NESSIE_URL", "http://nessie:19120"), "/") + "/iceberg/"
	// The path after the prefix, each part unescaped (a namespace's parts are joined by %1F).
	rest := strings.TrimPrefix(r.URL.EscapedPath(), sparkCatalogPath)
	raw := strings.Split(rest, "/")
	parts := make([]string, len(raw))
	for i, p := range raw {
		if parts[i], err = url.PathUnescape(p); err != nil {
			icebergError(w, http.StatusBadRequest, "BadRequestException", "Spark's catalog: a path it can't read")
			return
		}
	}
	denied := func(msg string) { icebergError(w, http.StatusForbidden, "ForbiddenException", msg) }
	if len(parts) < 2 || parts[0] != "v1" {
		denied("Spark's catalog answers Iceberg's REST API (v1) alone")
		return
	}
	if len(parts) == 2 && parts[1] == "config" && r.Method == http.MethodGet {
		s.sparkCatalogConfig(w, r, nessie)
		return
	}
	mainPrefix, err := s.sparkCatalogPrefix(r, nessie)
	if err != nil {
		icebergError(w, http.StatusBadGateway, "ServiceUnavailableException", "Spark's catalog: "+err.Error())
		return
	}
	if parts[1] != mainPrefix {
		denied("a Spark session reads and writes the catalog's main branch alone")
		return
	}
	p := parts[2:]
	table := func(i int) string { return p[i] + "." + p[i+2] }
	switch {
	case len(p) == 1 && p[0] == "namespaces" && r.Method == http.MethodGet:
		list := [][]string{}
		if r.URL.Query().Get("parent") == "" {
			for _, ns := range g.namespaces() {
				list = append(list, []string{ns})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"namespaces": list})
	case len(p) == 2 && p[0] == "namespaces" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if len(g.tablesIn(p[1])) == 0 {
			icebergError(w, http.StatusNotFound, "NoSuchNamespaceException", "Namespace does not exist: "+p[1])
			return
		}
		s.sparkCatalogForward(w, r, nessie+rest, nil)
	case len(p) == 3 && p[0] == "namespaces" && (p[2] == "tables" || p[2] == "views") && r.Method == http.MethodGet:
		ids := []map[string]any{}
		if p[2] == "tables" {
			for _, t := range g.tablesIn(p[1]) {
				ids = append(ids, map[string]any{"namespace": []string{p[1]}, "name": t})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"identifiers": ids})
	case len(p) == 4 && p[0] == "namespaces" && p[2] == "tables" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if _, ok := g.Tables[table(1)]; !ok {
			denied(table(1) + " isn't one of this session's tables (buckets_lake.spark_skipped says why, if it was asked for)")
			return
		}
		s.sparkCatalogForward(w, r, nessie+rest, nil)
	case len(p) == 5 && p[0] == "namespaces" && p[2] == "tables" && p[4] == "metrics" && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusNoContent) // a scan's report: nothing keeps it
	case len(p) == 4 && p[0] == "namespaces" && p[2] == "tables" && r.Method == http.MethodPost:
		t := table(1)
		if !g.Tables[t] {
			denied("this session may read " + t + " but not change it")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			icebergError(w, http.StatusBadRequest, "BadRequestException", "Spark's catalog: the commit didn't arrive whole")
			return
		}
		var u sparkUpdates
		if json.Unmarshal(body, &u) != nil {
			icebergError(w, http.StatusBadRequest, "BadRequestException", "Spark's catalog: a commit it can't read")
			return
		}
		if err := sparkCommitAllowed(u); err != nil {
			denied(err.Error())
			return
		}
		if s.sparkCatalogForward(w, r, nessie+rest, body) {
			s.record(r, g.User, "spark.commit", "spark", t)
		}
	case len(p) == 2 && p[0] == "transactions" && p[1] == "commit" && r.Method == http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			icebergError(w, http.StatusBadRequest, "BadRequestException", "Spark's catalog: the commit didn't arrive whole")
			return
		}
		var tx struct {
			Changes []struct {
				Identifier struct {
					Namespace []string `json:"namespace"`
					Name      string   `json:"name"`
				} `json:"identifier"`
				sparkUpdates
			} `json:"table-changes"`
		}
		if json.Unmarshal(body, &tx) != nil || len(tx.Changes) == 0 {
			icebergError(w, http.StatusBadRequest, "BadRequestException", "Spark's catalog: a commit it can't read")
			return
		}
		var names []string
		for _, c := range tx.Changes {
			t := strings.Join(c.Identifier.Namespace, ".") + "." + c.Identifier.Name
			if !g.Tables[t] {
				denied("this session may not change " + t)
				return
			}
			if err := sparkCommitAllowed(c.sparkUpdates); err != nil {
				denied(err.Error())
				return
			}
			names = append(names, t)
		}
		if s.sparkCatalogForward(w, r, nessie+rest, body) {
			s.record(r, g.User, "spark.commit", "spark", strings.Join(names, ", "))
		}
	default:
		denied("a Spark session doesn't make, drop or rename tables or namespaces: " +
			"buckets_lake.spark(create={...}) has the platform make a table, as you")
	}
}

// sparkCatalogConfig is Nessie's config, without its address: the session
// keeps to this one.
func (s *server) sparkCatalogConfig(w http.ResponseWriter, r *http.Request, nessie string) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, nessie+"v1/config?"+r.URL.RawQuery, nil)
	res, err := catalog.NessieHTTP(30 * time.Second).Do(req)
	if err != nil {
		icebergError(w, http.StatusBadGateway, "ServiceUnavailableException", "Spark's catalog: "+err.Error())
		return
	}
	defer res.Body.Close()
	var cfg map[string]any
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&cfg) != nil {
		icebergError(w, http.StatusBadGateway, "ServiceUnavailableException", fmt.Sprintf("Spark's catalog: Nessie's config: HTTP %d", res.StatusCode))
		return
	}
	for _, k := range []string{"defaults", "overrides"} {
		if m, ok := cfg[k].(map[string]any); ok {
			for key := range m {
				if key == "uri" || strings.HasPrefix(key, "nessie.") {
					delete(m, key)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, cfg)
}

// sparkCatalogPrefix is the main branch's prefix, unescaped (Nessie gives it
// escaped already; a client may send it either way).
func (s *server) sparkCatalogPrefix(r *http.Request, nessie string) (string, error) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, nessie+"v1/config?warehouse="+url.QueryEscape(envOr("NESSIE_WAREHOUSE", "warehouse")), nil)
	res, err := catalog.NessieHTTP(30 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var cfg struct {
		Defaults  map[string]any `json:"defaults"`
		Overrides map[string]any `json:"overrides"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&cfg) != nil {
		return "", fmt.Errorf("Nessie's config: HTTP %d", res.StatusCode)
	}
	prefix, _ := cfg.Overrides["prefix"].(string)
	if prefix == "" {
		prefix, _ = cfg.Defaults["prefix"].(string)
	}
	if prefix == "" {
		return "", errors.New("Nessie's config names no prefix")
	}
	if p, err := url.PathUnescape(prefix); err == nil {
		prefix = p
	}
	return prefix, nil
}

// sparkCatalogForward sends the request on to Nessie as the catalog's account,
// and its answer back; whether Nessie took it (2xx).
func (s *server) sparkCatalogForward(w http.ResponseWriter, r *http.Request, target string, body []byte) bool {
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, rd)
	if err != nil {
		icebergError(w, http.StatusBadRequest, "BadRequestException", "Spark's catalog: "+err.Error())
		return false
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	res, err := catalog.NessieHTTP(time.Minute).Do(req)
	if err != nil {
		icebergError(w, http.StatusBadGateway, "ServiceUnavailableException", "Spark's catalog: "+err.Error())
		return false
	}
	defer res.Body.Close()
	for _, h := range []string{"Content-Type", "ETag"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	io.Copy(w, io.LimitReader(res.Body, 64<<20))
	return res.StatusCode < 300
}
