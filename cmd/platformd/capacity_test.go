package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

func storageProject(limit string) *project.Project {
	p := &project.Project{}
	p.Metadata.Name = "sales"
	p.Spec.Files = &project.Files{Bucket: "sales"}
	if limit != "" {
		p.Spec.Limits = &project.Limits{Storage: limit}
	}
	return p
}

func withBucketSizes(t *testing.T, sizes map[string]int64) *server {
	op := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/buckets/sizes" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(sizes)
	}))
	t.Cleanup(op.Close)
	bucketSizeCache.Lock()
	bucketSizeCache.sizes, bucketSizeCache.at = nil, time.Time{}
	bucketSizeCache.Unlock()
	return &server{cfg: config{OperatorURL: op.URL}}
}

func TestStorageIssuesNearAndFull(t *testing.T) {
	s := withBucketSizes(t, map[string]int64{"sales": 9 << 30})
	got, unknown := s.storageIssues(context.Background(), storageProject("10Gi"), "/p/sales")
	if len(unknown) != 0 || len(got) != 1 || got[0].Key != "storage/sales/near" || !strings.Contains(got[0].Title, "90% full (9.0 GiB of its 10Gi)") {
		t.Fatalf("at 90%%: %+v %v", got, unknown)
	}
	if a := alertOf("sales", got[0], false); a.Severity != "warning" {
		t.Errorf("near: severity %q, want warning", a.Severity)
	}

	s = withBucketSizes(t, map[string]int64{"sales": 10 << 30})
	got, _ = s.storageIssues(context.Background(), storageProject("10Gi"), "/p/sales")
	if len(got) != 2 || got[1].Key != "storage/sales/full" || !strings.Contains(got[1].Title, "new files are refused") {
		t.Fatalf("at 100%%: %+v", got)
	}
	if a := alertOf("sales", got[1], false); a.Severity != "down" {
		t.Errorf("full: severity %q, want down", a.Severity)
	}
}

func TestStorageIssuesUseTheInstallsLimitAndStayQuietBelow(t *testing.T) {
	s := withBucketSizes(t, map[string]int64{"sales": 70 << 30})
	s.cfg.DefaultStorage = "100Gi"
	if got, _ := s.storageIssues(context.Background(), storageProject(""), "/p/sales"); len(got) != 0 {
		t.Fatalf("at 70%% of the install's limit: %+v", got)
	}
	s.cfg.DefaultStorage = "80Gi"
	if got, _ := s.storageIssues(context.Background(), storageProject(""), "/p/sales"); len(got) != 1 {
		t.Fatalf("at 87%% of the install's limit: %+v", got)
	}
	s.cfg.DefaultStorage = ""
	if got, unknown := s.storageIssues(context.Background(), storageProject(""), "/p/sales"); len(got) != 0 || len(unknown) != 0 {
		t.Fatalf("no limit: %+v %v", got, unknown)
	}
}

func TestStorageIssuesSayWhenTheSizesCantBeRead(t *testing.T) {
	s := &server{cfg: config{OperatorURL: "http://127.0.0.1:1"}}
	bucketSizeCache.Lock()
	bucketSizeCache.sizes = nil
	bucketSizeCache.Unlock()
	got, unknown := s.storageIssues(context.Background(), storageProject("10Gi"), "/p/sales")
	if len(got) != 0 || len(unknown) != 1 || !strings.HasPrefix(unknown[0], "the bucket's size: ") {
		t.Fatalf("%+v %v", got, unknown)
	}
}

func TestStorageFillingUpIsTwoAlertsAndEmptyingResolvesEach(t *testing.T) {
	pa := &projectAlerts{issues: map[string]map[string]issue{}, told: map[string]string{}, muted: map[string]time.Time{}, checked: true}
	near := issue{Key: "storage/sales/near", Kind: "storage", Title: "the bucket sales is 90% full"}
	full := issue{Key: "storage/sales/full", Kind: "storage", Title: "the bucket sales is full"}
	pa.update("sales", "h", nil)
	if sent := pa.update("sales", "h", []issue{near}); len(sent) != 1 {
		t.Fatalf("near: %+v", sent)
	}
	if sent := pa.update("sales", "h", []issue{near, full}); len(sent) != 1 || !strings.Contains(sent[0].Title, "is full") {
		t.Fatalf("full: %+v", sent)
	}
	if sent := pa.update("sales", "h", nil); len(sent) != 2 {
		t.Fatalf("emptied: %+v", sent)
	}
}
