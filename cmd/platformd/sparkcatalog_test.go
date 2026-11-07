package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSparkGrant(t *testing.T) {
	key := []byte("k")
	now := time.Unix(1000, 0)
	tok := sparkGrant{User: "bob", Tables: map[string]bool{"sales.orders": true}, Expires: 2000}.sign(key)
	g, err := parseSparkGrant(tok, key, now)
	if err != nil || g.User != "bob" || !g.Tables["sales.orders"] {
		t.Fatalf("a good grant: %+v, %v", g, err)
	}
	if _, err := parseSparkGrant(tok, []byte("other"), now); err == nil {
		t.Fatal("another key's grant was taken")
	}
	body, sig, _ := strings.Cut(tok, ".")
	forged := sparkGrant{User: "bob", Tables: map[string]bool{"sales.payroll": true}, Expires: 2000}.sign([]byte("forger"))
	fbody, _, _ := strings.Cut(forged, ".")
	if _, err := parseSparkGrant(fbody+"."+sig, key, now); err == nil {
		t.Fatal("a grant with another's body was taken")
	}
	if _, err := parseSparkGrant(body+"."+sig, key, time.Unix(2000, 0)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired grant: %v", err)
	}
	if _, err := parseSparkGrant(tok, nil, now); err == nil {
		t.Fatal("a grant was taken with no key")
	}
}

func TestSparkCommitAllowed(t *testing.T) {
	var u sparkUpdates
	json.Unmarshal([]byte(`{"updates":[{"action":"add-snapshot"},{"action":"set-snapshot-ref"},{"action":"set-properties","updates":{"comment":"x"}}]}`), &u)
	if err := sparkCommitAllowed(u); err != nil {
		t.Fatalf("an append: %v", err)
	}
	for _, body := range []string{
		`{"updates":[{"action":"set-location","location":"s3://scratch/x"}]}`,
		`{"updates":[{"action":"set-properties","updates":{"write.data.path":"s3://scratch/x"}}]}`,
		`{"updates":[{"action":"remove-properties","removals":["write.metadata.path"]}]}`,
		`{"updates":[{"action":"assign-uuid","uuid":"x"}]}`,
	} {
		var u sparkUpdates
		json.Unmarshal([]byte(body), &u)
		if sparkCommitAllowed(u) == nil {
			t.Errorf("allowed: %s", body)
		}
	}
}

// A stand-in Nessie: its config (with its address and its own keys, which the
// session doesn't get), and whatever else is asked, recorded.
func fakeNessie(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		if r.URL.Path == "/iceberg/v1/config" {
			io.WriteString(w, `{"defaults":{"warehouse":"s3://warehouse/"},"overrides":{"prefix":"main%7Cwarehouse","uri":"http://nessie:19120/iceberg/","nessie.core-base-uri":"http://nessie:19120/api/"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"metadata-location":"s3://warehouse/sales/x/metadata/1.json"}`)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestSparkCatalog(t *testing.T) {
	nessie, seen := fakeNessie(t)
	t.Setenv("NESSIE_URL", nessie.URL)
	t.Setenv("SPARK_S3_SECRET_KEY", "spark-secret")
	t.Setenv("CATALOG_CLIENT_SECRET", "")
	s := &server{}
	grant := sparkGrant{User: "bob", Tables: map[string]bool{"sales.orders": true, "sales.payroll": false}, Expires: time.Now().Add(time.Hour).Unix()}.sign(sparkCatalogKey())
	call := func(method, path, body, token string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.handleSparkCatalog(w, r)
		return w.Code, w.Body.String()
	}
	const base = sparkCatalogPath + "v1/main%7Cwarehouse/namespaces"

	if code, _ := call("GET", sparkCatalogPath+"v1/config?warehouse=warehouse", "", ""); code != http.StatusUnauthorized {
		t.Errorf("no grant: HTTP %d", code)
	}
	code, body := call("GET", sparkCatalogPath+"v1/config?warehouse=warehouse", "", grant)
	if code != 200 || strings.Contains(body, "nessie:19120") || !strings.Contains(body, "main%7Cwarehouse") {
		t.Errorf("config: HTTP %d %s", code, body)
	}
	if code, body := call("GET", base, "", grant); code != 200 || body != `{"namespaces":[["sales"]]}`+"\n" {
		t.Errorf("namespaces: HTTP %d %s", code, body)
	}
	if code, body := call("GET", base+"/sales/tables", "", grant); code != 200 || !strings.Contains(body, `"name":"orders"`) || !strings.Contains(body, `"name":"payroll"`) {
		t.Errorf("tables: HTTP %d %s", code, body)
	}
	if code, _ := call("GET", base+"/finance", "", grant); code != http.StatusNotFound {
		t.Errorf("another namespace: HTTP %d", code)
	}
	if code, body := call("GET", base+"/sales/tables/orders", "", grant); code != 200 || !strings.Contains(body, "metadata-location") {
		t.Errorf("load orders: HTTP %d %s", code, body)
	}
	if code, _ := call("GET", base+"/sales/tables/secrets", "", grant); code != http.StatusForbidden {
		t.Errorf("load a table not granted: HTTP %d", code)
	}
	if code, _ := call("GET", sparkCatalogPath+"v1/dev%7Cwarehouse/namespaces/sales/tables/orders", "", grant); code != http.StatusForbidden {
		t.Errorf("another branch: HTTP %d", code)
	}
	appendCommit := `{"requirements":[],"updates":[{"action":"add-snapshot","snapshot":{}}]}`
	if code, _ := call("POST", base+"/sales/tables/payroll", appendCommit, grant); code != http.StatusForbidden {
		t.Errorf("a commit to a table read only: HTTP %d", code)
	}
	if code, _ := call("POST", base+"/sales/tables/orders", appendCommit, grant); code != 200 {
		t.Errorf("a commit to a table it may change: HTTP %d", code)
	}
	if code, _ := call("POST", base+"/sales/tables/orders", `{"updates":[{"action":"set-location","location":"s3://scratch/x"}]}`, grant); code != http.StatusForbidden {
		t.Errorf("moving a table: HTTP %d", code)
	}
	for _, c := range [][3]string{
		{"POST", base + "/sales/tables", `{"name":"new"}`}, // create
		{"DELETE", base + "/sales/tables/orders", ""},      // drop
		{"POST", sparkCatalogPath + "v1/main%7Cwarehouse/tables/rename", `{}`},
		{"POST", base, `{"namespace":["x"]}`},
		{"POST", sparkCatalogPath + "v1/main%7Cwarehouse/transactions/commit", `{"table-changes":[{"identifier":{"namespace":["sales"],"name":"payroll"},"updates":[]}]}`},
	} {
		if code, _ := call(c[0], c[1], c[2], grant); code != http.StatusForbidden {
			t.Errorf("%s %s: HTTP %d", c[0], c[1], code)
		}
	}
	for _, req := range seen() {
		if strings.HasPrefix(req, "DELETE") || req == "POST /iceberg/v1/main%7Cwarehouse/namespaces/sales/tables" ||
			strings.Contains(req, "payroll") || strings.Contains(req, "secrets") || strings.Contains(req, "dev%7C") {
			t.Errorf("reached Nessie: %s", req)
		}
	}
}
