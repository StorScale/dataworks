package reconcile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/signer"

	"github.com/StorScale/dataworks/internal/project"
)

// Buckets is Buckets' S3 API (for buckets) and admin API (for policies).
type Buckets struct {
	URL, AccessKey, SecretKey string
	s3                        *minio.Client
}

func (b *Buckets) client() (*minio.Client, error) {
	if b.s3 == nil {
		u, err := url.Parse(b.URL)
		if err != nil {
			return nil, err
		}
		b.s3, err = minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(b.AccessKey, b.SecretKey, ""),
			Secure: u.Scheme == "https", Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
		if err != nil {
			return nil, err
		}
	}
	return b.s3, nil
}

func (b *Buckets) ensureBucket(ctx context.Context, name string) error {
	c, err := b.client()
	if err != nil {
		return err
	}
	ok, err := c.BucketExists(ctx, name)
	if err != nil || ok {
		return err
	}
	return c.MakeBucket(ctx, name, minio.MakeBucketOptions{Region: "us-east-1"})
}

// admin sends a request to the admin API (/minio/admin/v3), signed with SigV4.
func (b *Buckets) admin(ctx context.Context, method, path string, query url.Values, body []byte) error {
	_, err := b.adminRead(ctx, method, path, query, body)
	return err
}

// BucketSizes are the buckets' sizes in bytes, as Buckets last counted them
// (its data usage scan): for usage per project (13.1).
func (b *Buckets) BucketSizes(ctx context.Context) (map[string]int64, error) {
	data, err := b.adminRead(ctx, "GET", "/datausageinfo", url.Values{}, nil)
	if err != nil {
		return nil, err
	}
	var info struct {
		Buckets map[string]struct {
			Size int64 `json:"size"`
		} `json:"bucketsUsageInfo"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for name, u := range info.Buckets {
		out[name] = u.Size
	}
	return out, nil
}

// adminRead is admin, with the answer's body.
func (b *Buckets) adminRead(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	u := b.URL + "/minio/admin/v3" + path + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
	req = signer.SignV4(*req, b.AccessKey, b.SecretKey, "", "us-east-1")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, &HTTPError{Method: method, URL: u, Status: res.StatusCode, Body: string(msg)}
	}
	return io.ReadAll(io.LimitReader(res.Body, 16<<20))
}

func (b *Buckets) putPolicy(ctx context.Context, name string, statements []project.Statement) error {
	doc, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return err
	}
	return b.admin(ctx, "PUT", "/add-canned-policy", url.Values{"name": {name}}, doc)
}

// setQuota sets a bucket's hard size limit in bytes; 0 takes it away.
func (b *Buckets) setQuota(ctx context.Context, bucket string, bytes int64) error {
	body, _ := json.Marshal(map[string]any{"quota": bytes, "size": bytes, "quotatype": "hard"})
	return b.admin(ctx, "PUT", "/set-bucket-quota", url.Values{"bucket": {bucket}}, body)
}

func (b *Buckets) deletePolicy(ctx context.Context, name string) error {
	err := b.admin(ctx, "DELETE", "/remove-canned-policy", url.Values{"name": {name}}, nil)
	if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusBadRequest) {
		return nil // not there
	}
	return err
}
