package main

import (
	"net/http"
	"strings"
)

// fenceOnBehalf keeps a person's token that a project's service account got
// on their behalf (a CRM acting for the person signed in to it: Entra's
// on-behalf-of, then Keycloak's JWT authorization grant) to that project:
// it's the person's own token, good wherever they are a member, but the
// application holding it is the project's alone. Such a request reaches the
// project's routes (/api/projects/<it>/..., /api/agent/projects/<it>/...) and
// /api/session, and only while the project has that client as an approved
// service account that may act for people (onBehalfOf); anything else is
// refused, and the audit log says so.
func (s *server) fenceOnBehalf(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.URL.Path == "/api/session" {
			h.ServeHTTP(w, r)
			return
		}
		sess := s.session(r)
		if sess == nil || sess.For == "" {
			h.ServeHTTP(w, r)
			return
		}
		if name := projectInPath(r.URL.Path); name != "" && s.store != nil {
			if p, err := s.store.Project(r.Context(), name); err == nil && p != nil && p.ActsFor(sess.For) {
				h.ServeHTTP(w, r)
				return
			}
		}
		s.recordRefusal(r, sess.User.Username, "onbehalf.refused", r.URL.Path, "a token "+sess.For+" got on their behalf, outside its project")
		s.fail(w, r, http.StatusForbidden, "this token was got on "+sess.User.Username+"'s behalf by "+sess.For+", a project's service account: it's for that project alone")
	})
}

// projectInPath is the project a route is about (/api/projects/<name>/... or
// /api/agent/projects/<name>/...), or "".
func projectInPath(path string) string {
	for _, prefix := range []string{"/api/projects/", "/api/agent/projects/"} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			name, _, _ := strings.Cut(rest, "/")
			return name
		}
	}
	return ""
}
