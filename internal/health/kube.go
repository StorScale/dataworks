package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Kube finds a service's pods, and their latest warnings, through
// Kubernetes' API, as the pod's service account (pods and events: list).
type Kube struct {
	URL       string // https://host:port
	Namespace string
	Token     func() (string, error)
	HTTP      *http.Client
	Checks    map[string]Check // by name: their selectors
}

// NewKube is the API of the cluster the pod runs in.
func NewKube(checks []Check) (*Kube, error) {
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount"
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	ns, err := os.ReadFile(sa + "/namespace")
	if host == "" || err != nil {
		return nil, errors.New("not in a Kubernetes pod")
	}
	ca, err := os.ReadFile(sa + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	k := &Kube{URL: "https://" + host + ":" + port, Namespace: strings.TrimSpace(string(ns)),
		Token:  func() (string, error) { t, err := os.ReadFile(sa + "/token"); return strings.TrimSpace(string(t)), err },
		HTTP:   &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
		Checks: map[string]Check{}}
	for _, c := range checks {
		k.Checks[c.Name] = c
	}
	return k, nil
}

func (k *Kube) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", k.URL+"/api/v1/namespaces/"+k.Namespace+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	tok, err := k.Token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := k.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("kubernetes: GET %s: HTTP %d", path, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Pods are a service's pods, each with its waiting reason and latest warning.
func (k *Kube) Pods(ctx context.Context, service string) ([]Pod, error) {
	sel := "app.kubernetes.io/name=" + service
	if c, ok := k.Checks[service]; ok && c.Selector != "" {
		sel = c.Selector
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				Phase                                    string `json:"phase"`
				InitContainerStatuses, ContainerStatuses []struct {
					State struct {
						Waiting *struct {
							Reason  string `json:"reason"`
							Message string `json:"message"`
						} `json:"waiting"`
					} `json:"state"`
				}
			} `json:"status"`
		} `json:"items"`
	}
	if err := k.get(ctx, "/pods", url.Values{"labelSelector": {sel}}, &list); err != nil {
		return nil, err
	}
	var out []Pod
	for _, it := range list.Items {
		p := Pod{Name: it.Metadata.Name, Node: it.Spec.NodeName, Phase: it.Status.Phase}
		for _, cs := range append(it.Status.InitContainerStatuses, it.Status.ContainerStatuses...) {
			if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
				p.Waiting = w.Reason
				if w.Message != "" {
					p.Waiting += ": " + firstLine(w.Message)
				}
				break
			}
		}
		p.Event = k.warning(ctx, it.Metadata.Name)
		out = append(out, p)
	}
	return out, nil
}

// warning is a pod's latest warning event, as "Reason: message".
func (k *Kube) warning(ctx context.Context, pod string) string {
	var list struct {
		Items []struct {
			Reason        string    `json:"reason"`
			Message       string    `json:"message"`
			LastTimestamp time.Time `json:"lastTimestamp"`
			EventTime     time.Time `json:"eventTime"`
		} `json:"items"`
	}
	q := url.Values{"fieldSelector": {"involvedObject.name=" + pod + ",type=Warning"}}
	if k.get(ctx, "/events", q, &list) != nil || len(list.Items) == 0 {
		return ""
	}
	at := func(i int) time.Time {
		if t := list.Items[i].LastTimestamp; !t.IsZero() {
			return t
		}
		return list.Items[i].EventTime
	}
	sort.Slice(list.Items, func(i, j int) bool { return at(i).After(at(j)) })
	e := list.Items[0]
	return e.Reason + ": " + firstLine(e.Message)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
