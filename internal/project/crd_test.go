package project

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Every field of a project's spec is in the Project definition (CRD):
// Kubernetes drops the fields it lacks from every project, silently (the
// 0.5.0 upgrade lost pipelines.repo so; a bad edit nearly lost files,
// limits and more).
func TestTheProjectDefinitionHasEverySpecField(t *testing.T) {
	data, err := os.ReadFile("../../deploy/crds/project.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd map[string]any
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatal(err)
	}
	dig := func(m any, keys ...any) any {
		for _, k := range keys {
			switch k := k.(type) {
			case string:
				m, _ = m.(map[string]any)[k]
			case int:
				m = m.([]any)[k]
			}
		}
		return m
	}
	schema := dig(crd, "spec", "versions", 0, "schema", "openAPIV3Schema", "properties", "spec")
	var missing []string
	var walk func(path string, typ reflect.Type, node any)
	walk = func(path string, typ reflect.Type, node any) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
			if n, ok := node.(map[string]any); ok && n["items"] != nil {
				node = n["items"]
			}
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		props, _ := dig(node, "properties").(map[string]any)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			child, ok := props[name]
			if !ok {
				missing = append(missing, path+"."+name)
				continue
			}
			walk(path+"."+name, f.Type, child)
		}
	}
	walk("spec", reflect.TypeOf(Spec{}), schema)
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the Project definition (deploy/crds/project.yaml) lacks: %s", strings.Join(missing, ", "))
	}
}
