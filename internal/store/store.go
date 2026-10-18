// Package store keeps projects, and the operator's status for each.
// platformd writes projects; the operator writes status. Where depends on
// where the platform runs:
//
//   - Bucket: JSON objects in a Buckets bucket, projects/<name>.json and
//     status/<name>.json, and the secrets projects name, secrets/<name>.json
//     (Compose);
//   - Kube: Project resources, with their status (Kubernetes);
//   - Mirror: Kube, with a copy of every project and status kept in the
//     bucket, for the services that read projects there.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/StorScale/storscale-platform/internal/project"
)

// Store is where projects and their status live.
type Store interface {
	Projects(ctx context.Context) ([]*project.Project, error)
	// Project is nil, with no error, when there's no such project.
	Project(ctx context.Context, name string) (*project.Project, error)
	PutProject(ctx context.Context, p *project.Project) error
	DeleteProject(ctx context.Context, name string) error
	// Statuses includes those of projects being deleted, which the operator
	// still has to undo.
	Statuses(ctx context.Context) ([]*project.Status, error)
	Status(ctx context.Context, name string) (*project.Status, error)
	PutStatus(ctx context.Context, st *project.Status) error
	// DeleteStatus says the operator is done with a project: it has undone it.
	DeleteStatus(ctx context.Context, name string) error
	// Connection is one of a project's connections (credentials it names,
	// such as its git repo's), as keys and values: nil, with no error, when
	// the project has none of that name. A project reaches its own only: on
	// Kubernetes, a Secret labelled ConnectionLabel=<project> (a Secret
	// without the label, the platform's or another project's, is refused);
	// otherwise connections/<project>/<name>.json.
	Connection(ctx context.Context, project, name string) (map[string]string, error)
	// Secret is one of the platform's own secrets (a project's Airflow's keys,
	// which the operator makes): nil, with no error, when there's none. On
	// Kubernetes, a Secret in the namespace; otherwise secrets/<name>.json.
	Secret(ctx context.Context, name string) (map[string]string, error)
	ConnectionStore
}

// ConnectionLabel marks a Secret as a project's connection: its value is the project.
const ConnectionLabel = "platform.storscale.io/connection"

type Bucket struct {
	c      *minio.Client
	bucket string
}

// New connects to the bucket at endpoint (http://buckets:9000), with these keys.
func New(endpoint, accessKey, secretKey, bucket string) (*Bucket, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, errors.New("store endpoint " + endpoint + " isn't a URL")
	}
	c, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: u.Scheme == "https", Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
	if err != nil {
		return nil, err
	}
	return &Bucket{c: c, bucket: bucket}, nil
}

func (s *Bucket) Projects(ctx context.Context) ([]*project.Project, error) {
	var out []*project.Project
	err := s.each(ctx, "projects/", func(data []byte) error {
		p, err := project.Parse(data)
		if err != nil {
			return err
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

// Project is nil, with no error, when there's no such project.
func (s *Bucket) Project(ctx context.Context, name string) (*project.Project, error) {
	data, err := s.get(ctx, "projects/"+name+".json")
	if data == nil || err != nil {
		return nil, err
	}
	return project.Parse(data)
}

func (s *Bucket) PutProject(ctx context.Context, p *project.Project) error {
	return s.put(ctx, "projects/"+p.Metadata.Name+".json", p)
}

func (s *Bucket) DeleteProject(ctx context.Context, name string) error {
	return s.c.RemoveObject(ctx, s.bucket, "projects/"+name+".json", minio.RemoveObjectOptions{})
}

func (s *Bucket) Statuses(ctx context.Context) ([]*project.Status, error) {
	var out []*project.Status
	err := s.each(ctx, "status/", func(data []byte) error {
		var st project.Status
		if err := json.Unmarshal(data, &st); err != nil {
			return err
		}
		out = append(out, &st)
		return nil
	})
	return out, err
}

func (s *Bucket) Status(ctx context.Context, name string) (*project.Status, error) {
	data, err := s.get(ctx, "status/"+name+".json")
	if data == nil || err != nil {
		return nil, err
	}
	var st project.Status
	return &st, json.Unmarshal(data, &st)
}

func (s *Bucket) PutStatus(ctx context.Context, st *project.Status) error {
	return s.put(ctx, "status/"+st.Name+".json", st)
}

func (s *Bucket) DeleteStatus(ctx context.Context, name string) error {
	return s.c.RemoveObject(ctx, s.bucket, "status/"+name+".json", minio.RemoveObjectOptions{})
}

func (s *Bucket) each(ctx context.Context, prefix string, f func([]byte) error) error {
	for obj := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if obj.Err != nil {
			return obj.Err
		}
		if !strings.HasSuffix(obj.Key, ".json") {
			continue
		}
		data, err := s.get(ctx, obj.Key)
		if err != nil {
			return err
		}
		if data != nil {
			if err := f(data); err != nil {
				return errors.New(obj.Key + ": " + err.Error())
			}
		}
	}
	return nil
}

func (s *Bucket) Secret(ctx context.Context, name string) (map[string]string, error) {
	return s.keys(ctx, "secrets/"+name+".json")
}

func (s *Bucket) Connection(ctx context.Context, project, name string) (map[string]string, error) {
	if strings.Contains(project+name, "/") { // both are validated names; never a path
		return nil, errors.New("a connection's name has no slashes")
	}
	return s.keys(ctx, "connections/"+project+"/"+name+".json")
}

// ListFiles are the keys of the platform's files under a prefix.
func (s *Bucket) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	for obj := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, obj.Key)
	}
	return out, nil
}

// GetFile is a file of the platform's; nil, with no error, when there's none.
func (s *Bucket) GetFile(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key)
}

// PutFile keeps a file of the platform's (dbt's artifacts of a project's
// run, as dbt/<project>/manifest.json) in the bucket, as given.
func (s *Bucket) PutFile(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s.c.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

// keys reads a JSON object of strings; nil, with no error, when there's none.
func (s *Bucket) keys(ctx context.Context, key string) (map[string]string, error) {
	data, err := s.get(ctx, key)
	if data == nil || err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errors.New(key + ": " + err.Error())
	}
	return out, nil
}

// get is nil, with no error, when there's no such object.
func (s *Bucket) get(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func (s *Bucket) put(ctx context.Context, key string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = s.c.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/json"})
	return err
}
