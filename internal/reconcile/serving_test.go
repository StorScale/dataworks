package reconcile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/StorScale/dataworks/internal/project"
)

// servingTemplate is the chart's serving template, cut down to its placeholders.
const servingTemplate = `{
  "deployment": {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "@SERVER@",
    "labels": {"app.kubernetes.io/name": "model-serving", "platform.storscale.io/project": "@PROJECT@"}},
    "spec": {"template": {"spec": {"containers": [{"name": "server", "env": [
      {"name": "SERVING_CLIENT_ID", "value": "@CLIENT@"},
      {"name": "STORSCALE_SERVING", "value": "{\"project\": \"@PROJECT@\", \"serving\": {\"name\": \"@SERVING@\", \"model\": \"@MODEL@\", \"maxRows\": @MAX_ROWS@}}"}],
      "resources": {"limits": {"memory": "@MEMORY@"}, "requests": {"memory": "@REQUEST@"}}}]}}}},
  "service": {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "@SERVER@"}}
}`

func TestServingObjects(t *testing.T) {
	var tpl Template
	if err := json.Unmarshal([]byte(servingTemplate), &tpl); err != nil {
		t.Fatal(err)
	}
	p, err := project.Parse([]byte(`{"metadata": {"name": "sales"}, "spec": {"members": [{"group": "engineers", "role": "editor"}],
		"serving": [{"name": "churn", "model": "churn.v2", "maxRows": 50}, {"name": "big", "model": "churn", "memory": "3Gi"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	ps := &ProjectServings{Template: tpl, DefaultMemory: "1Gi"}
	d, svc := ps.objects(p, p.Spec.Serving[0])
	out, _ := json.Marshal(d)
	got := string(out)
	for _, want := range []string{`"name":"model-sales-churn"`, `"value":"serving.sales.churn"`, `"memory":"1024Mi"`, `"memory":"512Mi"`} {
		if !strings.Contains(got, want) {
			t.Errorf("deployment lacks %s: %s", want, got)
		}
	}
	env := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)
	var listed ProjectServing
	if err := json.Unmarshal([]byte(env[1].(map[string]any)["value"].(string)), &listed); err != nil || listed.Serving.Model != "churn.v2" || listed.Serving.MaxRows != 50 {
		t.Errorf("STORSCALE_SERVING: %v %+v", err, listed)
	}
	if svc["metadata"].(map[string]any)["name"] != "model-sales-churn" {
		t.Errorf("service: %v", svc)
	}
	d, _ = ps.objects(p, p.Spec.Serving[1])
	if out, _ := json.Marshal(d); !strings.Contains(string(out), `"memory":"3072Mi"`) {
		t.Errorf("its own memory: %s", out)
	}
}
