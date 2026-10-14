package main

import (
	"bytes"
	"fmt"
	"html"
	"net/http"
	"os"
	"regexp"
)

// The platform's brand. In the top bar's corner: StorScale's icon and name,
// or a logo of the deployment's own (BRAND_LOGO) in their place, with
// BRAND_NAME as its text for screen readers. In the browser's tab: the
// title (BRAND_TITLE, by default "<name> Platform") and an icon
// (BRAND_ICON, square, as a tab shows it), or StorScale's. Images are SVG,
// PNG, JPEG, GIF, WebP or ICO.

type brand struct {
	name, title        string
	logo, icon         []byte // nil: StorScale's
	logoType, iconType string
}

func loadBrand(name, title, logoFile, iconFile string) (brand, error) {
	if name == "" {
		name = "StorScale"
	}
	b := brand{name: name, title: title}
	if b.title == "" {
		b.title = name + " Platform"
	}
	var err error
	if b.logo, b.logoType, err = loadImage("BRAND_LOGO", logoFile); err != nil {
		return b, err
	}
	b.icon, b.iconType, err = loadImage("BRAND_ICON", iconFile)
	return b, err
}

func loadImage(setting, file string) ([]byte, string, error) {
	if file == "" {
		return nil, "", nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", setting, err)
	}
	t := imageType(data)
	if t == "" {
		return nil, "", fmt.Errorf("%s %s isn't an SVG, PNG, JPEG, GIF, WebP or ICO image", setting, file)
	}
	return data, t, nil
}

// imageType is an image's media type, or "" for anything else.
func imageType(data []byte) string {
	head := bytes.TrimLeft(data[:min(len(data), 1024)], "\ufeff \t\r\n")
	if bytes.HasPrefix(head, []byte("<svg")) || (bytes.HasPrefix(head, []byte("<?xml")) && bytes.Contains(head, []byte("<svg"))) {
		return "image/svg+xml"
	}
	switch t := http.DetectContentType(data); t {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/x-icon":
		return t
	}
	return ""
}

// session is what /api/session says of it: the logo's address, or "".
func (b brand) session() map[string]string {
	logo := ""
	if b.logo != nil {
		logo = "/brand/logo"
	}
	return map[string]string{"name": b.name, "logo": logo}
}

var (
	pageTitle = regexp.MustCompile(`<title>[^<]*</title>`)
	pageIcon  = regexp.MustCompile(`<link rel="icon"[^>]*>`)
)

// page is the web app's index.html with the brand's title and icon, so the
// tab shows them from the first moment, on every page of the app.
func (b brand) page(index []byte) []byte {
	index = pageTitle.ReplaceAllLiteral(index, []byte("<title>"+html.EscapeString(b.title)+"</title>"))
	if b.icon != nil {
		index = pageIcon.ReplaceAllLiteral(index, []byte(`<link rel="icon" href="/brand/icon" type="`+b.iconType+`" />`))
	}
	return index
}

// handleBrandLogo and handleBrandIcon serve the images to anyone: they're
// on every page, the sign-in's too.
func (s *server) handleBrandLogo(w http.ResponseWriter, r *http.Request) {
	serveImage(w, r, s.brand.logo, s.brand.logoType)
}

func (s *server) handleBrandIcon(w http.ResponseWriter, r *http.Request) {
	serveImage(w, r, s.brand.icon, s.brand.iconType)
}

func serveImage(w http.ResponseWriter, r *http.Request, data []byte, typ string) {
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "public, max-age=300")
	// An SVG opened on its own runs nothing it may hold.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Write(data)
}
