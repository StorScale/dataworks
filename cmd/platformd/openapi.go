package main

import (
	_ "embed"
	"net/http"
)

// The platform's API reference (use case 14.3, R14.7): an OpenAPI document
// of platformd's routes, made from server.go and each handler's comment by
// TestTheAPIReferenceIsCurrent (STORSCALE_UPDATE_OPENAPI=1 writes it), so a
// route added without the document fails the build. Served at
// /api/openapi.json, read by the web app's API page and the Python package.
//
//go:embed openapi.json
var openAPI []byte

// handleOpenAPI is the API reference: this document, as OpenAPI 3.1.
func (s *server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(openAPI)
}
