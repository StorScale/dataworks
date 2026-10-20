package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheAPI(t *testing.T) {
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"message": "401 Unauthorized"})
			return
		}
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery+" "+string(b))
		if r.Method == "GET" {
			json.NewEncoder(w).Encode([]MergeRequest{{IID: 7, SHA: "abc", SourceBranch: "fast-orders", TargetBranch: "main"}})
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()
	ctx := context.Background()
	c := New(ts.URL+"/", "tok", "sales/dags")
	mrs, err := c.OpenMergeRequests(ctx, "main")
	if err != nil || len(mrs) != 1 || mrs[0].IID != 7 || mrs[0].SHA != "abc" {
		t.Fatalf("merge requests: %v %v", mrs, err)
	}
	if err := c.Status(ctx, "abc", "success", "storscale/dbt", "2 models built", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Note(ctx, 7, "rows"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"GET /api/v4/projects/sales%2Fdags/merge_requests?", "POST /api/v4/projects/sales%2Fdags/statuses/abc?",
		"POST /api/v4/projects/sales%2Fdags/merge_requests/7/notes?"} {
		if !strings.HasPrefix(seen[i], want) {
			t.Errorf("call %d: %s, want %s...", i, seen[i], want)
		}
	}
	if !strings.Contains(seen[0], "state=opened") || !strings.Contains(seen[0], "target_branch=main") {
		t.Errorf("list: %s", seen[0])
	}
	bad := New(ts.URL, "wrong", "sales/dags")
	if _, err := bad.OpenMergeRequests(ctx, "main"); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("a refused token: %v", err)
	}
	if got := ProjectPath("https://gitlab.example.com/sales/dags.git"); got != "sales/dags" {
		t.Errorf("ProjectPath: %s", got)
	}
}

func TestBaseOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://gitlab.com":                                  "https://gitlab.com",
		"https://gitlab.example.com/":                         "https://gitlab.example.com",
		"https://gitlab.example.com/gitlab":                   "https://gitlab.example.com/gitlab", // a relative root
		"https://gitlab.com/vericast/group/sales-data.git":    "https://gitlab.com",                // the repo's address, pasted
		"http://gitlab.example.com/data/sales-pipelines.git/": "http://gitlab.example.com",
	} {
		if got := BaseOf(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	if ProjectPath("https://gitlab.com/vericast/group/sales-data.git") != "vericast/group/sales-data" {
		t.Error(ProjectPath("https://gitlab.com/vericast/group/sales-data.git"))
	}
}
