package catalog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientCredentials(t *testing.T) {
	var issued atomic.Int32
	kc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "lakehouse-catalog" || r.Form.Get("client_secret") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		issued.Add(1)
		io.WriteString(w, `{"access_token":"tok","expires_in":300}`)
	}))
	defer kc.Close()
	var got string
	nessie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("Authorization") }))
	defer nessie.Close()

	t.Setenv("CATALOG_TOKEN_URL", kc.URL)
	t.Setenv("CATALOG_CLIENT_SECRET", "s3cret")
	c := NessieHTTP(5 * time.Second)
	for range 3 {
		res, err := c.Get(nessie.URL)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if got != "Bearer tok" || issued.Load() != 1 {
		t.Fatalf("Authorization %q, %d tokens fetched", got, issued.Load())
	}

	t.Setenv("CATALOG_CLIENT_SECRET", "wrong")
	if _, err := NessieHTTP(5 * time.Second).Get(nessie.URL); err == nil {
		t.Fatal("a refused account still reached the catalog")
	}
	t.Setenv("CATALOG_CLIENT_SECRET", "")
	got = "unset"
	res, err := NessieHTTP(5 * time.Second).Get(nessie.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got != "" {
		t.Fatalf("no account, yet Authorization %q", got)
	}
}
