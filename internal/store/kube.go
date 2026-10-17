package store

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

// Finalizer keeps a deleted Project until the operator has undone it (taken
// its access out of Keycloak, Ranger and Buckets); then DeleteStatus removes it.
const Finalizer = "platform.storscale.io/undo"

// Kube keeps projects as Project resources (deploy/crds/project.yaml) in one
// namespace, through the Kubernetes API. A project being deleted (one with a
// deletionTimestamp) is no longer among Projects, but its status is among
// Statuses until the operator has undone it.
type Kube struct {
	URL   string // the API's base for the namespace's projects
	Token func() (string, error)
	HTTP  *http.Client
}

// NewKube is the store of the namespace the pod runs in, with the pod's
// service account.
func NewKube() (*Kube, error) {
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount"
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	ns, err := os.ReadFile(sa + "/namespace")
	if host == "" || err != nil {
		return nil, errors.New("not in a Kubernetes pod (no service account)")
	}
	ca, err := os.ReadFile(sa + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &Kube{
		URL: fmt.Sprintf("https://%s:%s/apis/platform.storscale.io/v1alpha1/namespaces/%s/projects", host, port, strings.TrimSpace(string(ns))),
		// Read each time: the kubelet rotates the token.
		Token: func() (string, error) { t, err := os.ReadFile(sa + "/token"); return strings.TrimSpace(string(t)), err },
		HTTP:  &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}, nil
}

// resource is a Project as the API has it.
type resource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name              string   `json:"name"`
		ResourceVersion   string   `json:"resourceVersion,omitempty"`
		Finalizers        []string `json:"finalizers,omitempty"`
		DeletionTimestamp string   `json:"deletionTimestamp,omitempty"`
	} `json:"metadata"`
	Spec   json.RawMessage `json:"spec,omitempty"`
	Status json.RawMessage `json:"status,omitempty"`
}

func (r *resource) project() (*project.Project, error) {
	data, _ := json.Marshal(map[string]any{"apiVersion": r.APIVersion, "kind": r.Kind,
		"metadata": map[string]string{"name": r.Metadata.Name}, "spec": r.Spec})
	return project.Parse(data)
}

func (r *resource) status() (*project.Status, error) {
	if len(r.Status) == 0 || string(r.Status) == "{}" || string(r.Status) == "null" {
		return nil, nil
	}
	var st project.Status
	if err := json.Unmarshal(r.Status, &st); err != nil {
		return nil, err
	}
	if st.Phase == "" {
		return nil, nil
	}
	st.Name = r.Metadata.Name
	return &st, nil
}

// call sends a request to the API. 404 is (nil, nil): no such project.
func (k *Kube) call(ctx context.Context, method, path, contentType string, body, out any) (bool, error) {
	return k.do(ctx, method, k.URL+path, contentType, body, out)
}

// Secret reads a Secret in the projects' namespace.
func (k *Kube) Secret(ctx context.Context, name string) (map[string]string, error) {
	data, _, err := k.secret(ctx, name)
	return data, err
}

// Connection reads a Secret labelled as one of the project's connections.
func (k *Kube) Connection(ctx context.Context, proj, name string) (map[string]string, error) {
	data, labels, err := k.secret(ctx, name)
	if data == nil || err != nil {
		return nil, err
	}
	if labels[ConnectionLabel] != proj {
		return nil, fmt.Errorf("the Secret %q isn't one of project %s's connections (its label %s=%s says so)",
			name, proj, ConnectionLabel, proj)
	}
	return data, nil
}

func (k *Kube) secret(ctx context.Context, name string) (map[string]string, map[string]string, error) {
	u := strings.TrimSuffix(strings.Replace(k.URL, "/apis/platform.storscale.io/v1alpha1/", "/api/v1/", 1), "/projects") + "/secrets/" + name
	var sec struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Data map[string][]byte `json:"data"` // base64 in JSON: decoded as []byte
	}
	found, err := k.do(ctx, "GET", u, "", nil, &sec)
	if !found || err != nil {
		return nil, nil, err
	}
	out := map[string]string{}
	for key, v := range sec.Data {
		out[key] = string(v)
	}
	return out, sec.Metadata.Labels, nil
}

func (k *Kube) do(ctx context.Context, method, url, contentType string, body, out any) (bool, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return false, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return false, err
	}
	token, err := k.Token()
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := k.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		var st struct{ Message string }
		if json.Unmarshal(msg, &st) == nil && st.Message != "" {
			return false, &APIError{Code: res.StatusCode, Message: st.Message}
		}
		return false, &APIError{Code: res.StatusCode, Message: strings.TrimSpace(string(msg))}
	}
	if out != nil {
		return true, json.NewDecoder(res.Body).Decode(out)
	}
	return true, nil
}

// APIError is the Kubernetes API refusing a request (an invalid spec, say).
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("kubernetes: HTTP %d: %s", e.Code, e.Message) }

func (k *Kube) list(ctx context.Context) ([]resource, error) {
	var l struct{ Items []resource }
	_, err := k.call(ctx, "GET", "", "", nil, &l)
	return l.Items, err
}

func (k *Kube) get(ctx context.Context, name string) (*resource, error) {
	var r resource
	found, err := k.call(ctx, "GET", "/"+name, "", nil, &r)
	if !found || err != nil {
		return nil, err
	}
	return &r, nil
}

func (k *Kube) Projects(ctx context.Context) ([]*project.Project, error) {
	items, err := k.list(ctx)
	if err != nil {
		return nil, err
	}
	var out []*project.Project
	for _, r := range items {
		if r.Metadata.DeletionTimestamp != "" {
			continue
		}
		p, err := r.project()
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", r.Metadata.Name, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (k *Kube) Project(ctx context.Context, name string) (*project.Project, error) {
	r, err := k.get(ctx, name)
	if r == nil || err != nil || r.Metadata.DeletionTimestamp != "" {
		return nil, err
	}
	return r.project()
}

func (k *Kube) PutProject(ctx context.Context, p *project.Project) error {
	spec, err := json.Marshal(p.Spec)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		r, err := k.get(ctx, p.Metadata.Name)
		if err != nil {
			return err
		}
		if r == nil {
			r = &resource{APIVersion: "platform.storscale.io/v1alpha1", Kind: "Project"}
			r.Metadata.Name = p.Metadata.Name
			r.Metadata.Finalizers = []string{Finalizer}
			r.Spec = spec
			_, err = k.call(ctx, "POST", "", "application/json", r, nil)
		} else {
			if r.Metadata.DeletionTimestamp != "" {
				return fmt.Errorf("project %s is being deleted; try again once it's gone", p.Metadata.Name)
			}
			if !slices.Contains(r.Metadata.Finalizers, Finalizer) {
				r.Metadata.Finalizers = append(r.Metadata.Finalizers, Finalizer)
			}
			r.Spec, r.Status = spec, nil
			_, err = k.call(ctx, "PUT", "/"+p.Metadata.Name, "application/json", r, nil)
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict && attempt < 3 {
			continue // changed since we read it: read it again
		}
		return err
	}
}

func (k *Kube) DeleteProject(ctx context.Context, name string) error {
	_, err := k.call(ctx, "DELETE", "/"+name, "", nil, nil)
	return err
}

func (k *Kube) Statuses(ctx context.Context) ([]*project.Status, error) {
	items, err := k.list(ctx)
	if err != nil {
		return nil, err
	}
	var out []*project.Status
	for _, r := range items {
		st, err := r.status()
		if err != nil {
			return nil, fmt.Errorf("project %s's status: %w", r.Metadata.Name, err)
		}
		if st != nil {
			out = append(out, st)
		}
	}
	return out, nil
}

func (k *Kube) Status(ctx context.Context, name string) (*project.Status, error) {
	r, err := k.get(ctx, name)
	if r == nil || err != nil {
		return nil, err
	}
	return r.status()
}

func (k *Kube) PutStatus(ctx context.Context, st *project.Status) error {
	// The status as a whole, and memberCount for kubectl's Members column.
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	var status map[string]any
	json.Unmarshal(body, &status)
	status["memberCount"] = len(st.Members)
	if _, err = k.call(ctx, "PATCH", "/"+st.Name+"/status", "application/merge-patch+json", map[string]any{"status": status}, nil); err != nil {
		return err
	}
	return k.keep(ctx, st.Name)
}

// keep gives a project the operator's finalizer if it hasn't one: one made
// with kubectl rather than through the platform, say. Without it, deleting
// the project removes it at once, before the operator takes its access out
// of Keycloak, Ranger and Buckets.
func (k *Kube) keep(ctx context.Context, name string) error {
	r, err := k.get(ctx, name)
	if r == nil || err != nil || r.Metadata.DeletionTimestamp != "" || slices.Contains(r.Metadata.Finalizers, Finalizer) {
		return err
	}
	_, err = k.call(ctx, "PATCH", "/"+name, "application/merge-patch+json", map[string]any{"metadata": map[string]any{
		"finalizers": append(slices.Clone(r.Metadata.Finalizers), Finalizer), "resourceVersion": r.Metadata.ResourceVersion}}, nil)
	return err
}

func (k *Kube) DeleteStatus(ctx context.Context, name string) error {
	r, err := k.get(ctx, name)
	if r == nil || err != nil {
		return err
	}
	if !slices.Contains(r.Metadata.Finalizers, Finalizer) {
		return nil
	}
	rest := slices.DeleteFunc(slices.Clone(r.Metadata.Finalizers), func(f string) bool { return f == Finalizer })
	_, err = k.call(ctx, "PATCH", "/"+name, "application/merge-patch+json",
		map[string]any{"metadata": map[string]any{"finalizers": rest, "resourceVersion": r.Metadata.ResourceVersion}}, nil)
	return err
}
