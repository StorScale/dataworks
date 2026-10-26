package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// options are the flags of a command.
type options struct {
	version    string   // up --k8s, upgrade: a published release (storscale's own when empty)
	volumes    bool     // down
	values     []string // upgrade: -f files
	sets       []string // upgrade: --set k=v
	setFiles   []string // upgrade: --set-file k=path
	yes        bool     // upgrade: without asking
	dryRun     bool     // upgrade: check, and change nothing
	test       bool     // upgrade: then run the end-to-end checks
	force      bool     // upgrade: even to an older version
	imageTag   string   // upgrade --dir on a cluster: the published images to run
	chart      string   // upgrade: the chart repository (oci://...) of a published release
	caFile     string   // upgrade: the CA of a registry with a private certificate
	kubeCtx    string   // upgrade: the cluster (kubectl's context)
	kubeNS     string   // upgrade: the namespace
	kubeRel    string   // upgrade: the Helm release
	kind       bool     // --k8s: the kind cluster storscale makes
	timeout    time.Duration
	keyFile    string // restore: the backup key
	backupName string // restore: which backup (latest)
	outDir     string // backup keygen: where to write the key pair
	withData   bool   // restore: copy the data back too (backup.data)
	intoNew    bool   // restore: into another install than the one backed up
}

// list is a flag that may be given more than once.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// pullChart fetches a published chart (ociChart, or a registry of the
// company's own) at a version, into a directory of its own (removed by the
// caller): its CRDs, and the chart to install.
func pullChart(repo, ver, caFile string) (string, error) {
	dir, err := os.MkdirTemp("", "storscale-chart-")
	if err != nil {
		return "", err
	}
	args := []string{"pull", repo, "--version", ver, "--untar", "--untardir", dir}
	if caFile != "" {
		args = append(args, "--ca-file", caFile)
	}
	c := exec.Command("helm", args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("the chart %s at %s: %w (is %s a published release?)", repo, ver, err, ver)
	}
	return dir, nil
}

// registryClient is an HTTP client that also trusts caFile's CAs (a
// registry with a certificate of the company's own CA).
func registryClient(caFile string) (*http.Client, error) {
	if caFile == "" {
		return http.DefaultClient, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s: no PEM certificates", caFile)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: pool}
	return &http.Client{Transport: t}, nil
}

// imageRegistry is where the upgrade's images come from (images.registry):
// a --set, else the value the release was installed with, else the chart's.
func imageRegistry(o options, chart string) string {
	reg := ""
	for _, s := range o.sets {
		if v, ok := strings.CutPrefix(s, "images.registry="); ok {
			reg = v
		}
	}
	if reg != "" {
		return reg
	}
	if out, err := exec.Command("helm", helmArgs("get", "values", release, "-o", "json")...).Output(); err == nil {
		var v struct {
			Images struct {
				Registry string `json:"registry"`
			} `json:"images"`
		}
		if json.Unmarshal(out, &v) == nil && v.Images.Registry != "" {
			return v.Images.Registry
		}
	}
	if data, err := os.ReadFile(filepath.Join(chart, "values.yaml")); err == nil {
		if m := regexp.MustCompile(`(?m)^images:\n(?:[ #].*\n)*?  registry: *([^\s#]+)`).FindSubmatch(data); m != nil {
			return string(m[1])
		}
	}
	return "ghcr.io/storscale"
}

// applyCRDs applies a chart's CRDs (Helm only ever installs them), and
// waits for the API server to serve them.
func applyCRDs(dir string) error {
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if len(files) == 0 {
		return fmt.Errorf("no CRDs in %s", dir)
	}
	for _, f := range files {
		// Server-side, taking the fields over from Helm, which installed them.
		if err := run1("kubectl", kubeArgs("apply", "--server-side", "--force-conflicts", "--field-manager", "storscale", "-f", f)...); err != nil {
			return fmt.Errorf("applying %s: %w", filepath.Base(f), err)
		}
		if err := run1("kubectl", kubeArgs("wait", "--for", "condition=established", "--timeout", "60s", "-f", f)...); err != nil {
			return err
		}
	}
	return nil
}

// installed is the release as Helm has it.
type installed struct {
	Name       string   `json:"name"`
	Revision   revision `json:"revision"`
	Status     string   `json:"status"`
	Chart      string   `json:"chart"` // storscale-platform-0.6.0
	AppVersion string   `json:"app_version"`
}

// revision is Helm's: text in Helm 3's JSON, a number in others'.
type revision string

func (r *revision) UnmarshalJSON(b []byte) error {
	*r = revision(strings.Trim(string(b), `"`))
	return nil
}

func currentRelease() (*installed, error) {
	out, err := exec.Command("helm", helmArgs("list", "--filter", "^"+regexp.QuoteMeta(release)+"$", "--deployed", "--failed", "--pending", "-o", "json")...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("helm list: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, err
	}
	var rs []installed
	if err := json.Unmarshal(out, &rs); err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}
	return &rs[0], nil
}

// The installed chart's version, as its name has it.
func (r *installed) version() string {
	return strings.TrimPrefix(r.Chart, "storscale-platform-")
}

var semverRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// older says a is an earlier release than b; ok is false when either isn't
// a version (dev).
func older(a, b string) (bool, bool) {
	ma, mb := semverRE.FindStringSubmatch(a), semverRE.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return false, false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x < y, true
		}
	}
	// 1.0.0-rc.1 comes before 1.0.0.
	switch {
	case ma[4] == mb[4]:
		return false, true
	case ma[4] == "":
		return false, true
	case mb[4] == "":
		return true, true
	}
	return ma[4] < mb[4], true
}

// helmCanResetValues: --reset-then-reuse-values came in Helm 3.14.
func helmCanResetValues() error {
	out, err := exec.Command("helm", "version", "--short").Output()
	if err != nil {
		return err
	}
	m := regexp.MustCompile(`v(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		return nil // can't tell: let helm say
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 3 || major == 3 && minor < 14 {
		return fmt.Errorf("upgrade needs Helm 3.14 or later (this is %s)", strings.TrimSpace(string(out)))
	}
	return nil
}

// The platform's images, as the chart names them (values.yaml, images.*).
var images = []string{"platformd", "platform-tools", "platform-airflow", "platform-superset", "platform-jupyterhub",
	"platform-notebook", "platform-opensearch", "platform-semanticd", "platform-catalog-sync", "platform-mlflow"}

// missingImages are the platform's images not published at a tag, under
// prefix (storscale on ghcr.io, a project's path on Harbor) at base (the
// registry's https://host), asked anonymously as the registry says to: a
// 401's WWW-Authenticate names where to get a token, and for what.
func missingImages(ctx context.Context, c *http.Client, base, prefix, tag string) ([]string, error) {
	var missing []string
	for _, name := range images {
		repo := strings.Trim(prefix+"/"+name, "/")
		head := func(token string) (*http.Response, error) {
			req, _ := http.NewRequestWithContext(ctx, "HEAD", base+"/v2/"+repo+"/manifests/"+url.PathEscape(tag), nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
			res, err := c.Do(req)
			if err != nil {
				return nil, fmt.Errorf("the image registry: %w", err)
			}
			res.Body.Close()
			return res, nil
		}
		res, err := head("")
		if err != nil {
			return nil, err
		}
		if res.StatusCode == http.StatusUnauthorized {
			token, err := registryToken(ctx, c, res.Header.Get("WWW-Authenticate"))
			if err != nil {
				return nil, fmt.Errorf("the image registry: %s: %w", repo, err)
			}
			if res, err = head(token); err != nil {
				return nil, err
			}
		}
		switch {
		case res.StatusCode == http.StatusNotFound:
			missing = append(missing, name)
		case res.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("the image registry: %s:%s: HTTP %d", repo, tag, res.StatusCode)
		}
	}
	return missing, nil
}

// registryToken gets an anonymous pull token as a registry's challenge
// (Bearer realm="...",service="...",scope="...") says to.
func registryToken(ctx context.Context, c *http.Client, challenge string) (string, error) {
	params := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+)="([^"]*)"`).FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") || params["realm"] == "" {
		return "", fmt.Errorf("it asks for a sign-in this check can't give (%q)", challenge)
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", params["realm"]+"?"+q.Encode(), nil)
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&tok) != nil {
		return "", fmt.Errorf("its token service answered HTTP %d", res.StatusCode)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	return tok.Token, nil
}

// upgrade takes an installed platform to a published release (or, with
// --dir, a checkout's chart): the release's CRDs first, as Helm never
// upgrades them; then the chart, keeping the values it was installed with;
// then it waits for every service.
func (k *kube) upgrade(st *stack, o options) error {
	if err := need("kubectl", "helm"); err != nil {
		return err
	}
	if err := helmCanResetValues(); err != nil {
		return err
	}
	kind := o.kind

	cur, err := currentRelease()
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("no release %q in the namespace %q (context %q): install it first", release, namespace, kubeContext)
	}
	switch {
	case strings.HasPrefix(cur.Status, "pending"):
		return fmt.Errorf("the release %s is %s: another install, upgrade or rollback is under way (or was cut off; see `helm history`)", release, cur.Status)
	case cur.Status != "deployed" && cur.Status != "failed":
		return fmt.Errorf("the release %s is %s", release, cur.Status)
	}

	// What it goes to: a published release's chart, or the checkout's.
	var chart, target, tag string
	switch {
	case k.checkout != "" && kind:
		if o.version != "" {
			return errors.New("--dir upgrades to the checkout: leave out --version (or --dir, for a published release)")
		}
		if !o.dryRun {
			fmt.Println("building the platform's images from", k.checkout)
			if err := run1(filepath.Join(k.checkout, "deploy", "kind", "images.sh"), kindCluster); err != nil {
				return err
			}
		}
		chart = filepath.Join(k.checkout, "deploy", "helm", "storscale-platform")
		target, tag = version()+" (this checkout)", "dev"
	case k.checkout != "":
		// A checkout's chart on a real cluster runs published images: which?
		if o.imageTag == "" {
			return errors.New("--dir on a cluster: say which published images to run (--image-tag <version>); the checkout's own aren't published")
		}
		chart = filepath.Join(k.checkout, "deploy", "helm", "storscale-platform")
		target, tag = version()+" (this checkout)", o.imageTag
	default:
		target = o.version
		if target == "" {
			target = version()
		}
		target = strings.TrimPrefix(target, "v")
		tag = target
		repo := o.chart
		if repo == "" {
			repo = ociChart
		}
		dir, err := pullChart(repo, target, o.caFile)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		chart = filepath.Join(dir, "storscale-platform")
	}
	crds := filepath.Join(chart, "crds")
	if k.checkout != "" {
		run1("helm", "repo", "add", "spark-operator", "https://kubeflow.github.io/spark-operator", "--force-update")
		if err := run1("helm", "dependency", "build", chart); err != nil {
			return err
		}
	}

	// Checks: not backwards, and the images there to pull.
	from := cur.version()
	if back, ok := older(tag, from); ok && back && !o.force {
		return fmt.Errorf("%s is older than the installed %s: a downgrade isn't tested, and a newer release's database changes may not undo (--force to do it anyway; `helm rollback` returns to an earlier revision)", tag, from)
	}
	registry := imageRegistry(o, chart)
	if tag != "dev" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		client, err := registryClient(o.caFile)
		if err != nil {
			return err
		}
		host, prefix, _ := strings.Cut(registry, "/")
		missing, err := missingImages(ctx, client, "https://"+host, prefix, tag)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return fmt.Errorf("these images aren't published at %s/*:%s: %s (a release's images are published before its chart; wait for its images pipeline)", registry, tag, strings.Join(missing, ", "))
		}
	}

	undo := strings.Join(append([]string{"helm"}, helmArgs("rollback", release, string(cur.Revision))...), " ")
	where := kubeContext
	if where == "" {
		out, _ := exec.Command("kubectl", "config", "current-context").Output()
		where = strings.TrimSpace(string(out)) + " (kubectl's current context)"
	}
	fmt.Printf(`
Upgrade the platform
  cluster      %s
  release      %s, in the namespace %s (revision %s, %s)
  from         %s
  to           %s, its images %s/*:%s
  values       those it was installed with, then the new release's defaults for new settings%s
Steps
  1. apply the release's Project definition (CRD), which Helm never upgrades
  2. helm upgrade, with the values above
  3. wait for every service to be ready
To undo: %s (the Project definition stays: its new fields are optional)

`, where, release, namespace, cur.Revision, cur.Status, from, target, registry, tag, strings.Join(append([]string{""}, append(o.values, append(o.sets, o.setFiles...)...)...), "\n               "), undo)

	args := []string{"upgrade", release, chart, "--reset-then-reuse-values", "--set", "images.tag=" + tag, "--timeout", "15m"}
	if tag == "dev" {
		args = append(args, devSets...)
	}
	for _, f := range o.values {
		args = append(args, "-f", f)
	}
	for _, s := range o.sets {
		args = append(args, "--set", s)
	}
	for _, s := range o.setFiles {
		args = append(args, "--set-file", s)
	}

	if o.dryRun {
		fmt.Println("The Project definition's changes (kubectl diff):")
		kubectl("diff", "--server-side", "--force-conflicts", "--field-manager", "storscale", "-f", crds)
		fmt.Println("\nChecking the chart renders, against the cluster (helm upgrade --dry-run=server):")
		c := exec.Command("helm", helmArgs(append(args, "--dry-run=server")...)...)
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("the chart doesn't render: %w", err)
		}
		fmt.Println("It does. Nothing was changed (--dry-run).")
		return nil
	}
	if !o.yes {
		fmt.Print("Go ahead? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("not upgraded")
		}
	}

	fmt.Println("\n1. The Project definition")
	if err := applyCRDs(crds); err != nil {
		return err
	}
	fmt.Println("\n2. helm upgrade")
	if err := helm(args...); err != nil {
		return fmt.Errorf("helm upgrade: %w (to undo: %s)", err, undo)
	}
	fmt.Println("\n3. Waiting for every service")
	limit := o.timeout
	if limit == 0 {
		limit = 60 * time.Minute
	}
	if err := waitReady(limit); err != nil {
		return fmt.Errorf("upgraded, but %w (see kubectl get pods,jobs -n %s; to undo: %s)", err, namespace, undo)
	}
	now, _ := currentRelease()
	if now != nil {
		fmt.Printf("\nUpgraded %s from %s to %s (revision %s).\n", release, from, target, now.Revision)
	}
	if o.test {
		fmt.Println("\nThe end-to-end checks (helm test; the ConfigMap storscale-tests picks them):")
		return helm("test", release, "--logs", "--timeout", "90m")
	}
	return nil
}
