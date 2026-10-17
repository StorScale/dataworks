package health

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAServiceIsDownAfterTwoFailedChecksAndBackAfterOneGood(t *testing.T) {
	var mu sync.Mutex
	status := 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
	}))
	defer ts.Close()
	var changes []Change
	c := &Checker{Checks: []Check{{Name: "trino", Label: "Query engine", URL: ts.URL, Features: []string{"SQL"}}},
		Notify: func(ch Change) { changes = append(changes, ch) }}
	ctx := context.Background()
	c.Once(ctx)
	if st := c.Statuses()[0]; st.State != Up || len(changes) != 0 {
		t.Fatalf("first: %+v %v", st, changes)
	}
	set := func(s int) { mu.Lock(); status = s; mu.Unlock() }
	set(503)
	c.Once(ctx)
	if st := c.Statuses()[0]; st.State != Up || len(changes) != 0 || st.Reason != "its health check answers HTTP 503" {
		t.Errorf("one failure: %+v %v, want still up (a blip), with the reason kept", st, changes)
	}
	c.Once(ctx)
	if st := c.Statuses()[0]; st.State != Down || len(changes) != 1 || changes[0].Was != Up {
		t.Fatalf("two failures: %+v %v, want down, and one change", st, changes)
	}
	c.Once(ctx)
	if len(changes) != 1 {
		t.Errorf("still down: %d changes, want no more", len(changes))
	}
	set(200)
	c.Once(ctx)
	if st := c.Statuses()[0]; st.State != Up || len(changes) != 2 || changes[1].Was != Down || st.Reason != "" {
		t.Errorf("back: %+v %v", st, changes)
	}
	if m := c.Metrics(); !strings.Contains(m, `platform_service_up{service="trino"} 1`) {
		t.Errorf("metrics:\n%s", m)
	}
}

func TestNothingIsSentWhileThePlatformStarts(t *testing.T) {
	var sent int
	c := &Checker{Checks: []Check{{Name: "x", URL: "tcp://127.0.0.1:1"}}, Confirm: 1, Quiet: time.Hour,
		Notify: func(Change) { sent++ }}
	c.Once(context.Background())
	if st := c.Statuses()[0]; st.State != Down || sent != 0 || !strings.Contains(st.Reason, "isn't accepting connections") {
		t.Errorf("%+v, %d sent: want it down, said plainly, and nothing sent while quiet", st, sent)
	}
}

func TestATCPCheck(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	c := &Checker{Checks: []Check{{Name: "db", URL: "tcp://" + l.Addr().String()}}}
	c.Once(context.Background())
	if st := c.Statuses()[0]; st.State != Up {
		t.Errorf("%+v", st)
	}
}

func TestAirflowsPartsAreChecksOfTheirOwn(t *testing.T) {
	body := `{"metadatabase": {"status": "healthy"}, "scheduler": {"status": "unhealthy"}, "dag_processor": {"status": "healthy"}}`
	if ok, _ := airflowOK("metadatabase")(200, body); !ok {
		t.Error("its database is healthy")
	}
	if ok, why := airflowOK("scheduler")(200, body); ok || why != "Airflow says it's unhealthy" {
		t.Errorf("its scheduler: %v %q", ok, why)
	}
	if ok, _ := answersSignIn(401, ""); !ok {
		t.Error("Ranger asking for a sign-in is up")
	}
	if ok, why := trinoOK(200, `{"starting":true}`); ok || why != "it's starting" {
		t.Errorf("trino starting: %v %q", ok, why)
	}
}

func TestAPodsReasonComesFromKubernetes(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/namespaces/storscale/pods":
			if r.URL.Query().Get("labelSelector") != "app.kubernetes.io/name=openmetadata-search" {
				t.Errorf("selector %q", r.URL.Query().Get("labelSelector"))
			}
			io.WriteString(w, `{"items": [{"metadata": {"name": "openmetadata-search-0"}, "spec": {"nodeName": "da1x-p2"},
				"status": {"phase": "Pending", "containerStatuses": [{"state": {"waiting": {"reason": "ContainerCreating"}}}]}}]}`)
		case "/api/v1/namespaces/storscale/events":
			if !strings.Contains(r.URL.Query().Get("fieldSelector"), "involvedObject.name=openmetadata-search-0,type=Warning") {
				t.Errorf("events of %q", r.URL.Query().Get("fieldSelector"))
			}
			io.WriteString(w, `{"items": [
				{"reason": "FailedAttachVolume", "message": "Multi-Attach error", "lastTimestamp": "2026-10-16T18:47:00Z"},
				{"reason": "FailedMount", "message": "MountVolume.MountDevice failed: can't read superblock", "lastTimestamp": "2026-10-16T18:57:00Z"}]}`)
		}
	}))
	defer api.Close()
	k := &Kube{URL: api.URL, Namespace: "storscale", HTTP: api.Client(), Token: func() (string, error) { return "t", nil }, Checks: map[string]Check{}}
	c := &Checker{Checks: []Check{{Name: "openmetadata-search", Label: "Catalog search", URL: "tcp://127.0.0.1:1"}}, Confirm: 1, Pods: k}
	c.Once(context.Background())
	st := c.Statuses()[0]
	if want := "pod openmetadata-search-0 on da1x-p2: FailedMount: MountVolume.MountDevice failed: can't read superblock"; st.Reason != want {
		t.Errorf("reason %q, want the latest warning", st.Reason)
	}
}

func TestAnAlertReachesTeamsAsACard(t *testing.T) {
	var got map[string]any
	teams := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(202)
	}))
	defer teams.Close()
	n := &Notifier{Targets: func(context.Context) (Targets, error) {
		return TargetsFrom(map[string]string{"teamsWebhook": teams.URL + "/workflows/abc"}), nil
	}}
	a := FromChange(Change{Status: Status{Check: Check{Label: "Catalog search", Features: []string{"search"}}, State: Down,
		Reason: "its volume won't mount"}, Was: Up}, "https://platform.example.com/admin/health")
	if errs := n.Send(context.Background(), a); errs["teams"] != nil {
		t.Fatal(errs)
	}
	b, _ := json.Marshal(got)
	for _, want := range []string{`"type":"message"`, `"contentType":"application/vnd.microsoft.card.adaptive"`,
		`"text":"Catalog search is down"`, `"value":"its volume won't mount"`, `"url":"https://platform.example.com/admin/health"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %s in %s", want, b)
		}
	}
	if len(n.Recent()) != 1 {
		t.Error("the alert is among the recent ones")
	}
	n.Mute("trino", time.Now().Add(time.Hour))
	if n.Muted("trino").IsZero() || !n.Muted("keycloak").IsZero() {
		t.Error("muted: trino for an hour, not keycloak")
	}
}

func TestAWebhooksAddressIsNeverInAnError(t *testing.T) {
	n := &Notifier{Targets: func(context.Context) (Targets, error) {
		return Targets{TeamsWebhook: "http://no-such-host.invalid/workflows/secret-signature"}, nil
	}}
	err := n.Send(context.Background(), Alert{Title: "x"})["teams"]
	if err == nil || strings.Contains(err.Error(), "secret-signature") {
		t.Errorf("%v: want an error without the address", err)
	}
}

func TestMonitoringRulesFiringAreFindingsThatStartAndEnd(t *testing.T) {
	firing := `{"data": {"alerts": [
		{"labels": {"alertname": "BucketsCapacityLow", "severity": "warning", "buckets_cluster": "store"},
		 "annotations": {"summary": "local/store: 12% of usable capacity left", "runbook_url": "https://x/runbook"},
		 "state": "firing", "activeAt": "2026-10-16T20:00:00Z"},
		{"labels": {"alertname": "BucketsDriveOffline"}, "state": "pending", "activeAt": "2026-10-16T20:10:00Z"}]}}`
	body := firing
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/alerts" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, body)
	}))
	defer prom.Close()
	var f Findings
	got, err := PrometheusAlerts(context.Background(), prom.Client(), prom.URL)
	if err != nil || len(got) != 1 || got[0].Title != "BucketsCapacityLow: local/store: 12% of usable capacity left" {
		t.Fatalf("firing: %+v %v (a pending rule isn't yet)", got, err)
	}
	on, off := f.Set("prometheus", got)
	if len(on) != 1 || len(off) != 0 {
		t.Errorf("first: %d started, %d ended", len(on), len(off))
	}
	on, _ = f.Set("prometheus", got)
	if len(on) != 0 {
		t.Error("still firing: nothing new")
	}
	body = `{"data": {"alerts": []}}`
	got, _ = PrometheusAlerts(context.Background(), prom.Client(), prom.URL)
	_, off = f.Set("prometheus", got)
	if len(off) != 1 || len(f.All()) != 0 {
		t.Errorf("resolved: %d ended, %d left", len(off), len(f.All()))
	}
	a := FromFinding(off[0], true, "http://x/admin/health")
	if a.Title != "Resolved: BucketsCapacityLow: local/store: 12% of usable capacity left" || a.Severity != "up" {
		t.Errorf("the resolved alert: %+v", a)
	}
}

func TestASetupStepThatGaveUpIsAFinding(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/batch/v1/namespaces/storscale/jobs" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, `{"items": [
			{"metadata": {"name": "setup-13"}, "status": {"conditions": [{"type": "Complete", "status": "True"}]}},
			{"metadata": {"name": "setup-trino-13"}, "status": {"conditions": [{"type": "Failed", "status": "True",
			 "reason": "BackoffLimitExceeded", "message": "Job has reached the specified backoff limit", "lastTransitionTime": "2026-10-16T20:00:00Z"}]}},
			{"metadata": {"name": "nessie-gc-29870364", "ownerReferences": [{"kind": "CronJob", "name": "nessie-gc"}]}, "status": {"conditions": [{"type": "Failed", "status": "True",
			 "reason": "BackoffLimitExceeded", "message": "Job has reached the specified backoff limit", "lastTransitionTime": "2026-10-17T03:30:00Z"}]}}]}`)
	}))
	defer api.Close()
	k := &Kube{URL: api.URL, Namespace: "storscale", HTTP: api.Client(), Token: func() (string, error) { return "t", nil }}
	got, err := k.FailedJobs(context.Background())
	if err != nil || len(got) != 2 || got[0].Title != "Setup step setup-trino-13 failed" ||
		got[0].Detail != "BackoffLimitExceeded: Job has reached the specified backoff limit" {
		t.Fatalf("%+v %v", got, err)
	}
	if got[1].Title != "Scheduled job nessie-gc failed (run nessie-gc-29870364)" {
		t.Errorf("a scheduled job's run: %q", got[1].Title)
	}
}

func TestBackupsAreReadFromTheirCronJobAndOverdueAfterADay(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/batch/v1/namespaces/storscale/cronjobs/storscale-backup":
			io.WriteString(w, `{"metadata": {"creationTimestamp": "2026-10-01T00:00:00Z"}, "spec": {"schedule": "0 4 * * *"},
				"status": {"lastSuccessfulTime": "2026-10-16T04:05:00Z"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	k := &Kube{URL: api.URL, Namespace: "storscale", HTTP: api.Client(), Token: func() (string, error) { return "t", nil }}
	b, err := k.Backups(context.Background())
	if err != nil || !b.Configured || b.Schedule != "0 4 * * *" || !b.LastSuccess.Equal(time.Date(2026, 10, 16, 4, 5, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", b, err)
	}
	if f := b.Overdue(time.Date(2026, 10, 17, 4, 30, 0, 0, time.UTC), 26*time.Hour); len(f) != 0 {
		t.Errorf("a day after: %+v", f)
	}
	if f := b.Overdue(time.Date(2026, 10, 17, 7, 0, 0, 0, time.UTC), 26*time.Hour); len(f) != 1 || f[0].Severity != "critical" ||
		!strings.Contains(f[0].Detail, "2026-10-16 04:05") {
		t.Errorf("27 hours after: %+v", f)
	}
	never := Backups{Configured: true, Since: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
	if f := never.Overdue(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), 26*time.Hour); len(f) != 1 || !strings.Contains(f[0].Detail, "none has succeeded") {
		t.Errorf("none ever: %+v", f)
	}
	k.Namespace = "elsewhere"
	if b, err := k.Backups(context.Background()); err != nil || b.Configured || b.Overdue(time.Now(), time.Hour) != nil {
		t.Errorf("not set up: %+v %v", b, err)
	}
}
