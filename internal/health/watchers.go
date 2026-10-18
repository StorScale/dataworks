package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// A Finding is something wrong that isn't a service down: a monitoring rule
// firing (storage near full, a drive offline), or a setup step that gave up.
type Finding struct {
	Key      string    `json:"key"` // what it is, to tell it from others
	Title    string    `json:"title"`
	Detail   string    `json:"detail,omitempty"`
	Severity string    `json:"severity"` // warning, critical
	Since    time.Time `json:"since"`
}

// Findings keeps the current ones, and tells what started and what ended.
type Findings struct {
	mu  sync.Mutex
	cur map[string]Finding
}

// Set replaces the findings of one source (prometheus, jobs); the ones new
// since, and the ones gone.
func (f *Findings) Set(source string, now []Finding) (started, ended []Finding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cur == nil {
		f.cur = map[string]Finding{}
	}
	seen := map[string]bool{}
	for _, x := range now {
		k := source + "/" + x.Key
		seen[k] = true
		if old, ok := f.cur[k]; ok {
			x.Since = old.Since
		} else {
			started = append(started, x)
		}
		f.cur[k] = x
	}
	for k, x := range f.cur {
		if strings.HasPrefix(k, source+"/") && !seen[k] {
			ended = append(ended, x)
			delete(f.cur, k)
		}
	}
	return started, ended
}

// All are the current findings, oldest first.
func (f *Findings) All() []Finding {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Finding, 0, len(f.cur))
	for _, x := range f.cur {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// PrometheusAlerts are the monitoring rules firing now (Prometheus has
// them; nothing else delivers them): storage near full, drives offline...
func PrometheusAlerts(ctx context.Context, c *http.Client, base string) ([]Finding, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/api/v1/alerts", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus: %s", plain(err))
	}
	defer res.Body.Close()
	var body struct {
		Data struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
				ActiveAt    time.Time         `json:"activeAt"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus: %v", err)
	}
	var out []Finding
	for _, a := range body.Data.Alerts {
		if a.State != "firing" {
			continue // pending: not yet for its rule's while
		}
		var keys []string
		for k, v := range a.Labels {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		title := a.Annotations["summary"]
		if title == "" {
			title = a.Labels["alertname"]
		}
		out = append(out, Finding{Key: strings.Join(keys, ","), Title: a.Labels["alertname"] + ": " + title,
			Detail: a.Annotations["runbook_url"], Severity: a.Labels["severity"], Since: a.ActiveAt})
	}
	return out, nil
}

// FailedJobs are the setup steps (Kubernetes Jobs) that gave up, and the
// scheduled jobs' runs (Nessie's GC) that failed.
func (k *Kube) FailedJobs(ctx context.Context) ([]Finding, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", k.URL+"/apis/batch/v1/namespaces/"+k.Namespace+"/jobs?"+url.Values{}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	tok, err := k.Token()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := k.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("kubernetes: jobs: HTTP %d", res.StatusCode)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name            string `json:"name"`
				OwnerReferences []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type               string    `json:"type"`
					Status             string    `json:"status"`
					Reason             string    `json:"reason"`
					Message            string    `json:"message"`
					LastTransitionTime time.Time `json:"lastTransitionTime"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return nil, err
	}
	var out []Finding
	for _, j := range list.Items {
		for _, c := range j.Status.Conditions {
			if c.Type == "Failed" && c.Status == "True" {
				title := "Setup step " + j.Metadata.Name + " failed"
				if o := j.Metadata.OwnerReferences; len(o) > 0 && o[0].Kind == "CronJob" {
					title = "Scheduled job " + o[0].Name + " failed (run " + j.Metadata.Name + ")"
				}
				out = append(out, Finding{Key: j.Metadata.Name, Title: title,
					Detail: strings.TrimSpace(c.Reason + ": " + firstLine(c.Message)), Severity: "critical", Since: c.LastTransitionTime})
			}
		}
	}
	return out, nil
}

// StoppedCopies are the projects' sources' copies (use case 8.1) whose pod
// isn't running: crashing, its image not pulled, its database refusing it.
// Their projects are told too (project alerts); the administrators are,
// here, as it may be the platform's doing.
func (k *Kube) StoppedCopies(ctx context.Context) ([]Finding, error) {
	pods, err := k.Pods(ctx, "storscale-copy")
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, p := range pods {
		if p.Waiting == "" || strings.HasPrefix(p.Waiting, "ContainerCreating") {
			continue
		}
		copy := p.Name
		if parts := strings.Split(p.Name, "-"); len(parts) > 2 { // copy-<project>-<source>-<replicaset>-<pod>
			copy = strings.Join(parts[:len(parts)-2], "-")
		}
		out = append(out, Finding{Key: "copy/" + copy, Title: "The copy " + copy + " isn't running",
			Detail: p.Waiting, Severity: "warning", Since: time.Now()})
	}
	return out, nil
}

// Backups are the platform's backups, as their CronJob (storscale-backup)
// has them: none when the chart has no backup certificate.
type Backups struct {
	Configured  bool      `json:"configured"`
	Schedule    string    `json:"schedule,omitempty"`
	LastSuccess time.Time `json:"lastSuccess"`
	Since       time.Time `json:"since"` // when they were set up
}

// Backups reads the backups' CronJob.
func (k *Kube) Backups(ctx context.Context) (Backups, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", k.URL+"/apis/batch/v1/namespaces/"+k.Namespace+"/cronjobs/storscale-backup", nil)
	if err != nil {
		return Backups{}, err
	}
	tok, err := k.Token()
	if err != nil {
		return Backups{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := k.HTTP.Do(req)
	if err != nil {
		return Backups{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return Backups{}, nil
	}
	if res.StatusCode != 200 {
		return Backups{}, fmt.Errorf("kubernetes: the backups' CronJob: HTTP %d", res.StatusCode)
	}
	var cj struct {
		Metadata struct {
			Created time.Time `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Schedule string `json:"schedule"`
		} `json:"spec"`
		Status struct {
			LastSuccess time.Time `json:"lastSuccessfulTime"`
		} `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&cj); err != nil {
		return Backups{}, err
	}
	return Backups{Configured: true, Schedule: cj.Spec.Schedule, LastSuccess: cj.Status.LastSuccess, Since: cj.Metadata.Created}, nil
}

// Overdue is the finding when backups are set up and none has succeeded
// within `within` (a nightly one's day, and some).
func (b Backups) Overdue(now time.Time, within time.Duration) []Finding {
	if !b.Configured {
		return nil
	}
	switch {
	case !b.LastSuccess.IsZero() && now.Sub(b.LastSuccess) > within:
		return []Finding{{Key: "backups", Title: "Backups are overdue", Severity: "critical", Since: b.LastSuccess.Add(within),
			Detail: "the last one that succeeded was " + b.LastSuccess.UTC().Format("2006-01-02 15:04 MST")}}
	case b.LastSuccess.IsZero() && now.Sub(b.Since) > within:
		return []Finding{{Key: "backups", Title: "Backups are overdue", Severity: "critical", Since: b.Since.Add(within),
			Detail: "none has succeeded since they were set up, " + b.Since.UTC().Format("2006-01-02 15:04 MST")}}
	}
	return nil
}

// FromFinding is the alert for a finding that started, or ended.
func FromFinding(f Finding, ended bool, link string) Alert {
	a := Alert{Title: f.Title, Severity: "warning", Link: link, At: time.Now().UTC()}
	if f.Severity == "critical" {
		a.Severity = "down"
	}
	if ended {
		a.Title, a.Severity = "Resolved: "+f.Title, "up"
	}
	if f.Detail != "" {
		a.Facts = append(a.Facts, [2]string{"Detail", f.Detail})
	}
	a.Facts = append(a.Facts, [2]string{"Since", f.Since.UTC().Format("2006-01-02 15:04 MST")})
	return a
}
