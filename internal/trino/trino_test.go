package trino

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExecFollowsTheStatementToItsEnd(t *testing.T) {
	var ts *httptest.Server
	busy := 1
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "maintenance" || p != "s3cret" || r.Header.Get("X-Trino-User") != "maintenance" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/statement":
			sql, _ := io.ReadAll(r.Body)
			if busy > 0 { // too busy, the first time: it's asked again
				busy--
				w.WriteHeader(503)
				return
			}
			if r.Header.Get("X-Trino-Session") != "iceberg.expire_snapshots_min_retention=1m" {
				t.Errorf("session: %q", r.Header.Get("X-Trino-Session"))
			}
			if string(sql) == "bad" {
				w.Write([]byte(`{"id":"q2","stats":{"state":"FAILED"},"error":{"message":"Access Denied: Cannot execute table procedure OPTIMIZE","errorName":"PERMISSION_DENIED"}}`))
				return
			}
			w.Write([]byte(`{"id":"q1","nextUri":"` + ts.URL + `/v1/statement/executing/q1/1","stats":{"state":"QUEUED"}}`))
		case r.URL.Path == "/v1/statement/executing/q1/1":
			w.Write([]byte(`{"id":"q1","nextUri":"` + ts.URL + `/v1/statement/executing/q1/2","stats":{"state":"RUNNING"},"data":[[1]]}`))
		case r.URL.Path == "/v1/statement/executing/q1/2":
			w.Write([]byte(`{"id":"q1","stats":{"state":"FINISHED"},"data":[[2]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := &Client{URL: ts.URL, User: "maintenance", Password: "s3cret", HTTP: ts.Client()}
	rows, err := c.Exec(context.Background(), "ALTER TABLE t EXECUTE optimize", "iceberg.expire_snapshots_min_retention=1m")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %v, %v; want both pages' rows", rows, err)
	}
	busy = 0
	_, err = c.Exec(context.Background(), "bad", "iceberg.expire_snapshots_min_retention=1m")
	var te *Error
	if !errors.As(err, &te) || te.Name != "PERMISSION_DENIED" || !strings.Contains(err.Error(), "OPTIMIZE") {
		t.Errorf("a refused statement: %v", err)
	}
	c.Password = "wrong"
	if _, err := c.Exec(context.Background(), "SELECT 1"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a wrong password: %v", err)
	}
}

func TestQuote(t *testing.T) {
	if got := Quote(`we"ird`); got != `"we""ird"` {
		t.Errorf("Quote: %s", got)
	}
}
