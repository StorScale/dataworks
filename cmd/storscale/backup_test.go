package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeygenMakesAPairAndKeyMatchesKnowsIt(t *testing.T) {
	dir := t.TempDir()
	if err := keygen(dir); err != nil {
		t.Fatal(err)
	}
	if err := keygen(dir); err == nil || !strings.Contains(err.Error(), "never overwrites") {
		t.Errorf("a second keygen: %v, want refused", err)
	}
	certPEM, _ := os.ReadFile(filepath.Join(dir, "storscale-backup-cert.pem"))
	keyPEM, _ := os.ReadFile(filepath.Join(dir, "storscale-backup-key.pem"))
	b, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := keyMatches(keyPEM, cert); err != nil {
		t.Errorf("its own key: %v", err)
	}
	other := t.TempDir()
	keygen(other)
	otherKey, _ := os.ReadFile(filepath.Join(other, "storscale-backup-key.pem"))
	if err := keyMatches(otherKey, cert); err == nil {
		t.Error("another pair's key matches")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "storscale-backup-key.pem")); fi.Mode().Perm() != 0o600 {
		t.Errorf("the key's mode: %v", fi.Mode().Perm())
	}
}

// The restore Job is built from the chart's own CronJob (testdata: as the
// chart renders it): its images, settings and volumes, other steps.
func TestTheRestoreJobIsTheBackupsPodTheOtherWay(t *testing.T) {
	cron, err := os.ReadFile("testdata/backup-cronjob.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := restoreJob("storscale-restore-x", "20261017T040000Z", cron, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		Spec struct {
			BackoffLimit int `json:"backoffLimit"`
			Template     struct {
				Spec struct {
					ServiceAccountName string `json:"serviceAccountName"`
					RestartPolicy      string `json:"restartPolicy"`
					InitContainers     []struct {
						Name, Image  string
						Command      []string
						VolumeMounts []struct{ Name, MountPath string } `json:"volumeMounts"`
					} `json:"initContainers"`
					Containers []struct {
						Name    string
						Command []string
					} `json:"containers"`
					Volumes []struct {
						Name   string
						Secret *struct{ SecretName string } `json:"secret"`
					} `json:"volumes"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	var names []string
	for _, c := range pod.InitContainers {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "fetch,databases" || len(pod.Containers) != 1 || pod.Containers[0].Name != "apply" {
		t.Fatalf("steps: %v then %+v", names, pod.Containers)
	}
	if got := strings.Join(pod.InitContainers[0].Command, " "); got != "python -u /tools/backup.py fetch 20261017T040000Z" {
		t.Errorf("fetch: %s", got)
	}
	if !strings.Contains(strings.Join(pod.InitContainers[1].Command, " "), "pg_restore -h postgres -U postgres --create") ||
		!strings.Contains(pod.InitContainers[1].Image, "postgres") {
		t.Errorf("databases: %s %v", pod.InitContainers[1].Image, pod.InitContainers[1].Command)
	}
	mounted := map[string]bool{}
	for _, m := range pod.InitContainers[0].VolumeMounts {
		mounted[m.MountPath] = true
	}
	for _, want := range []string{"/backup", "/tools", "/backup-cert", "/restore-key"} {
		if !mounted[want] {
			t.Errorf("fetch hasn't %s: %+v", want, pod.InitContainers[0].VolumeMounts)
		}
	}
	key := false
	for _, v := range pod.Volumes {
		key = key || (v.Name == "restore-key" && v.Secret != nil && v.Secret.SecretName == restoreKey)
	}
	if !key || pod.ServiceAccountName != "storscale" || pod.RestartPolicy != "Never" || job.Spec.BackoffLimit != 0 {
		t.Errorf("job: %+v", job.Spec)
	}
}

// With the data copied back, and into another install: the data first,
// then the databases, then each role its password of then; INTO_NEW for the
// steps that read and apply the backup's storscale-env.
func TestTheRestoreJobWithDataIntoAnotherInstall(t *testing.T) {
	cron, _ := os.ReadFile("testdata/backup-cronjob.json")
	dataCron, err := os.ReadFile("testdata/backup-data-cronjob.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := restoreJob("r", "latest", cron, dataCron, true)
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		Spec struct {
			Template struct {
				Spec struct {
					InitContainers []struct {
						Name    string
						Command []string
						Env     []struct{ Name, Value string }
					} `json:"initContainers"`
					Containers []struct {
						Name string
						Env  []struct{ Name, Value string }
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	json.Unmarshal(out, &job)
	pod := job.Spec.Template.Spec
	var names []string
	intoNew := map[string]bool{}
	for _, c := range pod.InitContainers {
		names = append(names, c.Name)
		for _, e := range c.Env {
			intoNew[c.Name] = intoNew[c.Name] || (e.Name == "INTO_NEW" && e.Value == "1")
		}
	}
	for _, e := range pod.Containers[0].Env {
		intoNew["apply"] = intoNew["apply"] || (e.Name == "INTO_NEW" && e.Value == "1")
	}
	if strings.Join(names, ",") != "fetch,data,databases,roles" {
		t.Fatalf("steps: %v", names)
	}
	if !intoNew["fetch"] || !intoNew["apply"] || intoNew["databases"] {
		t.Errorf("INTO_NEW: %v", intoNew)
	}
	data := strings.Join(pod.InitContainers[1].Command, " ")
	if !strings.Contains(data, `mc mirror --overwrite --quiet "dst/$BACKUP_BUCKET/data/$b" "src/$b"`) {
		t.Errorf("data, copied back: %s", data)
	}
	var rootCreds bool
	for _, e := range pod.InitContainers[1].Env {
		rootCreds = rootCreds || e.Name == "BUCKETS_ROOT_PASSWORD"
	}
	if !rootCreds || !strings.Contains(strings.Join(pod.InitContainers[3].Command, " "), "roles.sql") {
		t.Errorf("data step's credentials, or the roles step: %+v", pod.InitContainers)
	}
}

func TestTheSetupStepsAreRunAgainAsCopies(t *testing.T) {
	var jobs []map[string]any
	json.Unmarshal([]byte(`[
		{"metadata": {"name": "setup-14"}}, {"metadata": {"name": "setup-trino-16"}},
		{"metadata": {"name": "setup-16", "labels": {"app": "x", "batch.kubernetes.io/controller-uid": "u"}},
		 "spec": {"backoffLimit": 6, "selector": {"matchLabels": {"batch.kubernetes.io/controller-uid": "u"}},
		  "template": {"metadata": {"labels": {"job-name": "setup-16", "controller-uid": "u", "app": "x"}}, "spec": {"containers": [{"name": "setup"}]}}}},
		{"metadata": {"name": "setup-again-x"}}]`), &jobs)
	j := latestRevision(jobs, "setup")
	if md := j["metadata"].(map[string]any); md["name"] != "setup-16" {
		t.Fatalf("latest: %v", md["name"])
	}
	if s := latestRevision(jobs, "setup-trino"); s["metadata"].(map[string]any)["name"] != "setup-trino-16" {
		t.Errorf("setup-trino: %v", s)
	}
	out, err := jobCopy(j, "setup-again-1")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, gone := range []string{"controller-uid", "job-name", "selector"} {
		if strings.Contains(got, gone) {
			t.Errorf("the copy keeps %s: %s", gone, got)
		}
	}
	if !strings.Contains(got, `"name":"setup-again-1"`) || !strings.Contains(got, `"backoffLimit":6`) || !strings.Contains(got, `"app":"x"`) {
		t.Errorf("copy: %s", got)
	}
}
