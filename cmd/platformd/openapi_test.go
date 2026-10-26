package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/doc"
	"go/parser"
	gotoken "go/token"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/StorScale/storscale-platform/internal/project"
)

// The API reference is made from the routes: each "METHOD /api/..." route in
// server.go, its comment on the route's line, and its handler's doc comment.
func TestTheAPIReferenceIsCurrent(t *testing.T) {
	made, err := apiReference(".")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("STORSCALE_UPDATE_OPENAPI") == "1" {
		if err := os.WriteFile("openapi.json", made, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(bytes.TrimSpace(made), bytes.TrimSpace(openAPI)) {
		t.Fatal("openapi.json is behind the routes: go test ./cmd/platformd -run TestTheAPIReferenceIsCurrent with STORSCALE_UPDATE_OPENAPI=1 writes it")
	}
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	json.Unmarshal(made, &spec)
	if spec.Paths["/api/projects/{name}/usage"]["get"] == nil || spec.Paths["/api/projects/{name}/knowledge/{bank}/search"]["post"] == nil {
		t.Errorf("routes missing from the reference")
	}
}

var pathParamName = regexp.MustCompile(`\{([^}.]+)(\.\.\.)?\}`)

func apiReference(dir string) ([]byte, error) {
	fset := gotoken.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	pkg := pkgs["main"]
	docs := map[string]string{} // handler -> its doc comment
	for _, typ := range doc.New(pkg, "platformd", doc.AllDecls|doc.PreserveAST).Types {
		for _, m := range typ.Methods {
			docs[m.Name] = strings.TrimSpace(m.Doc)
		}
	}
	src, err := os.ReadFile(filepath.Join(dir, "server.go"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(src), "\n")
	paths := map[string]map[string]any{}
	ast.Inspect(pkg.Files[filepath.Join(dir, "server.go")], func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		pattern, _ := strconv.Unquote(lit.Value)
		method, path, ok := strings.Cut(pattern, " ")
		if !ok || !strings.HasPrefix(path, "/api/") {
			return true
		}
		handler := ""
		if h, ok := call.Args[1].(*ast.SelectorExpr); ok {
			handler = h.Sel.Name
		}
		line := lines[fset.Position(call.Pos()).Line-1]
		note := ""
		if i := strings.Index(line, "// "); i >= 0 {
			note = strings.TrimSpace(line[i+3:])
		}
		description := docs[handler]
		summary := firstSentence(description, handler)
		if note != "" {
			description = strings.TrimSpace(note + "\n\n" + description)
		}
		op := map[string]any{"operationId": operationID(method, path), "summary": summary, "tags": []string{apiTag(path)},
			"responses": map[string]any{"200": map[string]any{"description": "As JSON"}, "4XX": map[string]any{"description": "{\"error\": ..., \"request\": ...}: what's wrong, in words"}}}
		if description != "" {
			op["description"] = description
		}
		var params []any
		for _, m := range pathParamName.FindAllStringSubmatch(path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]string{"type": "string"}})
		}
		if params != nil {
			op["parameters"] = params
		}
		if method == "POST" || method == "PUT" || method == "PATCH" {
			op["requestBody"] = map[string]any{"required": false, "content": map[string]any{"application/json": map[string]any{"schema": map[string]string{"type": "object"}}}}
		}
		openPath := pathParamName.ReplaceAllString(path, "{$1}")
		if paths[openPath] == nil {
			paths[openPath] = map[string]any{}
		}
		paths[openPath][strings.ToLower(method)] = op
		return true
	})
	var tags []string
	seen := map[string]bool{}
	for _, ops := range paths {
		for _, op := range ops {
			if tag := op.(map[string]any)["tags"].([]string)[0]; !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	sort.Strings(tags)
	var tagList []any
	for _, t := range tags {
		tagList = append(tagList, map[string]string{"name": t})
	}
	spec := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{"title": "StorScale DataWorks API", "version": "1",
			"description": "platformd's API, as the web app, the MCP server and the Python package (`storscale`) use it. Sign in with a Keycloak token of the realm `lakehouse` as `Authorization: Bearer <token>` (a person's, or a service account's), or with the web app's session, whose changes also send `X-Platform-Request: 1`. Every call runs as whoever signed in, under their projects' rules; a refusal says why, with a request ID."},
		"servers":    []any{map[string]string{"url": "/"}},
		"security":   []any{map[string]any{"bearer": []string{}}, map[string]any{"session": []string{}}},
		"tags":       tagList,
		"paths":      paths,
		"components": map[string]any{"securitySchemes": map[string]any{"bearer": map[string]string{"type": "http", "scheme": "bearer", "bearerFormat": "JWT (Keycloak, realm lakehouse)"}, "session": map[string]string{"type": "apiKey", "in": "cookie", "name": "storscale_session"}}},
	}
	return json.MarshalIndent(spec, "", "  ")
}

// firstSentence is a handler's doc comment's first sentence, without its name ("handleX is ..."), as a summary.
func firstSentence(text, handler string) string {
	if text == "" {
		return words(strings.TrimPrefix(handler, "handle"))
	}
	text = strings.Join(strings.Fields(text), " ")
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimSuffix(text, ".")
	if rest, ok := strings.CutPrefix(text, handler+" "); ok {
		text = rest
		for _, verb := range []string{"is ", "are "} { // "handleUsage is every project's month": "Every project's month"
			text = strings.TrimPrefix(text, verb)
		}
	}
	if len(text) > 0 {
		r := []rune(text)
		r[0] = unicode.ToUpper(r[0])
		text = string(r)
	}
	return text
}

// words is a Go name as words: KnowledgeBuild, "Knowledge build".
func words(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteRune(' ')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// apiTag groups a route: a project's part (/api/projects/{name}/<part>), an agent's API, or the first part.
func apiTag(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) >= 4 && parts[1] == "projects":
		return parts[3]
	case len(parts) >= 2 && parts[1] == "agent":
		return "agent"
	case path == "/api/openapi.json":
		return "reference"
	case len(parts) >= 2:
		return parts[1]
	}
	return "api"
}

// operationID is a route's name for code: get_projects_name_usage.
func operationID(method, path string) string {
	p := pathParamName.ReplaceAllString(path, "$1")
	p = strings.NewReplacer("/api/", "", "/", "_", "-", "_", "{", "", "}", "").Replace(p)
	return strings.ToLower(method) + "_" + strings.Trim(p, "_")
}

// The API from a script: a Keycloak token reaches the routes the web app's session does, as its owner.
func TestATokenStandsForASession(t *testing.T) {
	sales, _ := project.Parse([]byte(`{"metadata":{"name":"sales"},"spec":{"members":[{"user":"bob","role":"editor"}],"tables":{}}}`))
	st := &projectsKept{statuses: statuses{list: []*project.Status{{Name: "sales", Spec: sales, Members: []project.Person{{Username: "bob", Role: "editor"}}}}},
		specs: map[string]*project.Project{"sales": sales}}
	s := &server{cfg: config{Groups: []string{"analysts"}, AdminGroups: []string{"engineers"}}, store: st, now: time.Now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), tokenUser: func(raw string) (string, error) { return raw, nil },
		sessions: map[string]*session{"bob": {User: user{Username: "bob", Groups: []string{"analysts"}}, Expires: time.Now().Add(time.Hour)}}}
	h := s.routes()
	for who, want := range map[string]int{"bob": 200, "mallory": 401} {
		r := httptest.NewRequest("GET", "/api/projects", nil)
		r.Header.Set("Authorization", "Bearer "+who)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || (want == 200 && !strings.Contains(w.Body.String(), `"sales"`)) {
			t.Errorf("%s: %d %s, want %d", who, w.Code, w.Body.String(), want)
		}
	}
	r := httptest.NewRequest("GET", "/api/openapi.json", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"openapi": "3.1.0"`) {
		t.Errorf("the reference: %d", w.Code)
	}
}
