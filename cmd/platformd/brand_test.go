package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDefaultBrandIsStorScales(t *testing.T) {
	k := newFakeKeycloak(t)
	const storscale = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"/>`
	ts := newTestServerWith(t, k, func(c *config) {
		c.BrandName = "StorScale"
		os.WriteFile(filepath.Join(c.WebDir, "favicon.svg"), []byte(storscale), 0o644)
	})
	c, _ := signIn(t, ts, k, map[string]any{"preferred_username": "alice", "groups": []string{"analysts"}}, "/")
	_, body := getSession(t, ts, c)
	if b := body["brand"].(map[string]any); b["name"] != "StorScale" || b["logo"] != "" {
		t.Errorf("brand %v, want StorScale's name and icon", b)
	}
	// Other tools (OpenMetadata) show the logo and icon: StorScale's icon for both.
	for _, path := range []string{"/brand/logo", "/brand/icon"} {
		res, _ := http.Get(ts.URL + path)
		got, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || string(got) != storscale || res.Header.Get("Content-Type") != "image/svg+xml" {
			t.Errorf("%s with none: %d %q %s, want StorScale's icon", path, res.StatusCode, got, res.Header.Get("Content-Type"))
		}
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
		if _, err := loadBrand("Vericast", "", f, ""); err == nil {
			t.Errorf("logo %s: loaded, want an error at start-up", filepath.Base(f))
		}
		if _, err := loadBrand("Vericast", "", "", f); err == nil {
			t.Errorf("icon %s: loaded, want an error at start-up", filepath.Base(f))
		}
	}
}

const index = `<!doctype html><html><head>
<link rel="icon" href="/favicon.svg" type="image/svg+xml" />
<title>StorScale Platform</title>
<script type="module" crossorigin src="/assets/index-x.js"></script>
</head><body><div id="root"></div></body></html>`

// tab gets a page of the app, and says what its tab shows.
func tab(t *testing.T, url string) (title, icon string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	page := string(body)
	if res.StatusCode != http.StatusOK || !strings.Contains(page, `src="/assets/index-x.js"`) {
		t.Fatalf("%s: %d %s, want the app's page", url, res.StatusCode, page)
	}
	title = page[strings.Index(page, "<title>")+7 : strings.Index(page, "</title>")]
	i := strings.Index(page, `<link rel="icon"`)
	return title, page[i : i+strings.Index(page[i:], ">")+1]
}

func TestTheTabShowsTheBrand(t *testing.T) {
	icon := filepath.Join(t.TempDir(), "icon.png")
	os.WriteFile(icon, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o644)
	for name, tc := range map[string]struct {
		change              func(*config)
		wantTitle, wantIcon string
	}{
		"StorScale's": {func(c *config) { c.BrandName = "StorScale" }, "StorScale Platform", `<link rel="icon" href="/favicon.svg" type="image/svg+xml" />`},
		"a deployment's": {func(c *config) { c.BrandName, c.BrandIcon = "Vericast", icon },
			"Vericast Platform", `<link rel="icon" href="/brand/icon" type="image/png" />`},
		"a title of its own": {func(c *config) { c.BrandName, c.BrandTitle = "Vericast", "Data & AI <Lab>" },
			"Data &amp; AI &lt;Lab&gt;", `<link rel="icon" href="/favicon.svg" type="image/svg+xml" />`},
	} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServerWith(t, newFakeKeycloak(t), func(c *config) {
				os.WriteFile(filepath.Join(c.WebDir, "index.html"), []byte(index), 0o644)
				tc.change(c)
			})
			for _, path := range []string{"/", "/p/sales/data", "/index.html"} {
				if title, icon := tab(t, ts.URL+path); title != tc.wantTitle || icon != tc.wantIcon {
					t.Errorf("%s: title %q, icon %s; want %q, %s", path, title, icon, tc.wantTitle, tc.wantIcon)
				}
			}
			res, _ := http.Get(ts.URL + "/brand/icon")
			if strings.Contains(tc.wantIcon, "/brand/icon") && (res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png") {
				t.Errorf("/brand/icon: %d %q, want the deployment's, as image/png", res.StatusCode, res.Header.Get("Content-Type"))
			}
		})
	}
}

func TestTheCatalogsLogoIsFittedToItsSidebar(t *testing.T) {
	logo := filepath.Join(t.TempDir(), "logo.svg")
	wide := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 127.5 21.4"><rect width="127.5" height="21.4"/></svg>`
	os.WriteFile(logo, []byte(wide), 0o644)
	ts := newTestServerWith(t, newFakeKeycloak(t), func(c *config) { c.BrandName, c.BrandLogo = "Vericast", logo })
	res, err := http.Get(ts.URL + "/brand/catalog-logo")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	got := string(body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/svg+xml" ||
		!strings.HasPrefix(got, `<svg xmlns="http://www.w3.org/2000/svg" width="150" height="40" viewBox="0 0 150 40">`) ||
		!strings.Contains(got, `preserveAspectRatio="xMinYMid meet"`) ||
		!strings.Contains(got, "data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString([]byte(wide))) {
		t.Errorf("%d %s: %s", res.StatusCode, res.Header.Get("Content-Type"), got)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src data:") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("its policy: %s", csp)
	}
}
