package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDefaultBrandIsStorScales(t *testing.T) {
	k := newFakeKeycloak(t)
	ts := newTestServerWith(t, k, func(c *config) { c.BrandName = "StorScale" })
	c, _ := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}, "/")
	_, body := getSession(t, ts, c)
	if b := body["brand"].(map[string]any); b["name"] != "StorScale" || b["logo"] != "" {
		t.Errorf("brand %v, want StorScale's name and icon", b)
	}
	if res, _ := http.Get(ts.URL + "/brand/logo"); res.StatusCode != http.StatusNotFound {
		t.Errorf("/brand/logo with none: %d, want 404", res.StatusCode)
	}
}

func TestADeploymentsOwnLogo(t *testing.T) {
	for name, tc := range map[string]struct {
		data     string
		wantType string
	}{
		"svg":      {`<?xml version="1.0"?>` + "\n" + `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`, "image/svg+xml"},
		"bare svg": {`<svg xmlns="http://www.w3.org/2000/svg"/>`, "image/svg+xml"},
		"png":      {"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", "image/png"},
	} {
		t.Run(name, func(t *testing.T) {
			logo := filepath.Join(t.TempDir(), "logo")
			os.WriteFile(logo, []byte(tc.data), 0o644)
			k := newFakeKeycloak(t)
			ts := newTestServerWith(t, k, func(c *config) { c.BrandName, c.BrandLogo = "Vericast", logo })
			c, _ := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}, "/")
			_, body := getSession(t, ts, c)
			if b := body["brand"].(map[string]any); b["name"] != "Vericast" || b["logo"] != "/brand/logo" {
				t.Errorf("brand %v, want the logo, named Vericast", b)
			}
			res, err := http.Get(ts.URL + "/brand/logo") // no session: it's on every page
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != tc.wantType || string(got) != tc.data {
				t.Errorf("/brand/logo: %d %q, want the file as %s", res.StatusCode, res.Header.Get("Content-Type"), tc.wantType)
			}
			if csp := res.Header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'none'") {
				t.Errorf("CSP %q: an SVG opened on its own must run nothing", csp)
			}
		})
	}
}

func TestALogoThatIsntAnImageIsRefused(t *testing.T) {
	dir := t.TempDir()
	html := filepath.Join(dir, "logo.html")
	os.WriteFile(html, []byte("<html><script>alert(1)</script></html>"), 0o644)
	for _, f := range []string{html, filepath.Join(dir, "missing.svg")} {
		if _, err := loadBrand("Vericast", f); err == nil {
			t.Errorf("%s: loaded, want an error at start-up", filepath.Base(f))
		}
	}
}
