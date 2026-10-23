package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"sync"
)

// Drives grows Buckets' drives to the sizes the chart gives each pool
// (buckets.storage, buckets.pools[].storage). The Buckets operator makes a
// pool's drives from its StatefulSet's volume claim templates, which
// Kubernetes never lets change, so the chart keeps those as they were made
// and the operator here asks for more on each drive's claim instead; the
// storage class must allow expansion. A pool's drives grow together: an
// erasure set holds only as much as its smallest drive. A drive is never
// shrunk (Kubernetes can't), only said to be bigger than asked.
type Drives struct {
	Kube    *Kube
	Cluster string            // the BucketsCluster's name
	Sizes   map[string]string // pool (pool-0, pool-1, ...) -> each drive's size: 40Gi
	Log     *slog.Logger

	mu   sync.Mutex
	said map[string]string // claim -> what was last logged of it, to say each thing once
}

// A drive is one of Buckets' claims, as Grow found it.
type drive struct {
	Name      string
	Pool      string
	Requested string // what the claim asks for
	Capacity  string // what it has
	Resizing  bool   // its file system waits to grow (FileSystemResizePending)
}

func (d *Drives) drives(ctx context.Context) ([]drive, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Resources struct {
					Requests map[string]string `json:"requests"`
				} `json:"resources"`
			} `json:"spec"`
			Status struct {
				Capacity   map[string]string `json:"capacity"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	sel := url.QueryEscape("buckets.io/cluster=" + d.Cluster)
	if _, err := d.Kube.do(ctx, "GET", d.Kube.path("persistentvolumeclaims", "")+"?labelSelector="+sel, "", nil, &list); err != nil {
		return nil, err
	}
	var out []drive
	for _, it := range list.Items {
		dr := drive{Name: it.Metadata.Name, Pool: it.Metadata.Labels["buckets.io/pool"],
			Requested: it.Spec.Resources.Requests["storage"], Capacity: it.Status.Capacity["storage"]}
		for _, c := range it.Status.Conditions {
			if c.Type == "FileSystemResizePending" && c.Status == "True" {
				dr.Resizing = true
			}
		}
		out = append(out, dr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Grow asks for each pool's size on every drive of it that asks for less;
// what it changed, by claim.
func (d *Drives) Grow(ctx context.Context) (map[string]string, error) {
	drives, err := d.drives(ctx)
	if err != nil {
		return nil, err
	}
	grown := map[string]string{}
	var failed error
	for _, dr := range drives {
		want, ok := d.Sizes[dr.Pool]
		if !ok {
			continue // a pool the chart doesn't size: as it was made
		}
		wb, ok1 := Bytes(want)
		has, ok2 := Bytes(dr.Requested)
		switch {
		case !ok1 || !ok2:
			d.say(dr.Name, "warn", fmt.Sprintf("can't compare sizes %q and %q", want, dr.Requested))
		case wb < has:
			d.say(dr.Name, "warn", fmt.Sprintf("asks for %s, more than its pool's %s: drives don't shrink", dr.Requested, want))
		case wb > has:
			patch := map[string]any{"spec": map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": want}}}}
			if _, err := d.Kube.do(ctx, "PATCH", d.Kube.path("persistentvolumeclaims", dr.Name), "application/merge-patch+json", patch, nil); err != nil {
				d.say(dr.Name, "error", "not grown to "+want+": "+err.Error())
				failed = err
				continue
			}
			grown[dr.Name] = dr.Requested + " -> " + want
			d.say(dr.Name, "info", "grown from "+dr.Requested+" to "+want)
		case dr.Resizing:
			d.say(dr.Name, "info", "its file system grows to "+want+" when its server next starts")
		case dr.Capacity != "" && dr.Capacity != dr.Requested:
			d.say(dr.Name, "info", "growing to "+want+" (has "+dr.Capacity+")")
		default:
			d.say(dr.Name, "info", "at "+want)
		}
	}
	return grown, failed
}

// say logs a drive's state when it's changed since last said.
func (d *Drives) say(claim, level, msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.said == nil {
		d.said = map[string]string{}
	}
	if d.said[claim] == msg || d.Log == nil {
		d.said[claim] = msg
		return
	}
	d.said[claim] = msg
	switch level {
	case "error":
		d.Log.Error("buckets drive", "claim", claim, "state", msg)
	case "warn":
		d.Log.Warn("buckets drive", "claim", claim, "state", msg)
	default:
		d.Log.Info("buckets drive", "claim", claim, "state", msg)
	}
}

var quantityRE = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti|Pi|k|M|G|T|P)?$`)

// Bytes is a Kubernetes storage quantity in bytes (20Gi, 1Ti, 500M, 1073741824).
func Bytes(q string) (int64, bool) {
	m := quantityRE.FindStringSubmatch(q)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]int64{"": 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50,
		"k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15}[m[2]]
	return n * mult, true
}
