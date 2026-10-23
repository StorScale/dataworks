package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"golang.org/x/oauth2"

	"github.com/StorScale/storscale-platform/internal/project"
)

// What every request gets: an ID (X-Request-ID, in the answer and in the
// log, so a person can quote it), one way of refusing it (fail: the error
// as JSON with the ID, and a log line saying why), and one way of telling
// who asks and what project they're about.

type ctxKey int

const requestIDKey ctxKey = 1

// requestIDs gives each request an ID (the caller's X-Request-ID, when it
// sends a plain one; else a fresh one), answered in the same header.
func requestIDs(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if len(id) > 64 || strings.ContainsFunc(id, func(c rune) bool { return c <= ' ' || c > '~' }) {
			id = ""
		}
		if id == "" {
			b := make([]byte, 8)
			rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// requestID is the request's ID, or "" outside one.
func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey).(string)
	return id
}

// fail answers a request with an error, as every refusal and failure is
// answered ({"error": why, "request": id}), and logs it: a failure of the
// platform's (5xx) as an error, a refusal (4xx) as information.
func (s *server) fail(w http.ResponseWriter, r *http.Request, status int, why string) {
	id := requestID(r)
	if s.log != nil {
		args := []any{"request", id, "method", r.Method, "path", r.URL.Path, "status", status, "error", why}
		if status >= 500 {
			s.log.Error("request failed", args...)
		} else {
			s.log.Info("request refused", args...)
		}
	}
	writeJSON(w, status, map[string]string{"error": why, "request": id})
}

// person is who asks, with the Keycloak token the request can act with:
// the request's own Bearer token (a service's, a test's), or the session's,
// refreshed. A session's change needs the header a cross-site form can't send.
func (s *server) person(w http.ResponseWriter, r *http.Request) (who, token string, ok bool) {
	return s.personWith(w, r, false)
}

// freshPerson is person with a session's token renewed now, whatever its
// age: for what reads the token's own claims (Buckets' STS maps its groups
// to policies), so a project made since the person signed in counts.
func (s *server) freshPerson(w http.ResponseWriter, r *http.Request) (who, token string, ok bool) {
	return s.personWith(w, r, true)
}

func (s *server) personWith(w http.ResponseWriter, r *http.Request, fresh bool) (who, token string, ok bool) {
	if raw, has := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); has && raw != "" {
		who, err := s.bearer(r)
		if err != nil {
			s.fail(w, r, http.StatusUnauthorized, err.Error())
			return "", "", false
		}
		return who, raw, true
	}
	sess := s.member(w, r)
	if sess == nil {
		return "", "", false
	}
	if r.Method != http.MethodGet && r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return "", "", false
	}
	if sess.Token != nil {
		have := sess.Token
		if fresh && have.RefreshToken != "" {
			have = &oauth2.Token{RefreshToken: have.RefreshToken} // no access token: the source gets a new one
		}
		tok, err := s.oauth.TokenSource(r.Context(), have).Token()
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
			return "", "", false
		}
		if tok.AccessToken != sess.Token.AccessToken {
			s.mu.Lock()
			sess.Token = tok
			s.mu.Unlock()
		}
		token = tok.AccessToken
	}
	return sess.User.Username, token, true
}

// projectRole is the project a request names ({name}) and the person's role
// in it (an administrator's session counts as an editor's); it refuses
// someone who isn't a member.
func (s *server) projectRole(w http.ResponseWriter, r *http.Request, who string) (*project.Project, string, bool) {
	if s.store == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "projects aren't set up")
		return nil, "", false
	}
	name := r.PathValue("name")
	st, err := s.store.Status(r.Context(), name)
	if err != nil || st == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return nil, "", false
	}
	role := ""
	for _, m := range st.Members {
		if m.Username == who && m.Role != "pipelines" {
			role = m.Role
		}
	}
	if sess := s.session(r); sess != nil && s.isAdmin(sess.User) {
		role = project.Editor
	}
	if role == "" {
		s.fail(w, r, http.StatusForbidden, "not a member of the project")
		return nil, "", false
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return nil, "", false
	}
	return p, role, true
}

// memberCan says whether someone may do something in a project: by the
// permissions the operator recorded for their role there (an administrator,
// signed in, everything; the project's pipelines, nothing this asks).
func (s *server) memberCan(r *http.Request, projectName, who, perm string) bool {
	if sess := s.session(r); sess != nil && s.isAdmin(sess.User) {
		return true
	}
	if s.store == nil {
		return false
	}
	st, err := s.store.Status(r.Context(), projectName)
	if err != nil || st == nil {
		return false
	}
	for _, m := range st.Members {
		if m.Username == who && m.Role != "pipelines" {
			return project.Permits(m.Role, m.Can, perm)
		}
	}
	return false
}
