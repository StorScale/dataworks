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

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// fakeTrino keeps catalogs as CREATE and DROP CATALOG say, and fails a
// CREATE while refuse is set (Trino not seeing the files yet).
type fakeTrino struct {
	mu       sync.Mutex
	catalogs map[string]bool
	stmts    []string
	refuse   bool
}

var (
	createRE = regexp.MustCompile(`^CREATE CATALOG "([a-z0-9_]+)" USING`)
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
			if f.refuse {
				out["error"] = map[string]string{"errorName": "GENERIC_INTERNAL_ERROR", "message": "no such file"}
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
	for _, st := range ft.stmts {
		if strings.Contains(st, "s3cret") || strings.Contains(st, "db.example.com") {
			t.Errorf("the connection is in a statement: %s", st)
		}
	}
	have, _ := files.Read(ctx)
	var url string
	for k, v := range have {
		if strings.HasSuffix(k, ".url") {
			url = v
			if !strings.Contains(ft.stmts[len(ft.stmts)-1], "${FILE:/etc/trino/sources/"+k+"}") {
				t.Errorf("the statement doesn't name %s: %s", k, ft.stmts[len(ft.stmts)-1])
			}
		}
	}
	if url != "jdbc:postgresql://db.example.com:5432/finance" || have["sales_finance.project"] != "sales" {
		t.Errorf("files: %v", have)
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
	if n := len(have); n != 5 { // url, user, password, project, made: the old version's gone
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
	for _, conn := range []map[string]string{{"database": "fin"}, {"host": "pg"}, {"host": "pg;x=1", "database": "fin"}, {"url": "http://pg"}} {
		if _, err := jdbcURL("postgresql", conn); err == nil {
			t.Errorf("%v: want refused", conn)
		}
	}
}
