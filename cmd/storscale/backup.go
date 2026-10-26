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
	dataCron     = "storscale-backup-data" // the data itself, copied off the cluster (backup.data)
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
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "DataWorks backups"},
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
	stamp := time.Now().UTC().Format("20060102-150405")
	name := "storscale-backup-now-" + stamp
	if err := kubectl("create", "job", name, "--from=cronjob/"+backupCron); err != nil {
		return err
	}
	if err := waitJob(name, 60*time.Minute, "dump", "backup"); err != nil {
		return err
	}
	if _, err := kubeOut("get", "cronjob", dataCron); err != nil {
		return nil // the data isn't copied here (backup.data)
	}
	name = "storscale-backup-data-now-" + stamp
	if err := kubectl("create", "job", name, "--from=cronjob/"+dataCron); err != nil {
		return err
	}
	return waitJob(name, 6*time.Hour, "data")
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
func restoreJob(name, backup string, cronJSON, dataJSON []byte, intoNew bool) ([]byte, error) {
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
	steps := append(keep, fetch)
	if dataJSON != nil { // the data copied back first: the catalog restored next points at it
		data, err := dataStep(dataJSON)
		if err != nil {
			return nil, err
		}
		steps = append(steps, data)
	}
	steps = append(steps, databases)
	if intoNew {
		for _, c := range []map[string]any{fetch, apply} {
			env, _ := c["env"].([]any)
			c["env"] = append(append([]any{}, env...), map[string]any{"name": "INTO_NEW", "value": "1"})
		}
		steps = append(steps, with(dump, "roles", []string{"bash", "-c", rolesScript}))
	}
	pod["initContainers"] = steps
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

// dataStep is the data copy's container (the storscale-backup-data
// CronJob's), the other way: each bucket copied back from backup.s3.
func dataStep(dataJSON []byte) (map[string]any, error) {
	var cron struct {
		Spec struct {
			JobTemplate struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []map[string]any `json:"containers"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			} `json:"jobTemplate"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(dataJSON, &cron); err != nil {
		return nil, err
	}
	cs := cron.Spec.JobTemplate.Spec.Template.Spec.Containers
	if len(cs) == 0 {
		return nil, errors.New("the CronJob storscale-backup-data has no container")
	}
	c := map[string]any{}
	for k, v := range cs[0] {
		c[k] = v
	}
	c["name"], c["command"] = "data", []string{"sh", "-c", dataRestoreScript}
	delete(c, "args")
	return c, nil
}

// dataRestoreScript copies each bucket's copy back into Buckets.
const dataRestoreScript = `set -eu
mc alias set src http://buckets:9000 "$BUCKETS_ROOT_USER" "$BUCKETS_ROOT_PASSWORD" >/dev/null
mc alias set dst "$BACKUP_ENDPOINT" "$BACKUP_ACCESS_KEY" "$BACKUP_SECRET_KEY" >/dev/null
for b in $(mc ls "dst/$BACKUP_BUCKET/data/" | awk '{print $NF}' | tr -d /); do
  echo "data: $b"
  mc mb --ignore-existing "src/$b" >/dev/null
  mc mirror --overwrite --quiet "dst/$BACKUP_BUCKET/data/$b" "src/$b"
done
echo "data: copied back"`

// rolesScript gives each database role the backup's password (roles.sql,
// which fetch wrote from the backup's storscale-env), as the admin of now.
const rolesScript = `set -euo pipefail
psql -h postgres -U postgres -v ON_ERROR_STOP=1 -q -f /backup/roles.sql
PGPASSWORD="$RANGER_PASSWORD" psql -h ranger-db -U postgres -v ON_ERROR_STOP=1 -q -f /backup/ranger-roles.sql
echo "the roles have the backup's passwords"`

// restoreScript puts every database back from its dump, as Postgres' admin:
// each dropped whole and made again from the dump, with its owner and
// rights (pg_restore --clean can't drop a partitioned table's inherited
// constraints, as OpenMetadata's have). A restore cut off is run again.
// set -o pipefail: a pg_restore or psql that fails in a pipe fails it.
const restoreScript = `set -euo pipefail
for f in /backup/postgres/*.dump; do
  db=$(basename "$f" .dump)
  echo "restoring $db"
  psql -h postgres -U postgres -v ON_ERROR_STOP=1 -qc "DROP DATABASE IF EXISTS \"$db\" WITH (FORCE)"
  pg_restore -h postgres -U postgres --create --exit-on-error -d postgres "$f"
done
echo "restoring ranger"
# Ranger's Postgres is older (13) than these tools (18), which write what
# 13 doesn't know (CREATE DATABASE ... LOCALE_PROVIDER, SET transaction_timeout):
# the database made here, with its owner, then its contents as SQL, less that.
export PGPASSWORD="$RANGER_PASSWORD"
owner=$(psql -h ranger-db -U postgres -Atc "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'ranger'")
owner=${owner:-postgres}
psql -h ranger-db -U postgres -v ON_ERROR_STOP=1 -qc "DROP DATABASE IF EXISTS ranger WITH (FORCE)"
psql -h ranger-db -U postgres -v ON_ERROR_STOP=1 -qc "CREATE DATABASE ranger OWNER \"$owner\""
pg_restore -f - /backup/ranger.dump | grep -v '^SET transaction_timeout' | psql -h ranger-db -U postgres -v ON_ERROR_STOP=1 -q -d ranger
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
	var dataJSON []byte
	if o.withData || o.intoNew {
		if dataJSON, err = kubeOut("get", "cronjob", dataCron, "-o", "json"); err != nil {
			return errors.New("--data: the data isn't copied off this install (backup.data), so there's none to copy back")
		}
	}
	data := "The data in Buckets stays as it is."
	if dataJSON != nil {
		data = "The data in Buckets is copied back from its copy first."
	}
	into := ""
	if o.intoNew {
		into = `
  Into another install than the one backed up (--into-new): each database
  role is given its password of then, the backup's storscale-env put back,
  Buckets restarted and the setup steps run again, so every account matches.`
	}
	fmt.Printf(`
Restore the platform from a backup
  cluster      %s, namespace %s
  backup       %s
  Every Deployment stops (%d of them: the platform is down meanwhile); every
  database is replaced by the backup's (Keycloak's people, Nessie's catalog,
  Airflow, Superset, OpenMetadata, Ranger's policies...); the backup's Projects
  and Secrets are put back; then everything starts again. What changed since
  the backup is lost. %s%s

`, kubeContext, namespace, name, len(replicas), data, into)
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
	job, err := restoreJob(jobName, name, cronJSON, dataJSON, o.intoNew)
	if err != nil {
		return err
	}
	if err := applyJSON(job); err != nil {
		return err
	}
	logs := []string{"fetch"}
	if dataJSON != nil {
		logs = append(logs, "data")
	}
	logs = append(logs, "databases")
	if o.intoNew {
		logs = append(logs, "roles")
	}
	jobErr := waitJob(jobName, 6*time.Hour, append(logs, "apply")...)

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
	if o.intoNew {
		// Buckets takes its root account (buckets-root, put back) as it
		// starts; the setup steps give Buckets' and Keycloak's accounts the
		// backup's secrets again.
		fmt.Println("\n4. Buckets started again, and the setup steps run again")
		if err := kubectl("delete", "pod", "-l", "buckets.io/cluster=buckets", "--wait=false"); err != nil {
			return err
		}
		if err := rerunSetup(); err != nil {
			return fmt.Errorf("restored, but the setup steps didn't run again: %w", err)
		}
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
		// The Deployments' replicas count no pod that's terminating, and one
		// can write for its grace period (OpenMetadata's scheduler did, into
		// a database being restored): their pods gone, too.
		out, err := kubeOut("get", "deployments", "-o", "jsonpath={range .items[*]}{.status.replicas}{\" \"}{end}")
		pods, perr := kubeOut("get", "pods", "-o", "jsonpath={range .items[*]}{.metadata.ownerReferences[0].kind}{\" \"}{end}")
		if err == nil && perr == nil && strings.Trim(strings.ReplaceAll(string(out), "0", ""), " ") == "" &&
			!strings.Contains(string(pods), "ReplicaSet") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Deployments still running after %s", limit)
		}
		time.Sleep(5 * time.Second)
	}
}

// rerunSetup runs the release's setup steps again (setup, setup-trino), as
// copies of their Jobs of the current revision.
func rerunSetup() error {
	out, err := kubeOut("get", "jobs", "-o", "json")
	if err != nil {
		return err
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	for _, step := range []string{"setup", "setup-trino"} {
		job := latestRevision(list.Items, step)
		if job == nil {
			return fmt.Errorf("no Job %s-<revision>", step)
		}
		copy, err := jobCopy(job, step+"-again-"+stamp)
		if err != nil {
			return err
		}
		if err := applyJSON(copy); err != nil {
			return err
		}
		if err := waitJob(step+"-again-"+stamp, 30*time.Minute, "setup"); err != nil {
			return err
		}
	}
	return nil
}

// latestRevision is the Job <step>-<n> with the highest n.
func latestRevision(jobs []map[string]any, step string) map[string]any {
	var best map[string]any
	bestN := -1
	for _, j := range jobs {
		md, _ := j["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		rest, ok := strings.CutPrefix(name, step+"-")
		if !ok {
			continue
		}
		n := 0
		if _, err := fmt.Sscanf(rest, "%d", &n); err != nil || fmt.Sprint(n) != rest {
			continue // setup-trino-14 is not setup-<n>
		}
		if n > bestN {
			best, bestN = j, n
		}
	}
	return best
}

// jobCopy is a Job made again under another name: what Kubernetes set on it
// (its selector, its pods' controller labels) left out.
func jobCopy(job map[string]any, name string) ([]byte, error) {
	spec, _ := job["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	tmd, _ := tmpl["metadata"].(map[string]any)
	if labels, ok := tmd["labels"].(map[string]any); ok {
		for _, k := range []string{"controller-uid", "batch.kubernetes.io/controller-uid", "job-name", "batch.kubernetes.io/job-name"} {
			delete(labels, k)
		}
	}
	newSpec := map[string]any{"backoffLimit": spec["backoffLimit"], "template": tmpl}
	md, _ := job["metadata"].(map[string]any)
	labels, _ := md["labels"].(map[string]any)
	for _, k := range []string{"controller-uid", "batch.kubernetes.io/controller-uid", "job-name", "batch.kubernetes.io/job-name"} {
		delete(labels, k)
	}
	return json.Marshal(map[string]any{"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "labels": labels}, "spec": newSpec})
}
