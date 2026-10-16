package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestABucketsSizeLimit(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(body, &q)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(body))
	}))
	defer ts.Close()
	b := &Buckets{URL: ts.URL, AccessKey: "root", SecretKey: "secret"}
	if err := b.setQuota(context.Background(), "sales", 500<<30); err != nil {
		t.Fatal(err)
	}
	if err := b.setQuota(context.Background(), "sales", 0); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`PUT /minio/admin/v3/set-bucket-quota?bucket=sales {"quota":536870912000,"quotatype":"hard","size":536870912000}`,
		`PUT /minio/admin/v3/set-bucket-quota?bucket=sales {"quota":0,"quotatype":"hard","size":0}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
