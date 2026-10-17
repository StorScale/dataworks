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
	data, err := restoreJob("storscale-restore-x", "20261017T040000Z", cron)
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
