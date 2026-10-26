package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/trino"
)

// fakeTrino keeps catalogs as CREATE and DROP CATALOG say, and fails a
// CREATE while refuse is set (Trino not seeing the files yet).
type fakeTrino struct {
	mu       sync.Mutex
	catalogs map[string]bool
	stmts    []string
	refuse   bool
	refuseN  int // refuse this many CREATEs, then accept (Trino seeing the file at last)
}

var (
	createRE = regexp.MustCompile(`^CREATE CATALOG (?:IF NOT EXISTS )?"([a-z0-9_]+)" USING`)
	dropRE   = regexp.MustCompile(`^DROP CATALOG IF EXISTS "([a-z0-9_]+)"$`)
)

func (f *fakeTrino) server(t *testing.T) *trino.Client {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sql := string(b)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stmts = append(f.stmts, sql)
		out := map[string]any{"id": "q", "stats": map[string]string{"state": "FINISHED"}}
		switch {
		case sql == "SHOW CATALOGS":
			var rows [][]any
			for c := range f.catalogs {
				rows = append(rows, []any{c})
			}
			out["data"] = rows
		case createRE.MatchString(sql):
			if f.refuse || f.refuseN > 0 {
				f.refuseN = max(f.refuseN-1, 0)
				out["error"] = map[string]string{"errorName": "GENERIC_INTERNAL_ERROR", "message": "Configuration errors: Invalid configuration property connection-credential-file: file does not exist: /etc/trino/sources/x.properties"}
			} else {
				f.catalogs[createRE.FindStringSubmatch(sql)[1]] = true
			}
		case dropRE.MatchString(sql):
			delete(f.catalogs, dropRE.FindStringSubmatch(sql)[1])
		default:
			t.Errorf("unexpected statement: %s", sql)
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(ts.Close)
	c, _ := trino.New(ts.URL, "platform-operator", "pw", "")
	return c
}

func TestSourcesCatalogs(t *testing.T) {
	ctx := context.Background()
	ft := &fakeTrino{catalogs: map[string]bool{"iceberg": true, "system": true}}
	files := &DirFiles{Dir: t.TempDir()}
	s := &TrinoSources{Trino: ft.server(t), Files: files, Dir: "/etc/trino/sources"}
	p, err := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"members":[{"group":"engineers","role":"editor"}],"sources":[{"name":"finance","kind":"postgresql","connection":"finance-db"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	conn := map[string]string{"host": "db.example.com", "database": "finance", "username": "reader", "password": "s3cret"}
	conns := func(name string) (map[string]string, error) {
		if name != "finance-db" {
			return nil, nil
		}
		return conn, nil
	}

	// Made, with the connection in files and not in the statement.
	if err := s.Apply(ctx, p, conns); err != nil {
		t.Fatal(err)
	}
	if !ft.catalogs["sales_finance"] {
		t.Fatalf("no catalog: %v", ft.stmts)
	}
	made := ft.stmts[len(ft.stmts)-1]
	for _, st := range ft.stmts {
		if strings.Contains(st, "s3cret") || strings.Contains(st, "reader") {
			t.Errorf("the user or password is in a statement: %s", st)
		}
	}
	have, _ := files.Read(ctx)
	var creds string
	for k, v := range have {
		if strings.HasSuffix(k, ".properties") {
			creds = v
			if !strings.Contains(made, `"connection-credential-file" = '/etc/trino/sources/`+k+`'`) {
				t.Errorf("the statement doesn't name %s: %s", k, made)
			}
		}
	}
	if creds != "connection-user=reader\nconnection-password=s3cret\n" || have["sales_finance.project"] != "sales" {
		t.Errorf("files: %v", have)
	}
	if !strings.Contains(made, `"connection-url" = 'jdbc:postgresql://db.example.com:5432/finance'`) {
		t.Errorf("the URL: %s", made)
	}

	// Nothing changed: nothing made again.
	n := len(ft.stmts)
	s.Apply(ctx, p, conns)
	if len(ft.stmts) != n+1 { // SHOW CATALOGS only
		t.Errorf("made again: %v", ft.stmts[n:])
	}

	// Trino restarted (its dynamic catalogs gone): made again.
	delete(ft.catalogs, "sales_finance")
	s.Apply(ctx, p, conns)
	if !ft.catalogs["sales_finance"] {
		t.Error("not made again after Trino's restart")
	}

	// A new password, before Trino sees the new files: tried again until it does.
	conn["password"] = "n3w"
	ft.refuse = true
	if err := s.Apply(ctx, p, conns); err == nil {
		t.Error("want an error while Trino can't make it")
	}
	ft.refuse = false
	if err := s.Apply(ctx, p, conns); err != nil || !ft.catalogs["sales_finance"] {
		t.Errorf("made with the new password: %v", err)
	}
	have, _ = files.Read(ctx)
	if n := len(have); n != 3 { // credentials, project, made: the old version's gone
		t.Errorf("want one version's files: %v", have)
	}

	// Another project's removal leaves it; the source going drops it.
	if err := s.Remove(ctx, "sales2"); err != nil || !ft.catalogs["sales_finance"] {
		t.Errorf("another project's removal touched sales': %v", err)
	}
	p.Spec.Sources = nil
	if err := s.Apply(ctx, p, conns); err != nil || ft.catalogs["sales_finance"] {
		t.Errorf("not dropped: %v %v", err, ft.catalogs)
	}
	if have, _ := files.Read(ctx); len(have) != 0 {
		t.Errorf("files left: %v", have)
	}
}

func TestJDBCURL(t *testing.T) {
	for _, c := range []struct {
		kind string
		conn map[string]string
		want string
	}{
		{"postgresql", map[string]string{"host": "pg", "database": "fin"}, "jdbc:postgresql://pg:5432/fin"},
		{"mysql", map[string]string{"host": "my", "port": "3307"}, "jdbc:mysql://my:3307"},
		{"sqlserver", map[string]string{"host": "ms", "database": "fin"}, "jdbc:sqlserver://ms:1433;databaseName=fin;encrypt=true"},
		{"oracle", map[string]string{"host": "ora", "database": "FIN"}, "jdbc:oracle:thin:@//ora:1521/FIN"},
		{"postgresql", map[string]string{"url": "jdbc:postgresql://pg/fin?ssl=true"}, "jdbc:postgresql://pg/fin?ssl=true"},
	} {
		if got, err := jdbcURL(c.kind, c.conn); err != nil || got != c.want {
			t.Errorf("%s %v: %s, %v", c.kind, c.conn, got, err)
		}
	}
	for _, conn := range []map[string]string{{"database": "fin"}, {"host": "pg"}, {"host": "pg;x=1", "database": "fin"}, {"url": "http://pg"},
		{"url": "jdbc:postgresql://pg/fin?user=x&password=y"}} {
		if _, err := jdbcURL("postgresql", conn); err == nil {
			t.Errorf("%v: want refused", conn)
		}
	}
}

func TestPropValue(t *testing.T) {
	for in, want := range map[string]string{
		`pa=ss:word`: `pa=ss:word`, ` lead`: `\ lead`, `back\slash`: `back\\slash`, "two\nlines": `two\nlines`, "café": `caf\u00e9`,
	} {
		if got := propValue(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestCopyProperties(t *testing.T) {
	p, err := project.Parse([]byte(`{"apiVersion":"platform.storscale.io/v1alpha1","kind":"Project","metadata":{"name":"sales"},
		"spec":{"members":[{"group":"engineers","role":"editor"}],"tables":{},
		"sources":[{"name":"finance","kind":"postgresql","connection":"fin","copy":{"tables":["public.invoices","ledger.accounts"]}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := &Copies{CommitEvery: 30}
	props, err := c.properties(p, p.Spec.Sources[0], map[string]string{"host": "pg", "database": "fin", "username": "cdc", "password": "pa=ss\\word"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"debezium.source.connector.class=io.debezium.connector.postgresql.PostgresConnector\n",
		"debezium.source.table.include.list=public.invoices,ledger.accounts\n",
		"debezium.source.slot.name=storscale_sales_finance\n",
		"debezium.source.database.password=pa=ss\\\\word\n",
		"debezium.sink.iceberg.table-namespace=sales\n",
		"debezium.sink.iceberg.table-prefix=finance_\n",
		"debezium.sink.iceberg.format-version=2\n",
		"debezium.source.offset.storage.iceberg.table-namespace=storscale_cdc\n",
		"debezium.sink.batch.batch-size-wait.max-wait-ms=30000\n",
	} {
		if !strings.Contains(props, want) {
			t.Errorf("no %q in:\n%s", want, props)
		}
	}
	if _, err := c.properties(p, p.Spec.Sources[0], map[string]string{"url": "jdbc:postgresql://pg/fin", "username": "u", "password": "p"}); err == nil {
		t.Error("a url alone: want refused")
	}
	if got := identifier("storscale_" + strings.Repeat("x", 70)); len(got) != 63 {
		t.Errorf("identifier: %d characters", len(got))
	}
}

func TestACatalogWaitsForTrinoToSeeItsFile(t *testing.T) {
	ctx := context.Background()
	ft := &fakeTrino{catalogs: map[string]bool{"iceberg": true}, refuseN: 2}
	files := &DirFiles{Dir: t.TempDir()}
	s := &TrinoSources{Trino: ft.server(t), Files: files, Dir: "/etc/trino/sources", FileWait: 3 * time.Second}
	conns := func(name string) (map[string]string, error) {
		return map[string]string{"host": "db.example.com", "port": "5432", "database": "finance", "username": "ro", "password": "pw"}, nil
	}
	p, err := project.Parse([]byte(`{"metadata": {"name": "sales"}, "spec": {"members": [{"user": "bob", "role": "editor"}],
		"sources": [{"name": "finance", "kind": "postgresql", "connection": "finance-db"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, p, conns); err != nil {
		t.Fatalf("waited, then made: %v", err)
	}
	creates := 0
	for _, st := range ft.stmts {
		if createRE.MatchString(st) {
			creates++
		}
	}
	if !ft.catalogs["sales_finance"] || creates != 3 {
		t.Fatalf("catalogs %v after %d CREATEs", ft.catalogs, creates)
	}
	// No wait: the refusal is the pass's answer, as before.
	ft2 := &fakeTrino{catalogs: map[string]bool{"iceberg": true}, refuse: true}
	s2 := &TrinoSources{Trino: ft2.server(t), Files: &DirFiles{Dir: t.TempDir()}, Dir: "/etc/trino/sources"}
	if err := s2.Apply(ctx, p, conns); err == nil || !strings.Contains(err.Error(), "the next pass tries again") {
		t.Fatalf("refused without a wait: %v", err)
	}
}
