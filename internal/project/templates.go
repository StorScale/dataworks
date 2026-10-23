package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Project templates and shared libraries (use case U.2), kept by the
// platform's administrators in the platform's bucket.
//
// A template is what a new team's project starts as: a spec (its tables'
// checks, its metrics, its limits, its pipelines' settings, where it's told
// of trouble) and a repo in the install's GitLab, copied as the project's
// own (a sample pipeline, a dbt model). Its members come when a project is
// made from it; "{project}" in its spec becomes the project's name.
//
// A library is a repo in the install's GitLab, at a ref, that projects use
// without copying (R-U.4): a dbt package (its macros, models and tests,
// which are the company's shared checks) named in a dbt project's
// packages.yml, or Python code a pipeline or a notebook puts on its path.
// The platform reads it with the install's token, so a project needs none.

// TemplatesKey and LibrariesKey are where platformd keeps them: {name: ...}.
const (
	TemplatesKey = "templates/templates.json"
	LibrariesKey = "templates/libraries.json"
)

type Template struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Repo is the template's repo in the install's GitLab (group/repo):
	// a project made from it gets a copy, in the install's group.
	Repo string `json:"repo,omitempty"`
	// Spec is the project's, but for its members (given when it's made);
	// with a repo, its pipelines name the copy.
	Spec      Spec   `json:"spec"`
	ChangedBy string `json:"changedBy,omitempty"`
	ChangedAt string `json:"changedAt,omitempty"`
}

type Library struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Repo        string `json:"repo"` // in the install's GitLab: group/repo
	Ref         string `json:"ref"`  // a tag (best: it doesn't move), a branch or a commit
	ChangedBy   string `json:"changedBy,omitempty"`
	ChangedAt   string `json:"changedAt,omitempty"`
}

// Placeholder in a template's spec is the project's name, when one's made.
const Placeholder = "{project}"

// Check says what's wrong with a template, if anything: its name, its repo,
// and its spec, as a project made from it would have it.
func (t *Template) Check() error {
	if !tagNameRE.MatchString(t.Name) {
		return errors.New("a template's name: lower-case letters, digits and dashes")
	}
	if strings.TrimSpace(t.Description) == "" {
		return errors.New("say what the template is for (its description)")
	}
	if t.Repo != "" && !gitPathRE.MatchString(t.Repo) {
		return errors.New("repo: the template repo's path in GitLab, as group/repo")
	}
	if len(t.Spec.Members) > 0 {
		return errors.New("a template has no members: they're given when a project is made from it")
	}
	if t.Spec.Pipelines != nil && t.Spec.Pipelines.Repo != "" {
		return errors.New("pipelines.repo: a project made from the template gets its repo (a copy of the template's); leave it out")
	}
	_, err := t.Render("example", "", []Member{{Group: "example-team", Role: Editor}}, nil, "https://gitlab.example.com/team/example.git", "example-repo", "https://gitlab.example.com", "team/example")
	if err != nil {
		return fmt.Errorf("a project made from it: %w", err)
	}
	return nil
}

// Render is a project made from the template: named name, with these members
// (and description and notify, when given), its pipelines' repo the copy
// (repoURL, signed in with the connection secretRef; its merge requests
// reviewed at gitlab's repoPath when the template's are).
func (t *Template) Render(name, description string, members []Member, notify *Notify, repoURL, secretRef, gitlab, repoPath string) (*Project, error) {
	spec, err := json.Marshal(t.Spec)
	if err != nil {
		return nil, err
	}
	p := &Project{APIVersion: APIVersion, Kind: Kind, Metadata: Metadata{Name: name}}
	raw, _ := json.Marshal(name)
	spec = []byte(strings.ReplaceAll(string(spec), Placeholder, strings.Trim(string(raw), `"`)))
	if err := json.Unmarshal(spec, &p.Spec); err != nil {
		return nil, err
	}
	p.Spec.Members = members
	if description != "" {
		p.Spec.Description = description
	}
	if notify != nil {
		p.Spec.Notify = notify
	}
	if t.Repo != "" {
		if p.Spec.Pipelines == nil {
			p.Spec.Pipelines = &Pipelines{ServiceAccount: "airflow-pipelines"}
		}
		p.Spec.Pipelines.Repo, p.Spec.Pipelines.Credentials = repoURL, &GitCredentials{SecretRef: secretRef}
		if r := p.Spec.Pipelines.Reviews; r != nil {
			r.GitLab, r.Project = gitlab, repoPath
		}
	}
	data, _ := json.Marshal(p)
	return Parse(data)
}

// Check says what's wrong with a library, if anything.
func (l *Library) Check() error {
	switch {
	case !tagNameRE.MatchString(l.Name):
		return errors.New("a library's name: lower-case letters, digits and dashes")
	case strings.TrimSpace(l.Description) == "":
		return errors.New("say what the library is for (its description)")
	case !gitPathRE.MatchString(l.Repo):
		return errors.New("repo: the library's path in GitLab, as group/repo")
	case !gitRefRE.MatchString(l.Ref):
		return errors.New("ref: a tag, a branch or a commit (a tag is best: it doesn't move)")
	}
	return nil
}
