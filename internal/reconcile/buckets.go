package reconcile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/signer"
	"github.com/minio/minio-go/v7/pkg/tags"

	"github.com/StorScale/dataworks/internal/project"
)

// Buckets is Buckets' S3 API (for buckets) and admin API (for policies).
//
// On an external store (External: a company's MinIO, storage.external), what
// it makes is marked as the platform's (a bucket with the tag
// dataworks=owned, a policy with the ID dataworks), and it changes nothing
// that isn't: a project whose bucket or group's policy is someone else's
// there is refused, and removing a policy that isn't the platform's leaves
// it. On the platform's own Buckets everything is its own, unmarked.
type Buckets struct {
	URL, AccessKey, SecretKey string
	External                  bool
	s3                        *minio.Client
}

// The marks of what the platform made in the store.
const (
	ownerTag      = "dataworks"
	ownerTagValue = "owned"
	ownerPolicyID = "dataworks"
)

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
	if err != nil {
		return err
	}
	if ok {
		if !b.External {
			return nil
		}
		t, err := c.GetBucketTagging(ctx, name)
		if err != nil && minio.ToErrorResponse(err).Code != "NoSuchTagSet" {
			return err
		}
		if t == nil || t.ToMap()[ownerTag] != ownerTagValue {
			return fmt.Errorf("the store already has a bucket %s that isn't the platform's (no tag %s=%s): name the project's another (files.bucket)", name, ownerTag, ownerTagValue)
		}
		return nil
	}
	if err := c.MakeBucket(ctx, name, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil || !b.External {
		return err
	}
	t, err := tags.NewTags(map[string]string{ownerTag: ownerTagValue}, false)
	if err != nil {
		return err
	}
	return c.SetBucketTagging(ctx, name, t)
}

// policyOwner is who a policy in the store is: "" when there is none, else
// its ID (ownerPolicyID for the platform's).
func (b *Buckets) policyOwner(ctx context.Context, name string) (exists bool, id string, err error) {
	data, err := b.adminRead(ctx, "GET", "/info-canned-policy", url.Values{"name": {name}, "v": {"2"}}, nil)
	if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusBadRequest) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	var info struct {
		Policy struct {
			ID string `json:"ID"`
		} `json:"Policy"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return true, "", err
	}
	return true, info.Policy.ID, nil
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
	if b.External {
		exists, id, err := b.policyOwner(ctx, name)
		if err != nil {
			return err
		}
		if exists && id != ownerPolicyID {
			return fmt.Errorf("the store already has a policy %s that isn't the platform's (its ID isn't %s)", name, ownerPolicyID)
		}
	}
	doc := map[string]any{"Version": "2012-10-17", "Statement": statements}
	if b.External {
		doc["Id"] = ownerPolicyID
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return b.admin(ctx, "PUT", "/add-canned-policy", url.Values{"name": {name}}, data)
}

// setQuota sets a bucket's hard size limit in bytes; 0 takes it away.
func (b *Buckets) setQuota(ctx context.Context, bucket string, bytes int64) error {
	body, _ := json.Marshal(map[string]any{"quota": bytes, "size": bytes, "quotatype": "hard"})
	return b.admin(ctx, "PUT", "/set-bucket-quota", url.Values{"bucket": {bucket}}, body)
}

func (b *Buckets) deletePolicy(ctx context.Context, name string) error {
	if b.External {
		exists, id, err := b.policyOwner(ctx, name)
		if err != nil || !exists || id != ownerPolicyID {
			return err // none, or someone else's: left as it is
		}
	}
	err := b.admin(ctx, "DELETE", "/remove-canned-policy", url.Values{"name": {name}}, nil)
	if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusBadRequest) {
		return nil // not there
	}
	return err
}
