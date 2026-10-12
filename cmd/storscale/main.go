// storscale runs StorScale Platform on one machine, with Docker Compose, or
// on a kind cluster of its own (--k8s), with the platform's Helm chart.
//
//	storscale up                 start the platform (builds its images the first time)
//	storscale status             the platform's services
//	storscale urls               where each tool is
//	storscale test [suite ...]   the end-to-end checks
//	storscale logs [service]     follow the logs
//	storscale down [--volumes]   stop it (--volumes also deletes its data)
//	storscale version
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const usage = `storscale runs StorScale Platform on one machine, with Docker Compose, or with
--k8s on a kind cluster of its own (the platform's Helm chart).

Usage:
  storscale up [--port N]       start the platform (builds its images the first time)
  storscale status              the platform's services
  storscale urls                where each tool is
  storscale test [suite ...]    the end-to-end checks (all of them, or some of: shell, lakehouse,
                                superset, airflow, jupyterhub, catalog, semantic, projects, monitoring)
  storscale logs [service ...]  follow the logs
  storscale down [--volumes]    stop it; --volumes also deletes its data
  storscale version

Flags for every command:
  --dir DIR   use the stack in DIR instead of the one built into storscale (a checkout's
              stack: with --k8s, the images are built from the checkout, and its chart used)
  --k8s       on kind (the cluster "storscale"; needs kind, kubectl and helm). down --volumes
              deletes the cluster.

Environment:
  STORSCALE_DOMAIN, STORSCALE_PORT   the platform's domain and port (storscale.localhost, 8800)
  DOCKER_SOCK                        the Docker socket JupyterHub starts notebooks through
                                     (rootless Docker: /run/user/<uid>/docker.sock)
  STORSCALE_K8S_VALUES               up --k8s: more values files for the chart, space-separated
                                     (deploy/kind/https.yaml: HTTPS, after deploy/kind/https.sh)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "storscale:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	dir := fs.String("dir", "", "use the stack in this directory")
	port := fs.Int("port", 0, "the platform's port (up)")
	volumes := fs.Bool("volumes", false, "also delete the platform's data (down)")
	k8s := fs.Bool("k8s", false, "on a kind cluster, with the Helm chart")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cmd == "version" {
		fmt.Println("storscale", version())
		return nil
	}
	if *port != 0 {
		os.Setenv("STORSCALE_PORT", fmt.Sprint(*port))
	}
	st, err := openStack(*dir)
	if err != nil {
		return err
	}
	if *k8s {
		return newKube(st).do(cmd, st, fs.Args(), *volumes)
	}
	switch cmd {
	case "up":
		warnRootless()
		if err := st.compose("up", "--detach", "--wait", "--build"); err != nil {
			return fmt.Errorf("the platform didn't start: see `storscale status` and `storscale logs` (%w)", err)
		}
		fmt.Println()
		printURLs(st)
		return nil
	case "status":
		return st.compose("ps", "--all")
	case "urls":
		printURLs(st)
		return nil
	case "test":
		return runTests(st, fs.Args())
	case "logs":
		return st.compose(append([]string{"logs", "--follow", "--tail", "100"}, fs.Args()...)...)
	case "down":
		if *volumes {
			return st.compose("down", "--volumes", "--remove-orphans")
		}
		return st.compose("down", "--remove-orphans")
	}
	return fmt.Errorf("unknown command %q (see storscale help)", cmd)
}

// runTests runs the suites asked for, or all of them. "shell" runs in a
// browser, in a service of its own; the rest run in the test service.
func runTests(st *stack, suites []string) error {
	var others []string
	shell := len(suites) == 0
	for _, s := range suites {
		if s == "shell" {
			shell = true
		} else {
			others = append(others, s)
		}
	}
	var failed []string
	if shell {
		fmt.Println("=== shell")
		if err := st.compose("run", "--rm", "--tty=false", "test-browser"); err != nil {
			failed = append(failed, "shell")
		}
	}
	if len(suites) == 0 || len(others) > 0 {
		if err := st.compose(append([]string{"run", "--rm", "--tty=false", "test"}, others...)...); err != nil {
			failed = append(failed, "the other suites")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func printURLs(st *stack) {
	base := func(sub string) string {
		host := st.setting("STORSCALE_DOMAIN")
		if sub != "" {
			host = sub + "." + host
		}
		return fmt.Sprintf("http://%s:%s", host, st.setting("STORSCALE_PORT"))
	}
	fmt.Printf("StorScale Platform %s\n\n", version())
	for _, t := range []struct{ name, sub, path string }{
		{"The platform", "", "/"},
		{"Notebooks (JupyterHub)", "", "/notebooks/"},
		{"SQL and dashboards (Superset)", "", "/dashboards/"},
		{"Pipelines (Airflow)", "", "/pipelines/"},
		{"Monitoring (Grafana)", "", "/monitoring/"},
		{"Catalog (OpenMetadata)", "catalog", "/_storscale/launch.html"},
		{"Access policies (Ranger)", "access", "/"},
		{"Sign-in (Keycloak)", "", "/sso/realms/lakehouse/account/"},
		{"S3 (Buckets)", "s3", ""},
	} {
		fmt.Printf("  %-31s %s%s\n", t.name, base(t.sub), t.path)
	}
	fmt.Printf("  %-31s %s\n", "Trino (JDBC, CLI)", "https://localhost:8443")
	fmt.Println("\nSign in as alice (analysts) or bob (engineers); their password is LAKEHOUSE_USER_PASSWORD in")
	fmt.Println(st.dir + "/.env")
}

// warnRootless says when JupyterHub would get the wrong Docker socket.
func warnRootless() {
	if os.Getenv("DOCKER_SOCK") != "" {
		return
	}
	out, err := exec.Command("docker", "info", "--format", "{{.SecurityOptions}}").Output()
	if err == nil && strings.Contains(string(out), "rootless") {
		fmt.Fprintln(os.Stderr, "storscale: Docker is rootless here, so notebooks need DOCKER_SOCK=/run/user/<uid>/docker.sock (the daemon's uid)")
	}
}
