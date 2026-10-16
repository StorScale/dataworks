package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	platform "github.com/StorScale/storscale-platform"
)

// The platform on a kind cluster of its own (storscale ... --k8s): the
// Helm chart, deploy/helm/storscale-platform, installed as the release
// "storscale" in the namespace "storscale".
const (
	kindCluster = "storscale"
	ociChart    = "oci://ghcr.io/storscale/charts/storscale-platform"
)

// Where the platform is: kind's, unless `storscale upgrade` is told of
// another cluster (--context "" is kubectl's current one).
var (
	kubeContext = "kind-storscale"
	namespace   = "storscale"
	release     = "storscale"
)

// kubeArgs are kubectl's flags for the platform's cluster and namespace.
func kubeArgs(args ...string) []string {
	var out []string
	if kubeContext != "" {
		out = append(out, "--context", kubeContext)
	}
	return append(append(out, "--namespace", namespace), args...)
}

// kubeOut is kubectl's output, for reading.
func kubeOut(args ...string) ([]byte, error) {
	return exec.Command("kubectl", kubeArgs(args...)...).Output()
}

// kube is the platform on kind. checkout is the repository the stack is in,
// when it's a checkout's stack (--dir stack): then the images are built from
// it and the chart is its own. Otherwise the published chart is installed, at
// this storscale's version.
type kube struct{ checkout string }

func newKube(st *stack) *kube {
	root := filepath.Dir(st.dir)
	if _, err := os.Stat(filepath.Join(root, "deploy", "helm", "storscale-platform", "Chart.yaml")); err == nil {
		return &kube{checkout: root}
	}
	return &kube{}
}

func need(tools ...string) error {
	var missing []string
	for _, t := range tools {
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("--k8s needs %s on the PATH", strings.Join(missing, ", "))
	}
	return nil
}

func run1(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func kubectl(args ...string) error {
	return run1("kubectl", kubeArgs(args...)...)
}

func helmArgs(args ...string) []string {
	var out []string
	if kubeContext != "" {
		out = append(out, "--kube-context", kubeContext)
	}
	return append(append(out, "--namespace", namespace), args...)
}

func helm(args ...string) error {
	return run1("helm", helmArgs(args...)...)
}

// script writes one of deploy/kind's files out, to run or to pass to kind.
func script(name string) (string, error) {
	data, err := platform.Kind.ReadFile("deploy/kind/" + name)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "storscale-*-"+name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(data)
	return f.Name(), err
}

func (k *kube) up(st *stack, ver string) error {
	if err := need("docker", "kind", "kubectl", "helm"); err != nil {
		return err
	}
	if os.Getenv("STORSCALE_PORT") != "" && os.Getenv("STORSCALE_PORT") != "8800" {
		return errors.New("--k8s uses port 8800 (deploy/kind/cluster.yaml maps it)")
	}
	clusters, err := exec.Command("kind", "get", "clusters").Output()
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Fields(string(clusters)), kindCluster) {
		cfg, err := script("cluster.yaml")
		if err != nil {
			return err
		}
		defer os.Remove(cfg)
		if err := run1("kind", "create", "cluster", "--name", kindCluster, "--config", cfg, "--wait", "180s"); err != nil {
			return err
		}
	}
	dns, err := script("dns.sh")
	if err != nil {
		return err
	}
	defer os.Remove(dns)
	c := exec.Command("bash", dns, st.setting("STORSCALE_DOMAIN"), namespace)
	c.Env = append(os.Environ(), "KUBECTL_CONTEXT="+kubeContext)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("the cluster's DNS: %w", err)
	}

	chart, extra := ociChart, []string{"--version", ver}
	crds := ""
	if k.checkout != "" && ver != version() {
		return errors.New("--version is for a published release: leave out --dir to install one")
	}
	if k.checkout == "" {
		dir, err := pullChart(ver)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		crds = filepath.Join(dir, "storscale-platform", "crds")
	} else {
		crds = filepath.Join(k.checkout, "deploy", "helm", "storscale-platform", "crds")
	}
	if k.checkout != "" {
		fmt.Println("building the platform's images from", k.checkout)
		if err := run1(filepath.Join(k.checkout, "deploy", "kind", "images.sh"), kindCluster); err != nil {
			return err
		}
		chart, extra = filepath.Join(k.checkout, "deploy", "helm", "storscale-platform"), []string{"--set", "images.tag=dev"}
		run1("helm", "repo", "add", "spark-operator", "https://kubeflow.github.io/spark-operator", "--force-update")
		if err := run1("helm", "dependency", "build", chart); err != nil {
			return err
		}
	}
	args := append([]string{"upgrade", "--install", release, chart, "--create-namespace", "--timeout", "10m",
		"--set", "global.domain=" + st.setting("STORSCALE_DOMAIN")}, extra...)
	for _, f := range strings.Fields(os.Getenv("STORSCALE_K8S_VALUES")) {
		args = append(args, "-f", f)
	}
	// Helm installs a chart's CRDs once and never upgrades them: an older
	// one drops the fields it lacks from every project.
	if err := applyCRDs(crds); err != nil {
		return err
	}
	if err := helm(args...); err != nil {
		return err
	}
	if err := waitReady(60 * time.Minute); err != nil {
		return fmt.Errorf("the platform didn't start: %w (see `storscale status --k8s` and `storscale logs --k8s <service>`)", err)
	}
	fmt.Println()
	printURLs(st)
	fmt.Println("\nOn kind, the people's password is in the cluster:")
	fmt.Printf("  kubectl --context %s -n %s get secret storscale-env -o jsonpath='{.data.LAKEHOUSE_USER_PASSWORD}' | base64 -d\n", kubeContext, namespace)
	return nil
}

// test runs the suites asked for (all of them when none are) as `helm
// test`; the ConfigMap storscale-tests says which.
func (k *kube) test(suites []string) error {
	if len(suites) == 0 {
		kubectl("delete", "configmap", "storscale-tests", "--ignore-not-found")
	} else {
		manifest, err := kubeOut("create", "configmap", "storscale-tests",
			"--from-literal=suites="+strings.Join(suites, " "), "--dry-run=client", "-o", "yaml")
		if err != nil {
			return err
		}
		c := exec.Command("kubectl", kubeArgs("apply", "-f", "-")...)
		c.Stdin, c.Stderr = strings.NewReader(string(manifest)), os.Stderr
		if err := c.Run(); err != nil {
			return err
		}
	}
	return helm("test", release, "--logs", "--timeout", "60m")
}

func (k *kube) do(cmd string, st *stack, args []string, o options) error {
	if err := need("kind", "kubectl", "helm"); err != nil {
		return err
	}
	ver := o.version
	if ver == "" {
		ver = version()
	}
	switch cmd {
	case "up":
		return k.up(st, ver)
	case "upgrade":
		return k.upgrade(st, o)
	case "status":
		return kubectl("get", "pods,jobs,projects,bucketsclusters")
	case "urls":
		printURLs(st)
		return nil
	case "test":
		return k.test(args)
	case "logs":
		if len(args) == 0 {
			return errors.New("logs --k8s <service ...>: which service's (gateway, keycloak, trino, ...)")
		}
		for _, svc := range args {
			if err := kubectl("logs", "--tail", "100", "--all-containers", "deploy/"+svc); err != nil {
				return err
			}
		}
		return nil
	case "down":
		if o.volumes {
			return run1("kind", "delete", "cluster", "--name", kindCluster)
		}
		return helm("uninstall", release)
	}
	return fmt.Errorf("unknown command %q (see storscale help)", cmd)
}

// pod is what waitReady reads of a pod.
type pod struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type, Status string
		} `json:"conditions"`
		InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
		ContainerStatuses     []containerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type containerStatus struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restartCount"`
	State        struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
		Running    *struct{} `json:"running"`
		Terminated *struct {
			ExitCode int `json:"exitCode"`
		} `json:"terminated"`
	} `json:"state"`
}

// why says what a pod that isn't ready is doing: waiting for a setup step
// or a service (its init container "wait-<what>"), pulling its image, or
// failing. stuck: it won't get better by waiting.
func (p *pod) why() (string, bool) {
	for _, c := range p.Status.InitContainerStatuses {
		if c.State.Terminated != nil && c.State.Terminated.ExitCode == 0 {
			continue
		}
		if w := c.State.Waiting; w != nil && w.Reason != "PodInitializing" {
			return c.Name + ": " + w.Reason, stuckReason(w.Reason, c.RestartCount)
		}
		return strings.TrimPrefix(c.Name, "wait-") + " first", false
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Ready {
			continue
		}
		if w := c.State.Waiting; w != nil {
			return fmt.Sprintf("%s (%d restarts)", w.Reason, c.RestartCount), stuckReason(w.Reason, c.RestartCount)
		}
		if c.RestartCount > 0 { // between crashes: as stuck as in CrashLoopBackOff
			return fmt.Sprintf("starting (%d restarts)", c.RestartCount), c.RestartCount >= crashLimit
		}
		return "starting", false
	}
	return strings.ToLower(p.Status.Phase), false
}

// crashLimit: a container restarted this often keeps crashing.
const crashLimit = 6

func stuckReason(reason string, restarts int) bool {
	switch reason {
	case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError":
		return true
	case "CrashLoopBackOff":
		return restarts >= crashLimit
	}
	return false
}

// failedJob names a setup step (Job) that has run out of tries, and why.
func failedJob() (string, string) {
	out, err := kubeOut("get", "jobs", "-o", "json")
	if err != nil {
		return "", ""
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct{ Type, Status, Reason, Message string } `json:"conditions"`
			} `json:"status"`
		}
	}
	json.Unmarshal(out, &list)
	for _, j := range list.Items {
		for _, c := range j.Status.Conditions {
			if c.Type == "Failed" && c.Status == "True" {
				return j.Metadata.Name, strings.TrimSpace(c.Reason + ": " + c.Message)
			}
		}
	}
	return "", ""
}

// showFailures prints, once each, the logs of a setup step's failed try (its
// pod is kept) and of a container's run before each restart, so a failure
// says why as it happens, before its pod is gone.
func showFailures(p pod, shown map[string]int) {
	if _, job := p.Metadata.Labels["job-name"]; job && p.Status.Phase == "Failed" {
		if _, ok := shown[p.Metadata.Name]; !ok {
			shown[p.Metadata.Name] = 1
			fmt.Printf("--- %s failed:\n", p.Metadata.Name)
			kubectl("logs", p.Metadata.Name, "--all-containers", "--tail", "40")
		}
		return
	}
	for _, c := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		key := p.Metadata.Name + "/" + c.Name
		if c.RestartCount > shown[key] {
			shown[key] = c.RestartCount
			fmt.Printf("--- %s, container %s, restarted (%d); its last run:\n", p.Metadata.Name, c.Name, c.RestartCount)
			kubectl("logs", p.Metadata.Name, "-c", c.Name, "--previous", "--tail", "40")
		}
	}
}

// rollingOut are the Deployments and StatefulSets whose pods aren't all of
// their latest spec yet: just after an upgrade the old pods are still ready.
func rollingOut() []string {
	out, err := kubeOut("get", "deployments,statefulsets", "-o", "json")
	if err != nil {
		return nil
	}
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name       string `json:"name"`
				Generation int64  `json:"generation"`
			} `json:"metadata"`
			Spec struct {
				Replicas       *int32 `json:"replicas"`
				UpdateStrategy struct {
					Type          string `json:"type"`
					RollingUpdate *struct {
						Partition *int32 `json:"partition"`
					} `json:"rollingUpdate"`
				} `json:"updateStrategy"` // StatefulSets'
			} `json:"spec"`
			Status struct {
				ObservedGeneration int64 `json:"observedGeneration"`
				UpdatedReplicas    int32 `json:"updatedReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	json.Unmarshal(out, &list)
	var rolling []string
	for _, d := range list.Items {
		if d.Spec.UpdateStrategy.Type == "OnDelete" {
			continue // its pods change only as they're deleted
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		if ru := d.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
			want -= *ru.Partition // those below it keep the old spec
		}
		if d.Status.ObservedGeneration < d.Metadata.Generation || d.Status.UpdatedReplicas < want {
			rolling = append(rolling, strings.ToLower(d.Kind)+"/"+d.Metadata.Name+" (rolling out)")
		}
	}
	return rolling
}

// waitReady waits for every pod of the platform to be ready, saying every
// half minute which aren't and why, and the logs of what fails as it fails.
// It gives up early on a pod that is stuck (its image can't be pulled, or it
// keeps crashing), with its logs, and on a setup step out of tries.
func waitReady(limit time.Duration) error {
	deadline := time.Now().Add(limit)
	stuckSince := map[string]time.Time{}
	shown := map[string]int{}
	for {
		if job, why := failedJob(); job != "" {
			return fmt.Errorf("the setup step %s failed (%s); its tries' logs are above", job, why)
		}
		out, err := kubeOut("get", "pods", "-o", "json")
		if err != nil {
			fmt.Fprintln(os.Stderr, "reading the pods:", err)
		} else {
			var list struct{ Items []pod }
			json.Unmarshal(out, &list)
			var waiting []string
			for _, p := range list.Items {
				showFailures(p, shown)
				_, job := p.Metadata.Labels["job-name"]
				if p.Status.Phase == "Succeeded" || (job && p.Status.Phase == "Failed") || strings.HasPrefix(p.Metadata.Name, "storscale-test") {
					continue // a finished setup step, a failed try of one (it's tried again), or a test
				}
				ready := false
				for _, c := range p.Status.Conditions {
					ready = ready || (c.Type == "Ready" && c.Status == "True")
				}
				if ready {
					delete(stuckSince, p.Metadata.Name)
					continue
				}
				why, stuck := p.why()
				waiting = append(waiting, p.Metadata.Name+" ("+why+")")
				if !stuck {
					delete(stuckSince, p.Metadata.Name)
				} else if since, ok := stuckSince[p.Metadata.Name]; !ok {
					stuckSince[p.Metadata.Name] = time.Now()
				} else if time.Since(since) > 5*time.Minute {
					kubectl("logs", p.Metadata.Name, "--all-containers", "--tail", "40")
					return fmt.Errorf("%s is stuck: %s", p.Metadata.Name, why)
				}
			}
			waiting = append(waiting, rollingOut()...)
			if len(list.Items) > 0 && len(waiting) == 0 {
				fmt.Println("every service is ready")
				return nil
			}
			sort.Strings(waiting)
			fmt.Printf("%s  waiting for %d: %s\n", time.Now().Format("15:04:05"), len(waiting), strings.Join(waiting, ", "))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s", limit)
		}
		time.Sleep(30 * time.Second)
	}
}
