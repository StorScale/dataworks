package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// A project's connections, as its Connections tab keeps them (use case
// 7.6): credentials given to the platform once, never shown again.

// ConnectionMeta is what's kept beside a connection's values: its kind,
// who added it and when, who last changed it.
type ConnectionMeta struct {
	Name      string    `json:"name"`
	Kind      string    `json:"kind,omitempty"` // empty: made outside the platform (kubectl)
	AddedBy   string    `json:"addedBy,omitempty"`
	AddedAt   time.Time `json:"addedAt,omitzero"`
	ChangedBy string    `json:"changedBy,omitempty"`
	ChangedAt time.Time `json:"changedAt,omitzero"`
}

// A StoredConnection is a connection, with its values.
type StoredConnection struct {
	ConnectionMeta
	Data map[string]string
}

// ErrNotTheProjects is a name taken by something that isn't the project's
// connection: the platform's own Secret, or another project's.
var ErrNotTheProjects = errors.New("that name is taken by something that isn't one of the project's connections")

// ConnectionStore keeps projects' connections (a Store is one).
type ConnectionStore interface {
	Connections(ctx context.Context, project string) ([]StoredConnection, error)
	// PutConnection adds or replaces one (ErrNotTheProjects when the name is
	// something else's).
	PutConnection(ctx context.Context, project string, c StoredConnection) error
	// DeleteConnection removes one of the project's; one that isn't there is no error.
	DeleteConnection(ctx context.Context, project, name string) error
}

func checkNames(project, name string) error {
	if project == "" || name == "" || strings.ContainsAny(project+name, "/\\") || strings.HasPrefix(name, ".") {
		return errors.New("a connection's name has no slashes")
	}
	return nil
}

func sortConns(cs []StoredConnection) []StoredConnection {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	return cs
}

// --- In the platform's bucket: connections/<project>/<name>.json, and beside
// it connection-meta/<project>/<name>.json.

func (s *Bucket) Connections(ctx context.Context, project string) ([]StoredConnection, error) {
	if err := checkNames(project, "x"); err != nil {
		return nil, err
	}
	keys, err := s.ListFiles(ctx, "connections/"+project+"/")
	if err != nil {
		return nil, err
	}
	out := []StoredConnection{}
	for _, k := range keys {
		name, ok := strings.CutSuffix(strings.TrimPrefix(k, "connections/"+project+"/"), ".json")
		if !ok || strings.Contains(name, "/") {
			continue
		}
		data, err := s.keys(ctx, k)
		if err != nil || data == nil {
			continue
		}
		c := StoredConnection{ConnectionMeta: ConnectionMeta{Name: name}, Data: data}
		if raw, err := s.get(ctx, "connection-meta/"+project+"/"+name+".json"); err == nil && raw != nil {
			json.Unmarshal(raw, &c.ConnectionMeta)
			c.Name = name
		}
		out = append(out, c)
	}
	return sortConns(out), nil
}

func (s *Bucket) PutConnection(ctx context.Context, project string, c StoredConnection) error {
	if err := checkNames(project, c.Name); err != nil {
		return err
	}
	if err := s.put(ctx, "connections/"+project+"/"+c.Name+".json", c.Data); err != nil {
		return err
	}
	return s.put(ctx, "connection-meta/"+project+"/"+c.Name+".json", c.ConnectionMeta)
}

func (s *Bucket) DeleteConnection(ctx context.Context, project, name string) error {
	if err := checkNames(project, name); err != nil {
		return err
	}
	for _, k := range []string{"connections/" + project + "/" + name + ".json", "connection-meta/" + project + "/" + name + ".json"} {
		if err := s.c.RemoveObject(ctx, s.bucket, k, minio.RemoveObjectOptions{}); err != nil && minio.ToErrorResponse(err).Code != "NoSuchKey" {
			return err
		}
	}
	return nil
}

// --- On Kubernetes: a Secret labelled ConnectionLabel=<project>, its meta as
// annotations.

const annotation = "platform.storscale.io/"

type kubeSecret struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string            `json:"name"`
		ResourceVersion string            `json:"resourceVersion,omitempty"`
		Labels          map[string]string `json:"labels,omitempty"`
		Annotations     map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Type string            `json:"type,omitempty"`
	Data map[string][]byte `json:"data"`
}

func (k *Kube) secretsURL() string {
	return strings.TrimSuffix(strings.Replace(k.URL, "/apis/platform.storscale.io/v1alpha1/", "/api/v1/", 1), "/projects") + "/secrets"
}

func (sec kubeSecret) connection() StoredConnection {
	a := sec.Metadata.Annotations
	c := StoredConnection{ConnectionMeta: ConnectionMeta{Name: sec.Metadata.Name, Kind: a[annotation+"kind"],
		AddedBy: a[annotation+"added-by"], ChangedBy: a[annotation+"changed-by"]}, Data: map[string]string{}}
	c.AddedAt, _ = time.Parse(time.RFC3339, a[annotation+"added-at"])
	c.ChangedAt, _ = time.Parse(time.RFC3339, a[annotation+"changed-at"])
	for key, v := range sec.Data {
		c.Data[key] = string(v)
	}
	return c
}

func (k *Kube) Connections(ctx context.Context, project string) ([]StoredConnection, error) {
	var list struct {
		Items []kubeSecret `json:"items"`
	}
	sel := url.QueryEscape(ConnectionLabel + "=" + project)
	if _, err := k.do(ctx, "GET", k.secretsURL()+"?labelSelector="+sel, "", nil, &list); err != nil {
		return nil, err
	}
	out := []StoredConnection{}
	for _, s := range list.Items {
		out = append(out, s.connection())
	}
	return sortConns(out), nil
}

func (k *Kube) PutConnection(ctx context.Context, project string, c StoredConnection) error {
	if err := checkNames(project, c.Name); err != nil {
		return err
	}
	var old kubeSecret
	found, err := k.do(ctx, "GET", k.secretsURL()+"/"+c.Name, "", nil, &old)
	if err != nil {
		return err
	}
	if found && old.Metadata.Labels[ConnectionLabel] != project {
		return ErrNotTheProjects
	}
	sec := kubeSecret{APIVersion: "v1", Kind: "Secret", Type: "Opaque", Data: map[string][]byte{}}
	sec.Metadata.Name = c.Name
	sec.Metadata.Labels = map[string]string{ConnectionLabel: project}
	sec.Metadata.Annotations = map[string]string{annotation + "kind": c.Kind, annotation + "added-by": c.AddedBy,
		annotation + "added-at": c.AddedAt.UTC().Format(time.RFC3339)}
	if c.ChangedBy != "" {
		sec.Metadata.Annotations[annotation+"changed-by"] = c.ChangedBy
		sec.Metadata.Annotations[annotation+"changed-at"] = c.ChangedAt.UTC().Format(time.RFC3339)
	}
	for key, v := range c.Data {
		sec.Data[key] = []byte(v)
	}
	if found {
		sec.Metadata.ResourceVersion = old.Metadata.ResourceVersion
		_, err = k.do(ctx, "PUT", k.secretsURL()+"/"+c.Name, "application/json", sec, nil)
	} else {
		_, err = k.do(ctx, "POST", k.secretsURL(), "application/json", sec, nil)
	}
	return err
}

func (k *Kube) DeleteConnection(ctx context.Context, project, name string) error {
	if err := checkNames(project, name); err != nil {
		return err
	}
	var old kubeSecret
	found, err := k.do(ctx, "GET", k.secretsURL()+"/"+name, "", nil, &old)
	if err != nil || !found {
		return err
	}
	if old.Metadata.Labels[ConnectionLabel] != project {
		return ErrNotTheProjects
	}
	_, err = k.do(ctx, "DELETE", k.secretsURL()+"/"+name, "", nil, nil)
	if err != nil {
		return fmt.Errorf("removing %s: %w", name, err)
	}
	return nil
}
