package main

import (
	"crypto/md5"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A store as a MinIO from before 2025 is: a multi-object delete needs a
// Content-MD5, and it must be the body's.
func oldMinIO(t *testing.T, seen *[]*http.Request, bodies *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*seen = append(*seen, r.Clone(r.Context()))
		*bodies = append(*bodies, string(body))
		if r.Method == http.MethodPost && hasQueryKey(r.URL.RawQuery, "delete") {
			sum := md5.Sum(body)
			switch r.Header.Get("Content-MD5") {
			case "":
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, "<Error><Code>MissingContentMD5</Code><Message>Missing required header for this request: Content-Md5.</Message></Error>")
				return
			case base64.StdEncoding.EncodeToString(sum[:]):
			default:
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, "<Error><Code>BadDigest</Code></Error>")
				return
			}
			io.WriteString(w, "<DeleteResult/>")
			return
		}
		io.WriteString(w, "object:"+r.URL.Path)
	}))
}

func TestStoreForwarder(t *testing.T) {
	var seen []*http.Request
	var bodies []string
	store := oldMinIO(t, &seen, &bodies)
	defer store.Close()
	fwd, err := newStoreForwarder(store.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(fwd)
	defer front.Close()

	send := func(method, path, body string, header map[string]string) (int, string) {
		r, _ := http.NewRequest(method, front.URL+path, strings.NewReader(body))
		r.Host = "buckets:9000" // as the platform's services address it, and sign it
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=k/20261109/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=abc")
		r.Header.Set("X-Amz-Checksum-Crc32", "AAAAAA==")
		for k, v := range header {
			r.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	const keys = `<Delete><Object><Key>t/metadata/snap-1.avro</Key></Object><Object><Key>t/data/a.parquet</Key></Object></Delete>`

	// A delete as the SDKs since 2025 send it: a CRC32, no Content-MD5. The forwarder adds the body's.
	if code, body := send("POST", "/warehouse?delete", keys, nil); code != 200 || body != "<DeleteResult/>" {
		t.Fatalf("a delete without Content-MD5: %d %s", code, body)
	}
	last := seen[len(seen)-1]
	if last.Host != "buckets:9000" || !strings.HasPrefix(last.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=k/") ||
		last.Header.Get("X-Amz-Checksum-Crc32") != "AAAAAA==" || bodies[len(bodies)-1] != keys {
		t.Errorf("the delete as the store had it: Host %q, Authorization %q, its CRC32 %q, body %q",
			last.Host, last.Header.Get("Authorization"), last.Header.Get("X-Amz-Checksum-Crc32"), bodies[len(bodies)-1])
	}
	if last.Header.Get("X-Forwarded-For") != "" {
		t.Error("X-Forwarded-For added: the request goes on as it came")
	}

	// One with its own Content-MD5 keeps it (a wrong one is the store's to refuse, not the forwarder's to mend).
	if code, _ := send("POST", "/warehouse?delete=", keys, map[string]string{"Content-MD5": "d3Jvbmc="}); code != http.StatusBadRequest ||
		seen[len(seen)-1].Header.Get("Content-MD5") != "d3Jvbmc=" {
		t.Errorf("a delete's own Content-MD5 was changed: %d %q", code, seen[len(seen)-1].Header.Get("Content-MD5"))
	}

	// aws-chunked: its bytes aren't the payload's, so no sum is added.
	send("POST", "/warehouse?delete", keys, map[string]string{"Content-Encoding": "aws-chunked", "X-Amz-Content-Sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"})
	if seen[len(seen)-1].Header.Get("Content-MD5") != "" {
		t.Error("a Content-MD5 was added to an aws-chunked body")
	}

	// Everything else passes as it came: an upload, a read, a POST that isn't a delete.
	for _, c := range [][3]string{{"PUT", "/warehouse/t/data/b.parquet", "parquet bytes"}, {"GET", "/warehouse/t/data/b.parquet", ""},
		{"POST", "/warehouse/t/data/big.parquet?uploads", ""}, {"GET", "/warehouse?list-type=2&prefix=deleted/", ""}} {
		code, _ := send(c[0], c[1], c[2], nil)
		r := seen[len(seen)-1]
		if code != 200 || r.Header.Get("Content-MD5") != "" || r.Host != "buckets:9000" || bodies[len(bodies)-1] != c[2] {
			t.Errorf("%s %s: %d, Content-MD5 %q, Host %q, body %q", c[0], c[1], code, r.Header.Get("Content-MD5"), r.Host, bodies[len(bodies)-1])
		}
	}
}

func TestStoreForwarderEndpoint(t *testing.T) {
	for _, bad := range []string{"", "minio:9000", "ftp://minio", "https://"} {
		if _, err := newStoreForwarder(bad, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Errorf("%q taken as the store's endpoint", bad)
		}
	}
}

func TestHasQueryKey(t *testing.T) {
	for raw, want := range map[string]bool{"delete": true, "delete=": true, "x=1&delete": true, "deleted=1": false, "prefix=delete": false, "": false} {
		if hasQueryKey(raw, "delete") != want {
			t.Errorf("%q: want %v", raw, want)
		}
	}
}
