package reconcile

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Kube is the little of Kubernetes' API the operator uses for projects'
// Airflows: Secrets, Services and Deployments in its own namespace, as its
// service account.
type Kube struct {
	URL       string // https://host:port
	Namespace string
	Token     func() (string, error)
	HTTP      *http.Client
}

// NewKube is the API of the cluster the pod runs in.
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
		URL: "https://" + host + ":" + port, Namespace: strings.TrimSpace(string(ns)),
		// Read each time: the kubelet rotates the token.
		Token: func() (string, error) { t, err := os.ReadFile(sa + "/token"); return strings.TrimSpace(string(t)), err },
		HTTP:  &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
	}, nil
}

// path is a resource's address: kind is secrets, services or deployments.
func (k *Kube) path(kind, name string) string {
	api := "/api/v1"
	if kind == "deployments" {
		api = "/apis/apps/v1"
	}
	p := api + "/namespaces/" + k.Namespace + "/" + kind
	if name != "" {
		p += "/" + name
	}
	return p
}

// do sends a request; found is false on 404.
func (k *Kube) do(ctx context.Context, method, path, contentType string, body, out any) (found bool, err error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return false, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.URL+path, rd)
	if err != nil {
		return false, err
	}
	tok, err := k.Token()
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := k.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<22))
	if res.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if res.StatusCode/100 != 2 {
		// Never the body of a Secret: Kubernetes' errors name the object, not its data.
		var st struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &st)
		return true, fmt.Errorf("%s %s: HTTP %d %s", method, path, res.StatusCode, st.Message)
	}
	if out != nil {
		return true, json.Unmarshal(data, out)
	}
	return true, nil
}

// EnsureSecret is a Secret's data, made from fill when there's no such
// Secret: what's made once (keys, passwords) is kept.
func (k *Kube) EnsureSecret(ctx context.Context, name string, labels map[string]string, fill func() map[string]string) (map[string]string, error) {
	var sec struct {
		Data map[string][]byte `json:"data"`
	}
	found, err := k.do(ctx, "GET", k.path("secrets", name), "", nil, &sec)
	if err != nil {
		return nil, err
	}
	if found {
		out := map[string]string{}
		for key, v := range sec.Data {
			out[key] = string(v)
		}
		return out, nil
	}
	data := fill()
	enc := map[string]string{}
	for key, v := range data {
		enc[key] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	body := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{"name": name, "labels": labels}, "data": enc}
	if _, err := k.do(ctx, "POST", k.path("secrets", ""), "application/json", body, nil); err != nil {
		return nil, err
	}
	return data, nil
}

// Apply makes an object (a Service or a Deployment) what obj says, by
// server-side apply: fields the operator doesn't set are left alone.
func (k *Kube) Apply(ctx context.Context, kind string, obj map[string]any) error {
	md, _ := obj["metadata"].(map[string]any)
	name, _ := md["name"].(string)
	if name == "" {
		return fmt.Errorf("a %s without a name", kind)
	}
	_, err := k.do(ctx, "PATCH", k.path(kind, name)+"?fieldManager=platform-operator&force=true",
		"application/apply-patch+yaml", obj, nil) // JSON is YAML
	return err
}

// Names are the objects of a kind whose labels match the selector (as app=x,y=z).
func (k *Kube) Names(ctx context.Context, kind, selector string) ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if _, err := k.do(ctx, "GET", k.path(kind, "")+"?labelSelector="+url.QueryEscape(selector), "", nil, &list); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, it.Metadata.Name)
	}
	return out, nil
}

// Delete removes an object; one that's already gone is no error.
func (k *Kube) Delete(ctx context.Context, kind, name string) error {
	_, err := k.do(ctx, "DELETE", k.path(kind, name), "", nil, nil)
	return err
}

// PodAnnotation is an annotation of a Deployment's pods (its template): ""
// when there's no such Deployment or annotation.
func (k *Kube) PodAnnotation(ctx context.Context, name, key string) (string, error) {
	var d struct {
		Spec struct {
			Template struct {
				Metadata struct {
					Annotations map[string]string `json:"annotations"`
				} `json:"metadata"`
			} `json:"template"`
		} `json:"spec"`
	}
	found, err := k.do(ctx, "GET", k.path("deployments", name), "", nil, &d)
	if !found || err != nil {
		return "", err
	}
	return d.Spec.Template.Metadata.Annotations[key], nil
}

// Ready says whether a Deployment's newest pods are all ready.
func (k *Kube) Ready(ctx context.Context, name string) (bool, error) {
	var d struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Replicas *int32 `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ObservedGeneration int64 `json:"observedGeneration"`
			UpdatedReplicas    int32 `json:"updatedReplicas"`
			ReadyReplicas      int32 `json:"readyReplicas"`
		} `json:"status"`
	}
	found, err := k.do(ctx, "GET", k.path("deployments", name), "", nil, &d)
	if !found || err != nil {
		return false, err
	}
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	return d.Status.ObservedGeneration >= d.Metadata.Generation && d.Status.UpdatedReplicas >= want && d.Status.ReadyReplicas >= want, nil
}
