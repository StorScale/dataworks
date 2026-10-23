package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// claims is a fake Kubernetes API holding Buckets' drives' claims.
type claims struct {
	pvcs    map[string]map[string]any // name -> claim
	patches []string                  // "name storage"
	refuse  bool                      // as a storage class without expansion
}

func pvc(name, pool, requested, capacity string, pending bool) map[string]any {
	st := map[string]any{"capacity": map[string]any{"storage": capacity}}
	if pending {
		st["conditions"] = []any{map[string]any{"type": "FileSystemResizePending", "status": "True"}}
	}
	return map[string]any{
		"metadata": map[string]any{"name": name, "labels": map[string]any{"buckets.io/cluster": "buckets", "buckets.io/pool": pool}},
		"spec":     map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": requested}}},
		"status":   st,
	}
}

func (c *claims) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const base = "/api/v1/namespaces/storscale/persistentvolumeclaims"
	switch {
	case r.Method == "GET" && r.URL.Path == base:
		if r.URL.Query().Get("labelSelector") != "buckets.io/cluster=buckets" {
			http.Error(w, `{"message": "wrong selector"}`, http.StatusBadRequest)
			return
		}
		var items []any
		for _, p := range c.pvcs {
			items = append(items, p)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, base+"/"):
		if r.Header.Get("Content-Type") != "application/merge-patch+json" {
			http.Error(w, `{"message": "not a merge patch"}`, http.StatusUnsupportedMediaType)
			return
		}
		if c.refuse {
			http.Error(w, `{"message": "only dynamically provisioned pvc can be resized and the storageclass that provisions the pvc must support resize"}`, http.StatusForbidden)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, base+"/")
		body, _ := io.ReadAll(r.Body)
		var p struct {
			Spec struct {
				Resources struct {
					Requests map[string]string `json:"requests"`
				} `json:"resources"`
			} `json:"spec"`
		}
		json.Unmarshal(body, &p)
		c.patches = append(c.patches, name+" "+p.Spec.Resources.Requests["storage"])
	default:
		http.Error(w, `{"message": "unexpected"}`, http.StatusNotFound)
	}
}

func drivesWith(t *testing.T, c *claims, sizes map[string]string) *Drives {
	ts := httptest.NewServer(c)
	t.Cleanup(ts.Close)
	k := &Kube{URL: ts.URL, Namespace: "storscale", HTTP: ts.Client(), Token: func() (string, error) { return "t", nil }}
	return &Drives{Kube: k, Cluster: "buckets", Sizes: sizes}
}

func TestDrivesGrowEveryDriveOfAPool(t *testing.T) {
	c := &claims{pvcs: map[string]map[string]any{}}
	for _, n := range []string{"data0", "data1", "data2", "data3"} {
		c.pvcs[n+"-buckets-pool-0-0"] = pvc(n+"-buckets-pool-0-0", "pool-0", "20Gi", "20Gi", false)
	}
	c.pvcs["data0-buckets-pool-1-0"] = pvc("data0-buckets-pool-1-0", "pool-1", "100Gi", "100Gi", false)
	d := drivesWith(t, c, map[string]string{"pool-0": "40Gi", "pool-1": "100Gi"})
	grown, err := d.Grow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(grown) != 4 || len(c.patches) != 4 {
		t.Fatalf("grown %v, patches %v: want the four drives of pool-0", grown, c.patches)
	}
	for _, p := range c.patches {
		if !strings.HasPrefix(p, "data") || !strings.HasSuffix(p, "-buckets-pool-0-0 40Gi") {
			t.Errorf("patch %q: want a pool-0 drive to 40Gi", p)
		}
	}
	if grown["data0-buckets-pool-0-0"] != "20Gi -> 40Gi" {
		t.Errorf("grown %v", grown)
	}
}

func TestDrivesNeverShrinkAndLeaveUnsizedPools(t *testing.T) {
	c := &claims{pvcs: map[string]map[string]any{
		"data0-buckets-pool-0-0": pvc("data0-buckets-pool-0-0", "pool-0", "40Gi", "40Gi", false),
		"data0-buckets-pool-1-0": pvc("data0-buckets-pool-1-0", "pool-1", "10Gi", "10Gi", false),
	}}
	d := drivesWith(t, c, map[string]string{"pool-0": "20Gi"}) // smaller; pool-1 not sized
	grown, err := d.Grow(context.Background())
	if err != nil || len(grown) != 0 || len(c.patches) != 0 {
		t.Fatalf("grown %v, patches %v, err %v: want nothing changed", grown, c.patches, err)
	}
	if !strings.Contains(d.said["data0-buckets-pool-0-0"], "don't shrink") {
		t.Errorf("said %q", d.said["data0-buckets-pool-0-0"])
	}
}

func TestDrivesSayWhatTheStorageClassRefuses(t *testing.T) {
	c := &claims{refuse: true, pvcs: map[string]map[string]any{
		"data0-buckets-pool-0-0": pvc("data0-buckets-pool-0-0", "pool-0", "20Gi", "20Gi", false),
	}}
	d := drivesWith(t, c, map[string]string{"pool-0": "40Gi"})
	if _, err := d.Grow(context.Background()); err == nil || !strings.Contains(err.Error(), "must support resize") {
		t.Fatalf("err %v: want the API's refusal", err)
	}
	if !strings.Contains(d.said["data0-buckets-pool-0-0"], "not grown to 40Gi") {
		t.Errorf("said %q", d.said["data0-buckets-pool-0-0"])
	}
}

func TestDrivesWaitingForTheirServer(t *testing.T) {
	c := &claims{pvcs: map[string]map[string]any{
		"data0-buckets-pool-0-0": pvc("data0-buckets-pool-0-0", "pool-0", "40Gi", "20Gi", true),
	}}
	d := drivesWith(t, c, map[string]string{"pool-0": "40Gi"})
	if _, err := d.Grow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.said["data0-buckets-pool-0-0"], "when its server next starts") {
		t.Errorf("said %q", d.said["data0-buckets-pool-0-0"])
	}
}

func TestBytes(t *testing.T) {
	for q, want := range map[string]int64{"20Gi": 20 << 30, "1Ti": 1 << 40, "500M": 500e6, "1073741824": 1 << 30} {
		if got, ok := Bytes(q); !ok || got != want {
			t.Errorf("Bytes(%q) = %d, %v; want %d", q, got, ok, want)
		}
	}
	for _, q := range []string{"", "20 Gi", "1.5Gi", "-1Gi", "20GB"} {
		if _, ok := Bytes(q); ok {
			t.Errorf("Bytes(%q) ok; want refused", q)
		}
	}
}
