package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The project as code (use case 7.4): in its repo, beside the pipelines, a
// folder storscale/ with project.yaml (the spec), semantic.yaml (its
// metrics), and targets/<target>.yaml, what differs on one install
// (development, production), laid over project.yaml.

// RepoFolder is the project's folder in its repo.
const RepoFolder = "storscale"

// Compose is a project's spec from its repo's project.yaml, with an
// install's target file (empty: none) laid over it: maps merged, anything
// else (lists, values) replaced. The name must be the project's.
func Compose(name string, projectYAML, targetYAML []byte) (*Project, error) {
	var base, over map[string]any
	if err := yaml.Unmarshal(projectYAML, &base); err != nil {
		return nil, fmt.Errorf("project.yaml: %w", err)
	}
	if base == nil {
		return nil, fmt.Errorf("project.yaml is empty")
	}
	if err := yaml.Unmarshal(targetYAML, &over); err != nil {
		return nil, fmt.Errorf("the target's file: %w", err)
	}
	merged := merge(base, over)
	data, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("project.yaml: %w", err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if p.Metadata.Name != name {
		return nil, fmt.Errorf("project.yaml names project %q, not %q", p.Metadata.Name, name)
	}
	return p, nil
}

func merge(base, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		bm, ok1 := out[k].(map[string]any)
		om, ok2 := v.(map[string]any)
		if ok1 && ok2 {
			out[k] = merge(bm, om)
		} else {
			out[k] = v
		}
	}
	return out
}

// SpecChanges are what a change of spec does, for people to read: members
// added and removed, then the other parts of the spec changed (as
// tables.readers), and the deliveries whose sending changed (approved again).
func SpecChanges(before, after *Project) []string {
	var out []string
	var was, now []Member
	if before != nil {
		was = before.Spec.Members
	}
	if after != nil {
		now = after.Spec.Members
	}
	who := func(m Member) string {
		if m.Group != "" {
			return "group " + m.Group + " as " + m.Role
		}
		return m.User + " as " + m.Role
	}
	for _, m := range now {
		if !contains(was, m) {
			out = append(out, "member added: "+who(m))
		}
	}
	for _, m := range was {
		if !contains(now, m) {
			out = append(out, "member removed: "+who(m))
		}
	}
	if before == nil || after == nil {
		return out
	}
	var a, b map[string]any
	ja, _ := json.Marshal(before.Spec)
	jb, _ := json.Marshal(after.Spec)
	json.Unmarshal(ja, &a)
	json.Unmarshal(jb, &b)
	delete(a, "members")
	delete(b, "members")
	var paths []string
	walkChanges("", a, b, 0, &paths)
	sort.Strings(paths)
	for _, p := range paths {
		out = append(out, "changed: "+p)
	}
	sends := map[string]string{}
	for _, d := range before.Spec.Deliveries {
		sends[d.Name] = d.SendsHash()
	}
	for _, d := range after.Spec.Deliveries {
		if h, ok := sends[d.Name]; !ok || h != d.SendsHash() {
			out = append(out, "delivery "+d.Name+": what it sends changes, so it needs approving again")
		}
	}
	return out
}

func contains(ms []Member, m Member) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}

func walkChanges(prefix string, x, y any, depth int, paths *[]string) {
	mx, okx := x.(map[string]any)
	my, oky := y.(map[string]any)
	if !okx || !oky || depth == 2 {
		if !reflect.DeepEqual(x, y) {
			*paths = append(*paths, prefix)
		}
		return
	}
	keys := map[string]bool{}
	for k := range mx {
		keys[k] = true
	}
	for k := range my {
		keys[k] = true
	}
	for k := range keys {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		walkChanges(p, mx[k], my[k], depth+1, paths)
	}
}

// MetricChanges are the metrics a change of a semantic model (MetricFlow's
// YAML, documents of which some are a metric) adds, removes and changes.
func MetricChanges(before, after string) ([]string, error) {
	was, err := metrics(before)
	if err != nil {
		return nil, fmt.Errorf("the current semantic model: %w", err)
	}
	now, err := metrics(after)
	if err != nil {
		return nil, fmt.Errorf("semantic.yaml: %w", err)
	}
	var out []string
	for _, n := range sortedKeys(now) {
		old, ok := was[n]
		switch {
		case !ok:
			out = append(out, "metric added: "+n)
		case !reflect.DeepEqual(old, now[n]):
			out = append(out, "metric changed: "+n)
		}
	}
	for _, n := range sortedKeys(was) {
		if _, ok := now[n]; !ok {
			out = append(out, "metric removed: "+n)
		}
	}
	return out, nil
}

func metrics(text string) (map[string]any, error) {
	out := map[string]any{}
	dec := yaml.NewDecoder(strings.NewReader(text))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if m, ok := doc["metric"].(map[string]any); ok {
			name, _ := m["name"].(string)
			out[name] = m
		}
	}
}

func sortedKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
