package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
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

// fakeStore is just enough of MinIO's S3 and admin APIs for the platform's
// marks: buckets (with tags) and canned policies (with IDs).
type fakeStore struct {
	buckets  map[string]map[string]string // name -> tags
	policies map[string]string            // name -> ID
}

func (f *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	name := r.URL.Query().Get("name")
	switch p := strings.Trim(r.URL.Path, "/"); {
	case p == "minio/admin/v3/info-canned-policy":
		id, ok := f.policies[name]
		if !ok {
			http.Error(w, `{"Code":"XMinioAdminNoSuchPolicy"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"PolicyName": name, "Policy": map[string]any{"ID": id}})
	case p == "minio/admin/v3/add-canned-policy":
		var doc struct{ Id string }
		json.Unmarshal(body, &doc)
		f.policies[name] = doc.Id
	case p == "minio/admin/v3/remove-canned-policy":
		delete(f.policies, name)
	case r.URL.Query().Has("tagging"):
		t, ok := f.buckets[p]
		switch {
		case !ok:
			http.Error(w, "<Error><Code>NoSuchBucket</Code></Error>", http.StatusNotFound)
		case r.Method == "PUT":
			for _, kv := range strings.Split(string(body), "<Tag>")[1:] {
				k := strings.SplitN(strings.SplitN(kv, "<Key>", 2)[1], "</Key>", 2)[0]
				v := strings.SplitN(strings.SplitN(kv, "<Value>", 2)[1], "</Value>", 2)[0]
				t[k] = v
			}
		case len(t) == 0:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("<Error><Code>NoSuchTagSet</Code><Message>no tags</Message></Error>"))
		default:
			out := "<Tagging><TagSet>"
			for k, v := range t {
				out += "<Tag><Key>" + k + "</Key><Value>" + v + "</Value></Tag>"
			}
			w.Write([]byte(out + "</TagSet></Tagging>"))
		}
	case r.Method == "HEAD":
		if _, ok := f.buckets[p]; !ok {
			w.WriteHeader(http.StatusNotFound)
		}
	case r.Method == "PUT":
		f.buckets[p] = map[string]string{}
	default:
		http.NotFound(w, r)
	}
}

// On a company's store the platform changes only what it marked as its own:
// a bucket or a policy someone else has there is refused, and left as it is.
func TestOnAnExternalStoreOnlyThePlatformsOwnIsChanged(t *testing.T) {
	f := &fakeStore{buckets: map[string]map[string]string{"lending": {}}, policies: map[string]string{"analysts": ""}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	ctx := context.Background()
	b := &Buckets{URL: ts.URL, AccessKey: "admin", SecretKey: "secret", External: true}
	st := []project.Statement{{Effect: "Allow", Action: []string{"s3:GetObject"}, Resource: []string{"arn:aws:s3:::sales/*"}}}

	if err := b.ensureBucket(ctx, "sales"); err != nil || f.buckets["sales"]["dataworks"] != "owned" {
		t.Fatalf("a new bucket, marked: %v %v", err, f.buckets["sales"])
	}
	if err := b.ensureBucket(ctx, "sales"); err != nil {
		t.Errorf("its own bucket again: %v", err)
	}
	if err := b.ensureBucket(ctx, "lending"); err == nil || !strings.Contains(err.Error(), "isn't the platform's") {
		t.Errorf("someone else's bucket: %v", err)
	}
	if err := b.putPolicy(ctx, "sales-readers", st); err != nil || f.policies["sales-readers"] != "dataworks" {
		t.Fatalf("a new policy, marked: %v %v", err, f.policies)
	}
	if err := b.putPolicy(ctx, "sales-readers", st); err != nil {
		t.Errorf("its own policy again: %v", err)
	}
	if err := b.putPolicy(ctx, "analysts", st); err == nil || !strings.Contains(err.Error(), "isn't the platform's") {
		t.Errorf("someone else's policy: %v", err)
	}
	if err := b.deletePolicy(ctx, "analysts"); err != nil || f.policies["analysts"] != "" {
		t.Errorf("someone else's policy is left: %v %v", err, f.policies)
	} else if _, kept := f.policies["analysts"]; !kept {
		t.Error("someone else's policy was removed")
	}
	if err := b.deletePolicy(ctx, "sales-readers"); err != nil {
		t.Fatal(err)
	} else if _, kept := f.policies["sales-readers"]; kept {
		t.Error("its own policy wasn't removed")
	}

	// The platform's own Buckets: everything there is its own, marked or not (installs before the marks).
	own := &Buckets{URL: ts.URL, AccessKey: "root", SecretKey: "secret"}
	if err := own.ensureBucket(ctx, "lending"); err != nil {
		t.Errorf("its own Buckets' bucket: %v", err)
	}
	if err := own.putPolicy(ctx, "analysts", st); err != nil || f.policies["analysts"] != "" {
		t.Errorf("its own Buckets' policy, unmarked as before: %v %v", err, f.policies)
	}
	if err := own.ensureBucket(ctx, "retail"); err != nil || len(f.buckets["retail"]) != 0 {
		t.Errorf("its own Buckets' new bucket, unmarked as before: %v %v", err, f.buckets["retail"])
	}
}
