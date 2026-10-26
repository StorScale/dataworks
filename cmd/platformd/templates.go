package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/dataworks/internal/gitlab"
	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/store"
)

// Project templates and shared libraries (use case U.2): the administrators
// keep them on Administration → Templates, in the platform's bucket. A
// project made from a template gets a copy of its repo in the install's
// GitLab group (GITLAB_URL, GITLAB_GROUP; GITLAB_TOKEN may make projects
// there), a token of that repo's own as its connection <project>-repo, and
// the template's spec with its members. A library is read with the
// install's token and handed to whoever the realm signed in (a person, a
// project's pipelines, an agent), so no project holds a token for it.

var commitRE = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

var templatesMu sync.Mutex // one change to the templates' or libraries' file at a time

// repoTokenDays is how long a new project's repo token lasts (GitLab's most is a year).
const repoTokenDays = 365

func (s *server) gitlabFor(repo string) *gitlab.Client {
	return gitlab.New(s.cfg.GitLabURL, s.gitlabToken(), repo)
}

// gitlabToken is the install's GitLab token: the setting's, or its file's now.
func (s *server) gitlabToken() string {
	if s.cfg.GitLabTokenFile != "" {
		b, _ := os.ReadFile(s.cfg.GitLabTokenFile)
		return strings.TrimSpace(string(b))
	}
	return s.cfg.GitLabToken
}

func (s *server) gitlabSet() bool {
	return s.cfg.GitLabURL != "" && s.cfg.GitLabGroup != "" && s.gitlabToken() != ""
}

func readMap[T any](ctx context.Context, s *server, key string) (map[string]T, error) {
	out := map[string]T{}
	if s.files == nil {
		return out, nil
	}
	b, err := s.files.GetFile(ctx, key)
	if err != nil || b == nil {
		return out, err
	}
	return out, json.Unmarshal(b, &out)
}

func writeMap[T any](ctx context.Context, s *server, key string, m map[string]T) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return s.files.PutFile(ctx, key, b, "application/json")
}

func sortedValues[T any](m map[string]T) []T {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]T, 0, len(m))
	for _, n := range names {
		out = append(out, m[n])
	}
	return out
}

// handleTemplates are the templates and libraries, to anyone signed in (a
// team sees what a project would start with, and what it may use).
func (s *server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	ts, err := readMap[project.Template](r.Context(), s, project.TemplatesKey)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the templates' file: "+err.Error())
		return
	}
	ls, err := readMap[project.Library](r.Context(), s, project.LibrariesKey)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the libraries' file: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": sortedValues(ts), "libraries": sortedValues(ls),
		"canEdit": s.isAdmin(sess.User) && s.files != nil, "gitlab": s.cfg.GitLabURL, "group": s.cfg.GitLabGroup, "repos": s.gitlabSet()})
}

// handlePutTemplate defines a template, or changes one (the administrators).
// Projects made from it before keep what they were made with.
func (s *server) handlePutTemplate(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	if s.files == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform keeps no files here (no STORE_URL)")
		return
	}
	var t project.Template
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a template, as JSON: its description, repo and spec: "+err.Error())
		return
	}
	t.Name = r.PathValue("template")
	if err := t.Check(); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if t.Repo != "" {
		if !s.gitlabSet() {
			s.fail(w, r, http.StatusUnprocessableEntity, "the install has no GitLab for templates' repos (GITLAB_URL, GITLAB_GROUP, GITLAB_TOKEN): leave repo out")
			return
		}
		if _, err := s.gitlabFor(t.Repo).Repo(r.Context()); err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, "the template's repo "+t.Repo+": "+err.Error())
			return
		}
	}
	t.ChangedBy, t.ChangedAt = sess.User.Username, s.now().UTC().Format(time.RFC3339)
	templatesMu.Lock()
	defer templatesMu.Unlock()
	ts, err := readMap[project.Template](r.Context(), s, project.TemplatesKey)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the templates' file: "+err.Error())
		return
	}
	_, had := ts[t.Name]
	ts[t.Name] = t
	if err := writeMap(r.Context(), s, project.TemplatesKey, ts); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	detail := map[bool]string{true: "changed", false: "added"}[had]
	if t.Repo != "" {
		detail += "; repo " + t.Repo
	}
	s.record(r, sess.User.Username, "template.saved", "template:"+t.Name, detail)
	writeJSON(w, http.StatusOK, t)
}

// handleDeleteTemplate removes a template; projects made from it stay as they are.
func (s *server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	s.deleteFrom(w, r, project.TemplatesKey, "template")
}

// handleDeleteLibrary removes a library: projects naming it can't fetch it any more.
func (s *server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	s.deleteFrom(w, r, project.LibrariesKey, "library")
}

func (s *server) deleteFrom(w http.ResponseWriter, r *http.Request, key, kind string) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	if s.files == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform keeps no files here (no STORE_URL)")
		return
	}
	name := r.PathValue(kind)
	templatesMu.Lock()
	defer templatesMu.Unlock()
	m, err := readMap[json.RawMessage](r.Context(), s, key)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	if _, ok := m[name]; !ok {
		s.fail(w, r, http.StatusNotFound, "no "+kind+" "+name)
		return
	}
	delete(m, name)
	if err := writeMap(r.Context(), s, key, m); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, sess.User.Username, kind+".removed", kind+":"+name, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleProjectFromTemplate makes a project from a template (the
// administrators): its name, members, and optionally its description and
// where it's told of trouble. With the template's repo: the repo is copied
// into the install's group as the project's (named as the project, and
// "{project}" in its files filled in, as in the spec), given a
// token of its own (Developer; api and read_repository) kept as the
// project's connection <project>-repo, and named in its pipelines. If any
// step fails, what was made is taken back.
func (s *server) handleProjectFromTemplate(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	var body struct {
		Name        string           `json:"name"`
		Description string           `json:"description"`
		Members     []project.Member `json:"members"`
		Notify      *project.Notify  `json:"notify"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		s.fail(w, r, http.StatusBadRequest, "the project's name and members, as JSON")
		return
	}
	ts, err := readMap[project.Template](r.Context(), s, project.TemplatesKey)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the templates' file: "+err.Error())
		return
	}
	t, ok := ts[r.PathValue("template")]
	if !ok {
		s.fail(w, r, http.StatusNotFound, "no template "+r.PathValue("template"))
		return
	}
	secret := body.Name + "-repo"
	// Checked first as it will be made (with a stand-in repo), so nothing is made for a project that can't be.
	if _, err := t.Render(body.Name, body.Description, body.Members, body.Notify, "https://gitlab.example.com/g/r.git", secret, "https://gitlab.example.com", "g/r"); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if existing, err := s.store.Project(r.Context(), body.Name); err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	} else if existing != nil {
		s.fail(w, r, http.StatusConflict, "there's a project "+body.Name+" already")
		return
	}
	ctx, who := r.Context(), sess.User.Username
	repoURL, repoPath, webURL := "", "", ""
	var undo []func()
	failed := func(status int, why string) {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		s.fail(w, r, status, why)
	}
	if t.Repo != "" {
		if !s.gitlabSet() {
			s.fail(w, r, http.StatusServiceUnavailable, "the install has no GitLab for templates' repos (GITLAB_URL, GITLAB_GROUP, GITLAB_TOKEN)")
			return
		}
		fork, err := s.gitlabFor(t.Repo).Fork(ctx, s.cfg.GitLabGroup, body.Name, body.Name, "The "+body.Name+" project's pipelines, from the "+t.Name+" template")
		if err != nil {
			s.fail(w, r, http.StatusBadGateway, "copying the template's repo: "+err.Error())
			return
		}
		repo := s.gitlabFor(fork.Path)
		undo = append(undo, func() {
			if err := repo.Delete(context.WithoutCancel(ctx)); err != nil {
				s.log.Error("the new project's repo wasn't removed", "repo", fork.Path, "error", err)
			}
		})
		copied, err := repo.WaitForCopy(ctx, 2*time.Minute)
		if err == nil {
			err = repo.Unfork(ctx)
		}
		if err == nil {
			err = fillIn(ctx, repo, copied.DefaultBranch, body.Name, t.Name)
		}
		var token string
		if err == nil {
			token, err = repo.AccessToken(ctx, "storscale-"+body.Name, []string{"api", "read_repository"}, 30, s.now().AddDate(0, 0, repoTokenDays))
		}
		if err != nil {
			failed(http.StatusBadGateway, "the project's repo: "+err.Error())
			return
		}
		now := s.now().UTC()
		c := store.StoredConnection{ConnectionMeta: store.ConnectionMeta{Name: secret, Kind: "git", AddedBy: who, AddedAt: now},
			Data: map[string]string{"username": "storscale-" + body.Name, "password": token}}
		if err := s.store.PutConnection(ctx, body.Name, c); err != nil {
			why := "the project store isn't answering"
			if errors.Is(err, store.ErrNotTheProjects) {
				why = "a connection " + secret + " is another's: remove it first"
			}
			failed(http.StatusBadGateway, why)
			return
		}
		undo = append(undo, func() { s.store.DeleteConnection(context.WithoutCancel(ctx), body.Name, secret) })
		repoURL, repoPath, webURL = copied.HTTPURL, copied.Path, copied.WebURL
	}
	p, err := t.Render(body.Name, body.Description, body.Members, body.Notify, repoURL, secret, s.cfg.GitLabURL, repoPath)
	if err == nil {
		err = s.store.PutProject(ctx, p)
	}
	if err != nil {
		var refused *store.APIError
		if errors.As(err, &refused) && refused.Code >= 400 && refused.Code < 500 && refused.Code != http.StatusForbidden {
			failed(http.StatusUnprocessableEntity, refused.Message)
			return
		}
		s.log.Error("making a project from a template", "project", body.Name, "template", t.Name, "error", err)
		failed(http.StatusBadGateway, "the project store isn't answering")
		return
	}
	detail := "from the template " + t.Name
	if repoPath != "" {
		detail += "; its repo " + repoPath + " (a copy of " + t.Repo + ")"
	}
	s.log.Info("project made from a template", "project", p.Metadata.Name, "template", t.Name, "by", who)
	s.record(r, who, "project.saved", "project:"+p.Metadata.Name, detail)
	s.kick(ctx, p.Metadata.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"name": p.Metadata.Name, "spec": p, "repo": webURL})
}

// fillIn makes the copied repo the project's: "{project}" in its files (as
// in the template's spec) becomes the project's name, in one commit.
func fillIn(ctx context.Context, repo *gitlab.Client, branch, name, template string) error {
	if branch == "" {
		branch = "main"
	}
	entries, err := repo.Tree(ctx, "", branch)
	if err != nil {
		return err
	}
	var changes []gitlab.FileChange
	for _, e := range entries {
		if e.Type != "blob" {
			continue
		}
		text, err := repo.File(ctx, e.Path, branch)
		if err != nil {
			return err
		}
		if strings.Contains(text, project.Placeholder) && !strings.ContainsRune(text, 0) {
			changes = append(changes, gitlab.FileChange{Action: "update", FilePath: e.Path, Content: strings.ReplaceAll(text, project.Placeholder, name)})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	_, err = repo.Commit(ctx, branch, "", "The "+name+" project's, from the "+template+" template", "", "", changes)
	return err
}

// handlePutLibrary defines a library, or moves it to another ref (the
// administrators): its repo and ref must be there.
func (s *server) handlePutLibrary(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	if s.files == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform keeps no files here (no STORE_URL)")
		return
	}
	if !s.gitlabSet() {
		s.fail(w, r, http.StatusServiceUnavailable, "the install has no GitLab for libraries (GITLAB_URL, GITLAB_GROUP, GITLAB_TOKEN)")
		return
	}
	var l project.Library
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a library, as JSON: its description, repo and ref")
		return
	}
	l.Name = r.PathValue("library")
	if err := l.Check(); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	commit, err := s.gitlabFor(l.Repo).CommitOf(r.Context(), l.Ref)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "the library's repo "+l.Repo+" at "+l.Ref+": "+err.Error())
		return
	}
	l.ChangedBy, l.ChangedAt = sess.User.Username, s.now().UTC().Format(time.RFC3339)
	templatesMu.Lock()
	defer templatesMu.Unlock()
	ls, err := readMap[project.Library](r.Context(), s, project.LibrariesKey)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the libraries' file: "+err.Error())
		return
	}
	before, had := ls[l.Name]
	ls[l.Name] = l
	if err := writeMap(r.Context(), s, project.LibrariesKey, ls); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	detail := l.Repo + " at " + l.Ref + " (" + commit[:min(12, len(commit))] + ")"
	if had {
		detail += "; was " + before.Repo + " at " + before.Ref
	}
	s.record(r, sess.User.Username, "library.saved", "library:"+l.Name, detail)
	writeJSON(w, http.StatusOK, l)
}

// reader is whoever may read the libraries: someone signed in to the web
// app, or anyone with a token of the realm's (a person, a project's
// pipelines, an agent).
func (s *server) reader(w http.ResponseWriter, r *http.Request) string {
	if sess := s.session(r); sess != nil && s.allowed(sess.User.Groups) {
		return sess.User.Username
	}
	who, err := s.bearer(r)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, err.Error())
		return ""
	}
	return who
}

func (s *server) library(w http.ResponseWriter, r *http.Request) (project.Library, bool) {
	ls, err := readMap[project.Library](r.Context(), s, project.LibrariesKey)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the libraries' file: "+err.Error())
		return project.Library{}, false
	}
	l, ok := ls[r.PathValue("library")]
	if !ok {
		s.fail(w, r, http.StatusNotFound, "no library "+r.PathValue("library")+" (Administration → Templates lists them)")
	}
	return l, ok
}

// handleLibraries are the libraries, each with its repo's address (a dbt
// project's packages.yml names a library by it), to anyone the realm signed in.
func (s *server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	if s.reader(w, r) == "" {
		return
	}
	ls, err := readMap[project.Library](r.Context(), s, project.LibrariesKey)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the libraries' file: "+err.Error())
		return
	}
	out := []map[string]string{}
	for _, l := range sortedValues(ls) {
		out = append(out, map[string]string{"name": l.Name, "description": l.Description, "repo": l.Repo, "ref": l.Ref, "url": s.libraryURL(l)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

func (s *server) libraryURL(l project.Library) string {
	return strings.TrimRight(s.cfg.GitLabURL, "/") + "/" + l.Repo + ".git"
}

// handleLibrary is a library, with the commit its ref names now (a cache's
// key: what was fetched at that commit is still it).
func (s *server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if s.reader(w, r) == "" {
		return
	}
	l, ok := s.library(w, r)
	if !ok {
		return
	}
	if !s.gitlabSet() {
		s.fail(w, r, http.StatusServiceUnavailable, "the install has no GitLab for libraries")
		return
	}
	commit, err := s.gitlabFor(l.Repo).CommitOf(r.Context(), l.Ref)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the library's repo: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": l.Name, "description": l.Description, "repo": l.Repo, "ref": l.Ref, "commit": commit,
		"url": s.libraryURL(l)})
}

// handleLibraryArchive is a library's files at a commit of its (default its
// ref's), as GitLab's .tar.gz: one top folder, then the repo.
func (s *server) handleLibraryArchive(w http.ResponseWriter, r *http.Request) {
	who := s.reader(w, r)
	if who == "" {
		return
	}
	l, ok := s.library(w, r)
	if !ok {
		return
	}
	if !s.gitlabSet() {
		s.fail(w, r, http.StatusServiceUnavailable, "the install has no GitLab for libraries")
		return
	}
	g := s.gitlabFor(l.Repo)
	commit := r.URL.Query().Get("commit")
	if commit == "" {
		var err error
		if commit, err = g.CommitOf(r.Context(), l.Ref); err != nil {
			s.fail(w, r, http.StatusBadGateway, "the library's repo: "+err.Error())
			return
		}
	} else if !commitRE.MatchString(commit) {
		s.fail(w, r, http.StatusBadRequest, "commit: a commit's ID")
		return
	}
	body, err := g.Archive(r.Context(), commit)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Library-Commit", commit)
	io.Copy(w, body)
	s.log.Info("library read", "library", l.Name, "commit", commit, "by", who)
}
