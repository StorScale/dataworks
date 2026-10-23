package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

// A project's bucket nearing its size limit (its limits.storage, else the
// install's LIMITS_PROJECT_STORAGE), which Buckets enforces as the bucket's
// quota: past it, every new file is refused. The project's people hear of it
// at 80%, and again when it's full. Buckets as a whole nearing full is the
// administrators' (the monitoring rules BucketsCapacityLow and
// BucketsCapacityCritical, which the Health page forwards).

const storageNear = 0.8

// bucketSizeCache holds the buckets' sizes, as the operator counts them
// (Buckets' usage scan, itself minutes behind), for ten minutes.
var bucketSizeCache struct {
	sync.Mutex
	at    time.Time
	sizes map[string]int64
}

func (s *server) bucketSizes(ctx context.Context) (map[string]int64, error) {
	bucketSizeCache.Lock()
	defer bucketSizeCache.Unlock()
	if bucketSizeCache.sizes != nil && time.Since(bucketSizeCache.at) < 10*time.Minute {
		return bucketSizeCache.sizes, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", s.cfg.OperatorURL+"/buckets/sizes", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the operator answered HTTP %d", resp.StatusCode)
	}
	sizes := map[string]int64{}
	if err := json.NewDecoder(resp.Body).Decode(&sizes); err != nil {
		return nil, err
	}
	bucketSizeCache.at, bucketSizeCache.sizes = time.Now(), sizes
	return sizes, nil
}

// storageIssues are a project's bucket near or at its size limit; and what couldn't be looked at.
func (s *server) storageIssues(ctx context.Context, p *project.Project, link string) ([]issue, []string) {
	if p.Spec.Files == nil || p.Spec.Files.Bucket == "" {
		return nil, nil
	}
	limit := s.cfg.DefaultStorage
	if l := p.Spec.Limits; l != nil && l.Storage != "" {
		limit = l.Storage
	}
	max := project.StorageBytes(limit)
	if max <= 0 {
		return nil, nil
	}
	sizes, err := s.bucketSizes(ctx)
	if err != nil {
		return nil, []string{"the bucket's size: " + err.Error()}
	}
	b := p.Spec.Files.Bucket
	used := sizes[b]
	share := float64(used) / float64(max)
	if share < storageNear {
		return nil, nil
	}
	what := fmt.Sprintf("%s of its %s", gib(used), limit)
	// The near one stays while it's full, so filling up is one alert, then another, and emptying says each was resolved.
	out := []issue{{Key: "storage/" + b + "/near", Kind: "storage", Link: link + "/usage",
		Title:  fmt.Sprintf("the bucket %s is %.0f%% full (%s)", b, share*100, what),
		Detail: "Buckets refuses new files once it's full: remove what isn't needed, or raise the project's storage limit (Edit, Limits)."}}
	if share >= 1 {
		out = append(out, issue{Key: "storage/" + b + "/full", Kind: "storage", Link: link + "/usage",
			Title:  fmt.Sprintf("the bucket %s is full (%s): new files are refused", b, what),
			Detail: "Remove what isn't needed, or raise the project's storage limit (Edit, Limits)."})
	}
	return out, nil
}

// gib is a size in GiB, as 12.3 GiB.
func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }
