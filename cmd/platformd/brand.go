package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
)

// The platform's brand, in the top bar's corner: StorScale's icon and name,
// or a logo of the deployment's own (BRAND_LOGO, an image file: SVG, PNG,
// JPEG, GIF or WebP) in their place, with BRAND_NAME as its text for
// screen readers.

type brand struct {
	name     string
	logo     []byte // nil: StorScale's icon and the name
	logoType string
}

func loadBrand(name, logoFile string) (brand, error) {
	b := brand{name: name}
	if logoFile == "" {
		return b, nil
	}
	data, err := os.ReadFile(logoFile)
	if err != nil {
		return b, fmt.Errorf("BRAND_LOGO: %w", err)
	}
	if b.logoType = imageType(data); b.logoType == "" {
		return b, fmt.Errorf("BRAND_LOGO %s isn't an SVG, PNG, JPEG, GIF or WebP image", logoFile)
	}
	b.logo = data
	return b, nil
}

// imageType is an image's media type, or "" for anything else.
func imageType(data []byte) string {
	head := bytes.TrimLeft(data[:min(len(data), 1024)], "\ufeff \t\r\n")
	if bytes.HasPrefix(head, []byte("<svg")) || (bytes.HasPrefix(head, []byte("<?xml")) && bytes.Contains(head, []byte("<svg"))) {
		return "image/svg+xml"
	}
	switch t := http.DetectContentType(data); t {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
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

// handleBrandLogo serves the logo to anyone: it's an image on every page.
func (s *server) handleBrandLogo(w http.ResponseWriter, r *http.Request) {
	if s.brand.logo == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", s.brand.logoType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	// An SVG opened on its own runs nothing it may hold.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Write(s.brand.logo)
}
