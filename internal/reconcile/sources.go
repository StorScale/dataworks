package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// TrinoSources keeps a Trino catalog for each of a project's sources (use
// case 8.3: the company's databases, queried where they are). Trino makes
// and drops catalogs while it runs (catalog.management=dynamic), as the
// operator's own Trino account asks (CREATE CATALOG), and Ranger decides
// who may read them (the project's policies).
//
// A source's connection (its URL, user and password) never goes into the
// statement, which Trino logs and Ranger's audit records: it goes into
// Files, which Trino reads at Dir, and the statement names the files
// (${FILE:...}, which Trino resolves as it makes the catalog). Each version
// of a source's connection has files of its own (named with its hash), so a
// catalog made again for a changed one waits until Trino sees the new files,
// never making it from the old.
type TrinoSources struct {
	Trino *trino.Client
	Files SourceFiles
	Dir   string // where Trino reads the files: /etc/trino/sources
}

// SourceFiles hold the sources' connections, a file each, and what the
// operator made of them: on Kubernetes a Secret mounted into Trino's pods,
// in Compose a directory in a volume Trino shares.
type SourceFiles interface {
	Read(ctx context.Context) (map[string]string, error)
	Write(ctx context.Context, files map[string]string) error // all of them: what's left out goes
}

// The files of a catalog: <catalog>.<hash>.{url,user,password}, the
// connection; and <catalog>.project, <catalog>.made, which project it's for
// and the hash the catalog was last made with.
const (
	projectSuffix = ".project"
	madeSuffix    = ".made"
)

// sourceConn is a source as Trino's connector takes it.
type sourceConn struct {
	connector, url, user, password string
}

func (c sourceConn) hash() string {
	h := sha256.Sum256([]byte(strings.Join([]string{c.connector, c.url, c.user, c.password}, "\x00")))
	return hex.EncodeToString(h[:])[:12]
}

// jdbcURL is a source's connection URL, from its connection's keys: url,
// as given (for options the platform doesn't know), or host, port and
// database.
func jdbcURL(kind string, conn map[string]string) (string, error) {
	if u := conn["url"]; u != "" {
		if !strings.HasPrefix(u, "jdbc:") {
			return "", errors.New("its url isn't a JDBC URL (jdbc:...)")
		}
		return u, nil
	}
	host, port, db := conn["host"], conn["port"], conn["database"]
	if host == "" {
		return "", errors.New("it has no host (or url)")
	}
	if strings.ContainsAny(host+port+db, "/?;@ \t\r\n") {
		return "", errors.New("its host, port or database has a character a URL can't hold here")
	}
	or := func(v, def string) string {
		if v != "" {
			return v
		}
		return def
	}
	switch kind {
	case "postgresql":
		if db == "" {
			return "", errors.New("a PostgreSQL source names its database")
		}
		return "jdbc:postgresql://" + host + ":" + or(port, "5432") + "/" + url.PathEscape(db), nil
	case "mysql": // a catalog is the server: each database a schema
		return "jdbc:mysql://" + host + ":" + or(port, "3306"), nil
	case "sqlserver":
		if db == "" {
			return "", errors.New("a SQL Server source names its database")
		}
		return "jdbc:sqlserver://" + host + ":" + or(port, "1433") + ";databaseName=" + db + ";encrypt=true", nil
	case "oracle": // database: the service name
		if db == "" {
			return "", errors.New("an Oracle source names its service (database)")
		}
		return "jdbc:oracle:thin:@//" + host + ":" + or(port, "1521") + "/" + db, nil
	}
	return "", fmt.Errorf("no such kind %q", kind)
}

// Apply makes Trino hold the project's sources' catalogs, and no others of
// the project's. conns reads one of the project's connections.
func (s *TrinoSources) Apply(ctx context.Context, p *project.Project, conns func(name string) (map[string]string, error)) error {
	want := map[string]sourceConn{}
	var errs []error
	for _, src := range p.Spec.Sources {
		cat := p.SourceCatalog(src)
		conn, err := conns(src.Connection)
		if err == nil && conn == nil {
			err = fmt.Errorf("no connection %q", src.Connection)
		}
		var u string
		if err == nil {
			u, err = jdbcURL(src.Kind, conn)
		}
		if err == nil && (conn["username"] == "" || conn["password"] == "") {
			err = errors.New("its connection needs a username and a password")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("source %s: %w", src.Name, err))
			continue
		}
		want[cat] = sourceConn{connector: src.Kind, url: u, user: conn["username"], password: conn["password"]}
	}
	return errors.Join(append(errs, s.sync(ctx, p.Metadata.Name, want))...)
}

// Remove drops the project's catalogs (its project went).
func (s *TrinoSources) Remove(ctx context.Context, name string) error {
	return s.sync(ctx, name, nil)
}

func (s *TrinoSources) sync(ctx context.Context, proj string, want map[string]sourceConn) error {
	files, err := s.Files.Read(ctx)
	if err != nil {
		return fmt.Errorf("the sources' files: %w", err)
	}
	have, err := s.catalogs(ctx)
	if err != nil {
		return err
	}
	// The files first: the connections wanted, and nothing of the
	// project's catalogs that aren't (nor their old versions).
	next := map[string]string{}
	mine := map[string]bool{}
	for k, v := range files {
		if strings.HasSuffix(k, projectSuffix) && v == proj {
			mine[strings.TrimSuffix(k, projectSuffix)] = true
		}
	}
	for k, v := range files {
		if !mine[catalogOf(k)] {
			next[k] = v
		}
	}
	for cat, c := range want {
		if other, ok := files[cat+projectSuffix]; ok && other != proj {
			return fmt.Errorf("the catalog %s is project %s's", cat, other)
		}
		h := c.hash()
		next[cat+projectSuffix] = proj
		next[cat+"."+h+".url"], next[cat+"."+h+".user"], next[cat+"."+h+".password"] = c.url, c.user, c.password
		if m := files[cat+madeSuffix]; m != "" {
			next[cat+madeSuffix] = m
		}
	}
	if !sameFiles(files, next) {
		if err := s.Files.Write(ctx, next); err != nil {
			return fmt.Errorf("the sources' files: %w", err)
		}
	}

	var errs []error
	for cat := range mine {
		if _, ok := want[cat]; ok || !have[cat] {
			continue
		}
		if _, err := s.Trino.Exec(ctx, "DROP CATALOG IF EXISTS "+trino.Quote(cat)); err != nil {
			errs = append(errs, fmt.Errorf("dropping %s: %w", cat, err))
		}
	}
	made := false
	for _, cat := range sortedKeys(want) {
		c := want[cat]
		h := c.hash()
		if have[cat] && next[cat+madeSuffix] == h {
			continue
		}
		if have[cat] {
			if _, err := s.Trino.Exec(ctx, "DROP CATALOG IF EXISTS "+trino.Quote(cat)); err != nil {
				errs = append(errs, fmt.Errorf("source catalog %s: %w", cat, err))
				continue
			}
		}
		if _, err := s.Trino.Exec(ctx, s.createCatalog(cat, c.connector, h)); err != nil {
			errs = append(errs, fmt.Errorf("source catalog %s: %w (Trino may not see its connection yet: the next pass tries again)", cat, err))
			continue
		}
		next[cat+madeSuffix], made = h, true
	}
	if made {
		if err := s.Files.Write(ctx, next); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// createCatalog is the statement making a catalog: the connection named by
// its files, never written into it.
func (s *TrinoSources) createCatalog(cat, connector, hash string) string {
	file := func(what string) string {
		return "'${FILE:" + filepath.Join(s.Dir, cat+"."+hash+"."+what) + "}'"
	}
	return fmt.Sprintf(`CREATE CATALOG %s USING %s WITH ("connection-url" = %s, "connection-user" = %s, "connection-password" = %s)`,
		trino.Quote(cat), connector, file("url"), file("user"), file("password"))
}

// catalogs are Trino's catalogs (all those the operator's account may see:
// it may make and drop any).
func (s *TrinoSources) catalogs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.Trino.Exec(ctx, "SHOW CATALOGS")
	if err != nil {
		return nil, fmt.Errorf("Trino's catalogs: %w", err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		if len(r) > 0 {
			if n, ok := r[0].(string); ok {
				out[n] = true
			}
		}
	}
	return out, nil
}

// catalogOf is the catalog a file is of: its name up to the first dot.
func catalogOf(file string) string {
	cat, _, _ := strings.Cut(file, ".")
	return cat
}

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SecretFiles keeps the sources' files in a Secret, which the chart mounts
// into Trino's pods (the kubelet brings changes within a minute or so).
type SecretFiles struct {
	Kube *Kube
	Name string // trino-sources
}

type secretObj struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Type       string            `json:"type,omitempty"`
	Metadata   map[string]any    `json:"metadata"`
	Data       map[string]string `json:"data"`
}

func (f *SecretFiles) get(ctx context.Context) (*secretObj, bool, error) {
	var sec secretObj
	found, err := f.Kube.do(ctx, "GET", f.Kube.path("secrets", f.Name), "", nil, &sec)
	return &sec, found, err
}

func (f *SecretFiles) Read(ctx context.Context) (map[string]string, error) {
	sec, _, err := f.get(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range sec.Data {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, err
		}
		out[k] = string(b)
	}
	return out, nil
}

func (f *SecretFiles) Write(ctx context.Context, files map[string]string) error {
	sec, found, err := f.get(ctx)
	if err != nil {
		return err
	}
	data := map[string]string{}
	for k, v := range files {
		data[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	if !found {
		body := secretObj{APIVersion: "v1", Kind: "Secret", Type: "Opaque", Metadata: map[string]any{"name": f.Name}, Data: data}
		_, err := f.Kube.do(ctx, "POST", f.Kube.path("secrets", ""), "application/json", body, nil)
		return err
	}
	sec.APIVersion, sec.Kind, sec.Data = "v1", "Secret", data // its resourceVersion kept: a write over another's fails
	_, err = f.Kube.do(ctx, "PUT", f.Kube.path("secrets", f.Name), "application/json", sec, nil)
	return err
}

// DirFiles keeps the sources' files in a directory (Compose: a volume the
// operator and Trino share).
type DirFiles struct {
	Dir string
}

func (f *DirFiles) Read(_ context.Context) (map[string]string, error) {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			b, err := os.ReadFile(filepath.Join(f.Dir, e.Name()))
			if err != nil {
				return nil, err
			}
			out[e.Name()] = string(b)
		}
	}
	return out, nil
}

func (f *DirFiles) Write(ctx context.Context, files map[string]string) error {
	old, err := f.Read(ctx)
	if err != nil {
		return err
	}
	for k, v := range files {
		if strings.ContainsAny(k, "/\\") || strings.HasPrefix(k, ".") {
			return fmt.Errorf("no file %q", k)
		}
		if w, ok := old[k]; ok && w == v {
			continue
		}
		tmp := filepath.Join(f.Dir, ".tmp-"+k)
		// Trino reads them as its own user: readable, in a volume only it
		// and the operator mount.
		if err := os.WriteFile(tmp, []byte(v), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(f.Dir, k)); err != nil {
			return err
		}
	}
	for k := range old {
		if _, ok := files[k]; !ok {
			if err := os.Remove(filepath.Join(f.Dir, k)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
