package project

import (
	"fmt"
	"regexp"
)

// A Serving is one of a project's registered models answering as an API
// (use case 15.1, R15.4): its approved version, loaded by a server of the
// project's own (on Kubernetes, a Deployment named for it; in Compose, the
// shared one), which platformd alone reaches, at
// /api/projects/<project>/serving/<name>/predict, for the project's people,
// each call audited.
type Serving struct {
	Name   string `json:"name"`
	Model  string `json:"model"`            // a registered model of the project's MLflow workspace
	Memory string `json:"memory,omitempty"` // its server's, as 1Gi (default the install's)
	// MaxRows is the most rows a call may score (default 1,000).
	MaxRows int `json:"maxRows,omitempty"`
}

const (
	DefaultServingRows = 1000
	MaxServingRows     = 100_000
	MinServingMiB      = 256
	MaxServingMiB      = 16 * 1024
)

// A serving's name is short: its server's Kubernetes name (model-<project>-<name>) is a DNS label.
var servingNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// The model's name goes into its server's settings: plain characters only.
var servedModelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)

// ServingName is the serving's server on Kubernetes: its Deployment, Service and Secret.
func (p *Project) ServingName(s Serving) string { return "model-" + p.Metadata.Name + "-" + s.Name }

// ServingClientID is the serving's Keycloak client: its server signs in with it to read the model.
// (A dot, which no project's name has, keeps one project's from another's: serving.a.x, serving.a-b.x.)
func (p *Project) ServingClientID(s Serving) string {
	return ServingClientPrefix(p.Metadata.Name) + s.Name
}

// ServingClientPrefix begins each of a project's servings' clients.
func ServingClientPrefix(projectName string) string { return "serving." + projectName + "." }

// ServingOf is the project's serving of that name, or nil.
func (p *Project) ServingOf(name string) *Serving {
	for i := range p.Spec.Serving {
		if p.Spec.Serving[i].Name == name {
			return &p.Spec.Serving[i]
		}
	}
	return nil
}

// Rows is the most rows a call may score.
func (s Serving) Rows() int {
	if s.MaxRows > 0 {
		return s.MaxRows
	}
	return DefaultServingRows
}

func (p *Project) checkServing(bad func(format string, a ...any)) {
	seen := map[string]bool{}
	for i, s := range p.Spec.Serving {
		at := fmt.Sprintf("serving %d", i+1)
		if servingNameRE.MatchString(s.Name) {
			at = "serving: " + s.Name
		}
		switch {
		case !servingNameRE.MatchString(s.Name):
			bad("%s: a name is up to 16 lowercase letters, digits and dashes, starting with a letter: %q isn't", at, s.Name)
		case seen[s.Name]:
			bad("serving: two named %s", s.Name)
		case len(p.ServingName(s)) > 63:
			bad("%s: model-%s-%s is its server's name, longer than 63: a shorter name", at, p.Metadata.Name, s.Name)
		}
		seen[s.Name] = true
		if !servedModelRE.MatchString(s.Model) {
			bad("%s: model %q: the registered model it serves, by its name (letters, digits, _ . -); its approved version is served", at, s.Model)
		}
		if s.Memory != "" {
			if mi, ok := MiB(s.Memory); !ok || mi < MinServingMiB || mi > MaxServingMiB {
				bad("%s: memory %q: between %dMi and %dGi, as 1Gi", at, s.Memory, MinServingMiB, MaxServingMiB/1024)
			}
		}
		if s.MaxRows < 0 || s.MaxRows > MaxServingRows {
			bad("%s: maxRows %d: up to %d a call", at, s.MaxRows, MaxServingRows)
		}
	}
}
