package main

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Backups (deploy/helm/storscale-platform/templates/backup.yaml): each
// night every database is dumped and the Projects and Secrets exported,
// encrypted for the chart's backup.certificate. storscale backup keygen
// makes that certificate and the key a restore needs; storscale backup takes
// one now; storscale restore puts one back, into the same install.

const (
	backupCron   = "storscale-backup"
	restoreKey   = "storscale-restore-key"
	restoreSaved = "storscale-restore-replicas" // the Deployments' replicas while a restore runs
)

// keygen writes a key pair: the certificate for the chart, and the key, for
// a restore, which belongs somewhere else than the cluster.
func keygen(dir string) error {
	if dir == "" {
		return errors.New("backup keygen --out DIR: where to write the key pair")
	}
	key, cert := filepath.Join(dir, "storscale-backup-key.pem"), filepath.Join(dir, "storscale-backup-cert.pem")
	for _, f := range []string{key, cert} {
		if _, err := os.Stat(f); err == nil {
			return fmt.Errorf("%s exists: keygen never overwrites a key (backups made for it need it)", f)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "StorScale Platform backups"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(30, 0, 0),
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	fmt.Printf(`Wrote
  %s   the certificate backups are encrypted for: give it to the chart,
      --set-file backup.certificate=%s
  %s   the key that opens them: keep it somewhere else than the cluster
      (a password manager, a vault). Without it no backup can be restored;
      with it, anyone can read them.
`, cert, cert, key)
	return nil
}

// jobState is a Job's end: complete, failed, or "" while it runs.
func jobState(name string) (string, error) {
	out, err := kubeOut("get", "job", name, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("job %s: %w", name, err)
	}
	var j struct {
		Status struct {
			Conditions []struct{ Type, Status, Message string } `json:"conditions"`
		} `json:"status"`
	}
	json.Unmarshal(out, &j)
	for _, c := range j.Status.Conditions {
		if c.Status != "True" {
			continue
		}
		switch c.Type {
		case "Complete":
			return "complete", nil
		case "Failed":
			return "failed: " + c.Message, nil
		}
	}
	return "", nil
}

func waitJob(name string, limit time.Duration, containers ...string) error {
	deadline := time.Now().Add(limit)
	for {
		state, err := jobState(name)
		if err != nil {
			return err
		}
		if state != "" {
			for _, c := range containers {
				fmt.Printf("--- %s:\n", c)
				kubectl("logs", "job/"+name, "-c", c, "--tail", "60")
			}
			if state != "complete" {
				return fmt.Errorf("job %s %s", name, state)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("job %s still running after %s", name, limit)
		}
		time.Sleep(5 * time.Second)
	}
}

// backupNow runs the nightly backup now.
func backupNow() error {
	if _, err := kubeOut("get", "cronjob", backupCron); err != nil {
		return errors.New("backups aren't set up here: give the chart backup.certificate (storscale backup keygen makes one)")
	}
	name := "storscale-backup-now-" + time.Now().UTC().Format("20060102-150405")
	if err := kubectl("create", "job", name, "--from=cronjob/"+backupCron); err != nil {
		return err
	}
	return waitJob(name, 60*time.Minute, "dump", "backup")
}

// certOf is the backup certificate the cluster encrypts for.
func certOf() (*x509.Certificate, []byte, error) {
	out, err := kubeOut("get", "secret", "storscale-backup-cert", "-o", "jsonpath={.data.cert\\.pem}")
	if err != nil {
		return nil, nil, errors.New("backups aren't set up here (no Secret storscale-backup-cert)")
	}
	raw, err := base64.StdEncoding.DecodeString(string(out))
	if err != nil {
		return nil, nil, err
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, nil, errors.New("storscale-backup-cert holds no certificate")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	return c, raw, err
}

// keyMatches says whether a private key (PEM) is the certificate's.
func keyMatches(keyPEM []byte, cert *x509.Certificate) error {
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return errors.New("the key file holds no PEM key")
	}
	var priv any
	var err error
	if priv, err = x509.ParsePKCS8PrivateKey(b.Bytes); err != nil {
		if priv, err = x509.ParsePKCS1PrivateKey(b.Bytes); err != nil {
			return fmt.Errorf("the key file: %w", err)
		}
	}
	rk, ok := priv.(*rsa.PrivateKey)
	pub, ok2 := cert.PublicKey.(*rsa.PublicKey)
	if !ok || !ok2 || rk.PublicKey.N.Cmp(pub.N) != 0 || rk.PublicKey.E != pub.E {
		return errors.New("this key isn't the one for the cluster's backup certificate: its backups can't be opened with it")
	}
	return nil
}

// deployments are the Deployments' replicas now.
func deployments() (map[string]int, error) {
	out, err := kubeOut("get", "deployments", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
			Spec     struct {
				Replicas *int `json:"replicas"`
			} `json:"spec"`
			Status struct {
				Replicas int `json:"replicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	out2 := map[string]int{}
	for _, d := range list.Items {
		n := 1
		if d.Spec.Replicas != nil {
			n = *d.Spec.Replicas
		}
		out2[d.Metadata.Name] = n
	}
	return out2, nil
}

// restoreJob is the restore's Job, built from the backup CronJob's own pod
// (its images, settings and volumes): the backup fetched and decrypted, the
// databases put back, then the Projects and Secrets.
func restoreJob(name, backup string, cronJSON []byte) ([]byte, error) {
	out := cronJSON
	var cron struct {
		Spec struct {
			JobTemplate struct {
				Spec struct {
					Template struct {
						Spec map[string]any `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &cron); err != nil {
		return nil, err
	}
	pod := cron.Spec.JobTemplate.Spec.Template.Spec
	containers, _ := pod["containers"].([]any)
	inits, _ := pod["initContainers"].([]any)
	var tools, dump map[string]any
	var keep []any // the CA bundle's step, if there is one
	for _, c := range containers {
		if m, _ := c.(map[string]any); m["name"] == "backup" {
			tools = m
		}
	}
	for _, c := range inits {
		m, _ := c.(map[string]any)
		switch m["name"] {
		case "dump":
			dump = m
		case "ca-bundle":
			keep = append(keep, m)
		}
	}
	if tools == nil || dump == nil {
		return nil, errors.New("the CronJob storscale-backup isn't the chart's (no backup or dump container)")
	}
	with := func(base map[string]any, name string, command []string, mounts ...any) map[string]any {
		c := map[string]any{}
		for k, v := range base {
			c[k] = v
		}
		c["name"], c["command"] = name, command
		delete(c, "args")
		vm, _ := base["volumeMounts"].([]any)
		c["volumeMounts"] = append(append([]any{}, vm...), mounts...)
		return c
	}
	keyMount := map[string]any{"name": "restore-key", "mountPath": "/restore-key", "readOnly": true}
	fetch := with(tools, "fetch", []string{"python", "-u", "/tools/backup.py", "fetch", backup}, keyMount)
	databases := with(dump, "databases", []string{"bash", "-c", restoreScript})
	apply := with(tools, "apply", []string{"python", "-u", "/tools/backup.py", "apply"})
	pod["initContainers"] = append(keep, fetch, databases)
	pod["containers"] = []any{apply}
	vols, _ := pod["volumes"].([]any)
	pod["volumes"] = append(vols, map[string]any{"name": "restore-key", "secret": map[string]any{"secretName": restoreKey}})
	pod["restartPolicy"] = "Never"
	job := map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "labels": map[string]any{"app.kubernetes.io/part-of": "storscale-platform"}},
		"spec": map[string]any{"backoffLimit": 0, "template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app.kubernetes.io/name": "storscale-restore"}}, // for the network policies
			"spec":     pod}},
	}
	return json.Marshal(job)
}

// restoreScript puts every database back from its dump, as Postgres' admin:
// each dropped whole and made again from the dump, with its owner and
// rights (pg_restore --clean can't drop a partitioned table's inherited
// constraints, as OpenMetadata's have). A restore cut off is run again.
const restoreScript = `set -euo pipefail
for f in /backup/postgres/*.dump; do
  db=$(basename "$f" .dump)
  echo "restoring $db"
  psql -h postgres -U postgres -v ON_ERROR_STOP=1 -qc "DROP DATABASE IF EXISTS \"$db\" WITH (FORCE)"
  pg_restore -h postgres -U postgres --create --exit-on-error -d postgres "$f"
done
echo "restoring ranger"
PGPASSWORD="$RANGER_PASSWORD" psql -h ranger-db -U postgres -v ON_ERROR_STOP=1 -qc "DROP DATABASE IF EXISTS ranger WITH (FORCE)"
PGPASSWORD="$RANGER_PASSWORD" pg_restore -h ranger-db -U postgres --create --exit-on-error -d postgres /backup/ranger.dump
echo "databases restored"`

// restore puts a backup back into the install it came from: everything
// stopped, the databases restored, the Projects and Secrets put back, then
// everything started again.
func restore(o options) error {
	if o.keyFile == "" {
		return errors.New("restore --key FILE: the backup key (storscale-backup-key.pem, from storscale backup keygen)")
	}
	keyPEM, err := os.ReadFile(o.keyFile)
	if err != nil {
		return err
	}
	cert, _, err := certOf()
	if err != nil {
		return err
	}
	if err := keyMatches(keyPEM, cert); err != nil {
		return err
	}
	name := o.backupName
	if name == "" {
		name = "latest"
	}
	// An earlier restore cut off leaves the replicas it saved: they, not
	// today's zeros, are what to start again.
	replicas := map[string]int{}
	if out, err := kubeOut("get", "configmap", restoreSaved, "-o", "jsonpath={.data.replicas}"); err == nil && len(out) > 0 {
		if err := json.Unmarshal(out, &replicas); err != nil {
			return fmt.Errorf("%s: %w", restoreSaved, err)
		}
		fmt.Printf("An earlier restore didn't finish: starting from the replicas it saved (%d Deployments).\n", len(replicas))
	} else if replicas, err = deployments(); err != nil {
		return err
	}
	fmt.Printf(`
Restore the platform from a backup
  cluster      %s, namespace %s
  backup       %s
  Every Deployment stops (%d of them: the platform is down meanwhile); every
  database is replaced by the backup's (Keycloak's people, Nessie's catalog,
  Airflow, Superset, OpenMetadata, Ranger's policies...); the backup's Projects
  and Secrets are put back; then everything starts again. What changed since
  the backup is lost. The data in Buckets stays as it is.

`, kubeContext, namespace, name, len(replicas))
	if !o.yes {
		fmt.Print("Go ahead? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("not restored")
		}
	}
	saved, _ := json.Marshal(replicas)
	cm, err := kubeOut("create", "configmap", restoreSaved, "--from-literal=replicas="+string(saved), "--dry-run=client", "-o", "json")
	if err != nil {
		return err
	}
	if err := applyJSON(cm); err != nil {
		return err
	}

	fmt.Println("\n1. Stopping every Deployment")
	if err := kubectl("scale", "deployments", "--all", "--replicas=0"); err != nil {
		return err
	}
	if err := waitStopped(10 * time.Minute); err != nil {
		return err
	}

	fmt.Println("\n2. Restoring", name)
	kubectl("delete", "secret", restoreKey, "--ignore-not-found")
	if err := kubectl("create", "secret", "generic", restoreKey, "--from-file=key.pem="+o.keyFile); err != nil {
		return err
	}
	defer kubectl("delete", "secret", restoreKey, "--ignore-not-found")
	jobName := "storscale-restore-" + time.Now().UTC().Format("20060102-150405")
	cronJSON, err := kubeOut("get", "cronjob", backupCron, "-o", "json")
	if err != nil {
		return errors.New("backups aren't set up here (no CronJob storscale-backup)")
	}
	job, err := restoreJob(jobName, name, cronJSON)
	if err != nil {
		return err
	}
	if err := applyJSON(job); err != nil {
		return err
	}
	jobErr := waitJob(jobName, 60*time.Minute, "fetch", "databases", "apply")

	fmt.Println("\n3. Starting everything again")
	for d, n := range replicas {
		if err := kubectl("scale", "deployment/"+d, fmt.Sprintf("--replicas=%d", n)); err != nil {
			fmt.Fprintf(os.Stderr, "storscale: starting %s again: %v\n", d, err)
		}
	}
	kubectl("delete", "configmap", restoreSaved, "--ignore-not-found")
	if jobErr != nil {
		return fmt.Errorf("the restore failed: %w (the platform was started again; its databases may be part restored: restore again)", jobErr)
	}
	if err := waitReady(o.timeout); err != nil {
		return fmt.Errorf("restored, but %w", err)
	}
	fmt.Println("\nRestored.")
	return nil
}

func applyJSON(doc []byte) error {
	c := exec.Command("kubectl", kubeArgs("apply", "-f", "-")...)
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(string(doc)), os.Stdout, os.Stderr
	return c.Run()
}

// waitStopped waits for the Deployments' pods to be gone.
func waitStopped(limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		out, err := kubeOut("get", "deployments", "-o", "jsonpath={range .items[*]}{.status.replicas}{\" \"}{end}")
		if err == nil && strings.Trim(strings.ReplaceAll(string(out), "0", ""), " ") == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Deployments still running after %s", limit)
		}
		time.Sleep(5 * time.Second)
	}
}
