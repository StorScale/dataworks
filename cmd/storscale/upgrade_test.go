package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOlderKnowsReleasesApart(t *testing.T) {
	for _, tc := range []struct {
		a, b      string
		older, ok bool
	}{
		{"0.6.0", "0.7.0", true, true},
		{"0.7.0", "0.6.0", false, true},
		{"0.10.0", "0.9.1", false, true}, // as numbers, not text
		{"v0.6.0", "0.6.0", false, true},
		{"1.0.0-rc.1", "1.0.0", true, true},
		{"1.0.0", "1.0.0-rc.1", false, true},
		{"dev", "0.6.0", false, false},
	} {
		if older, ok := older(tc.a, tc.b); older != tc.older || ok != tc.ok {
			t.Errorf("older(%s, %s) = %v, %v; want %v, %v", tc.a, tc.b, older, ok, tc.older, tc.ok)
		}
	}
}

func TestMissingImagesAreTheOnesNotPublishedAtTheTag(t *testing.T) {
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if !strings.HasPrefix(r.URL.Query().Get("scope"), "repository:storscale/") {
				t.Errorf("scope %q", r.URL.Query().Get("scope"))
			}
			w.Write([]byte(`{"token":"anonymous"}`))
		case r.Method == "HEAD" && r.Header.Get("Authorization") == "Bearer anonymous":
			// Every image but the notebook's is published at 0.7.0.
			if strings.HasSuffix(r.URL.Path, "/manifests/0.7.0") && !strings.Contains(r.URL.Path, "platform-notebook") {
				return
			}
			http.NotFound(w, r)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer reg.Close()
	got, err := missingImages(context.Background(), reg.Client(), reg.URL, "0.7.0")
	if err != nil || len(got) != 1 || got[0] != "platform-notebook" {
		t.Errorf("missingImages = %v, %v; want the notebook's", got, err)
	}
	if got, err := missingImages(context.Background(), reg.Client(), reg.URL, "0.8.0"); err != nil || len(got) != len(images) {
		t.Errorf("an unpublished release: %v, %v; want every image", got, err)
	}
}

// The images upgrade checks are the chart's (values.yaml, images.*).
func TestTheImagesCheckedAreTheCharts(t *testing.T) {
	data, err := os.ReadFile("../../deploy/helm/storscale-platform/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?ms)^images:\n(.*?)^\S`).FindStringSubmatch(string(data))
	if block == nil {
		t.Fatal("no images: in values.yaml")
	}
	var chart []string
	for _, m := range regexp.MustCompile(`(?m)^  \w+: (platform[\w-]*)\s*$`).FindAllStringSubmatch(block[1], -1) {
		chart = append(chart, m[1])
	}
	if strings.Join(chart, " ") != strings.Join(images, " ") {
		t.Errorf("the chart's images: %v\nupgrade checks: %v", chart, images)
	}
}

func TestTheReleaseIsReadAsEitherHelmWritesIt(t *testing.T) {
	for _, js := range []string{
		`[{"name":"storscale","revision":"13","status":"deployed","chart":"storscale-platform-0.6.0","app_version":"0.6.0"}]`,
		`[{"name":"storscale","revision":13,"status":"deployed","chart":"storscale-platform-0.6.0","app_version":"0.6.0"}]`,
	} {
		var rs []installed
		if err := json.Unmarshal([]byte(js), &rs); err != nil || rs[0].Revision != "13" || rs[0].version() != "0.6.0" {
			t.Errorf("%s: %+v %v", js, rs, err)
		}
	}
}
