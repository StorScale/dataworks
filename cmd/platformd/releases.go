package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

// The project as code (use case 7.4): its spec and metrics from its repo
// (storscale/ at a tag, a branch or a commit), planned, then applied by
// someone else. A rollback is a plan of an earlier release.

// PlanDAG is the platform's DAG (the storscale bundle) that checks the
// project's repo out at a ref and sends its storscale/ folder back.
const PlanDAG = "storscale_plan"

// planAnswerWithin is how long a plan waits for its run's answer.
const planAnswerWithin = 15 * time.Minute

type plan struct {
	ID       string           `json:"id"`
	Ref      string           `json:"ref"`
	Commit   string           `json:"commit,omitempty"`
	Target   string           `json:"target"`
	By       string           `json:"by"`
	At       time.Time        `json:"at"`
	State    string           `json:"state"` // fetching, ready, failed, applied
	Error    string           `json:"error,omitempty"`
	Changes  []string         `json:"changes"`
	Base     string           `json:"base,omitempty"` // the spec and model it was planned against
	Spec     *project.Project `json:"spec,omitempty"`
	Semantic *string          `json:"semantic,omitempty"` // nil: the repo has no semantic.yaml, the model stays as it is

	AppliedBy string     `json:"appliedBy,omitempty"`
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
}

var (
	refRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	planIDRE = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{6}$`)
)

func planKey(project, id string) string { return "plans/" + project + "/" + id + ".json" }

func (s *server) getPlan(ctx context.Context, project, id string) (*plan, error) {
	if !planIDRE.MatchString(id) {
		return nil, nil
	}
	data, err := s.files.GetFile(ctx, planKey(project, id))
	if err != nil || data == nil {
		return nil, err
	}
	var p plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *server) putPlan(ctx context.Context, project string, p *plan) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.files.PutFile(ctx, planKey(project, p.ID), data, "application/json")
}

// base is what a plan is made against: the project's spec and semantic model
// as they are; a plan applies only while they still are.
func (s *server) base(ctx context.Context, p *project.Project) (string, string, error) {
	model, err := s.files.GetFile(ctx, "semantic/"+p.Metadata.Name+".yaml")
	if err != nil {
		return "", "", err
	}
	spec, _ := json.Marshal(p.Spec)
	h := sha256.New()
	h.Write(spec)
	h.Write([]byte{0})
	h.Write(model)
	return hex.EncodeToString(h.Sum(nil))[:16], string(model), nil
}

// handlePlans is a project's plans, newest first, and its release (the last
// one applied), to its members.
func (s *server) handlePlans(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if s.files == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the platform keeps no files here (no STORE_URL)"})
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	keys, err := s.files.ListFiles(r.Context(), "plans/"+v.Name+"/")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	plans := []plan{}
	var release *plan
	for _, k := range keys {
		id := strings.TrimSuffix(k[strings.LastIndex(k, "/")+1:], ".json")
		p, err := s.getPlan(r.Context(), v.Name, id)
		if err != nil || p == nil {
			continue
		}
		if p.State == "fetching" && s.now().Sub(p.At) > planAnswerWithin {
			p.State, p.Error = "failed", "no answer from its run ("+PlanDAG+" in the project's Airflow)"
		}
		p.Spec, p.Semantic = nil, nil
		if p.State == "applied" && (release == nil || p.AppliedAt.After(*release.AppliedAt)) {
			c := *p
			release = &c
		}
		if len(plans) < 30 {
			plans = append(plans, *p)
		}
	}
	pl := v.Spec.Spec.Pipelines
	writeJSON(w, http.StatusOK, map[string]any{
		"plans": plans, "release": release, "target": s.cfg.InstallTarget,
		"repo":     pl != nil && pl.Repo != "",
		"canPlan":  v.Role == project.Editor || s.isAdmin(sess.User),
		"canApply": s.isAdmin(sess.User),
	})
}

// handlePlan plans a ref of the project's repo (its editors, administrators):
// the platform's DAG checks it out and sends storscale/ back (handlePlanFiles).
func (s *server) handlePlan(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-Platform-Request"})
		return
	}
	if s.files == nil || s.airflow == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "plans need the platform's files and Airflow"})
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	if v.Role != project.Editor && !s.isAdmin(sess.User) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the project's editors plan its releases"})
		return
	}
	if pl := v.Spec.Spec.Pipelines; pl == nil || pl.Repo == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the project has no repo (pipelines.repo)"})
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if !refRE.MatchString(body.Ref) || strings.Contains(body.Ref, "..") {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "a ref of the repo: a tag (v1.3), a branch, or a commit"})
		return
	}
	rnd := make([]byte, 3)
	rand.Read(rnd)
	now := s.now().UTC()
	p := &plan{ID: now.Format("20060102T150405Z") + "-" + hex.EncodeToString(rnd), Ref: body.Ref,
		Target: s.cfg.InstallTarget, By: sess.User.Username, At: now, State: "fetching", Changes: []string{}}
	if err := s.putPlan(r.Context(), v.Name, p); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	af, _, err := s.pipelinesOf(r.Context(), v)
	if err == nil {
		_, err = af.Trigger(r.Context(), PlanDAG, "plan_"+p.ID, map[string]any{
			"project": v.Name, "ref": p.Ref, "plan": p.ID, "folder": project.RepoFolder, "target": p.Target})
	}
	if err != nil {
		p.State, p.Error = "failed", "its run didn't start: "+err.Error()
		s.putPlan(r.Context(), v.Name, p)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": p.Error})
		return
	}
	s.record(r, sess.User.Username, "release.planned", "project:"+v.Name, "ref "+p.Ref+" for "+p.Target)
	writeJSON(w, http.StatusOK, p)
}

// handlePlanFiles takes the repo's storscale/ folder at a plan's ref from the
// platform's DAG (the project's pipelines account): the spec composed for this
// install's target, checked, and what it would change.
func (s *server) handlePlanFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.projectPipelines(w, r, name); !ok {
		return
	}
	p, err := s.getPlan(r.Context(), name, r.PathValue("plan"))
	if err != nil || p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such plan"})
		return
	}
	if p.State != "fetching" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan is " + p.State})
		return
	}
	var body struct {
		Commit string            `json:"commit"`
		Files  map[string]string `json:"files"`
		Error  string            `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p.Commit = body.Commit
	fail := func(why string) {
		p.State, p.Error = "failed", why
		if err := s.putPlan(r.Context(), name, p); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
	if body.Error != "" {
		fail(body.Error)
		return
	}
	spec, ok := body.Files["project.yaml"]
	if !ok {
		fail("no " + project.RepoFolder + "/project.yaml at " + p.Ref)
		return
	}
	composed, err := project.Compose(name, []byte(spec), []byte(body.Files["targets/"+p.Target+".yaml"]))
	if err != nil {
		fail(err.Error())
		return
	}
	before, err := s.store.Project(r.Context(), name)
	if err != nil || before == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the project store isn't answering"})
		return
	}
	base, model, err := s.base(r.Context(), before)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	changes := project.SpecChanges(before, composed)
	if text, ok := body.Files["semantic.yaml"]; ok {
		if err := s.checkModel(r, name, text); err != nil {
			fail("semantic.yaml: " + err.Error())
			return
		}
		moved, err := project.MetricChanges(model, text)
		if err != nil {
			fail(err.Error())
			return
		}
		changes = append(changes, moved...)
		if len(moved) == 0 && model != text {
			changes = append(changes, "semantic model changed (its metrics as they were)")
		}
		p.Semantic = &text
	}
	p.State, p.Spec, p.Base, p.Changes = "ready", composed, base, changes
	if p.Changes == nil {
		p.Changes = []string{}
	}
	if err := s.putPlan(r.Context(), name, p); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// checkModel asks the semantic layer whether a model is one, with the
// pipelines' own token (they're a member).
func (s *server) checkModel(r *http.Request, name, text string) error {
	if s.cfg.SemanticURL == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"yaml": text})
	req, _ := http.NewRequestWithContext(r.Context(), "POST", s.cfg.SemanticURL+"/api/projects/"+name+"/semantic/check", bytes.NewReader(body))
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("Content-Type", "application/json")
	res, err := semanticClient.Do(req)
	if err != nil {
		return fmt.Errorf("the semantic layer isn't answering")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		return nil
	}
	var out struct {
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out)
	if out.Error == "" {
		out.Error = res.Status
	}
	return fmt.Errorf("%s", out.Error)
}

// handleApplyPlan applies a ready plan (an administrator other than who
// planned it): the spec saved (deliveries' approvals kept; one whose sending
// changed, approved again), the semantic model saved, the release recorded.
func (s *server) handleApplyPlan(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	if s.files == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the platform keeps no files here (no STORE_URL)"})
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	who := sess.User.Username
	p, err := s.getPlan(r.Context(), v.Name, r.PathValue("plan"))
	switch {
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	case p == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such plan"})
		return
	case p.State != "ready":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the plan is " + p.State})
		return
	case p.By == who:
		s.recordRefusal(r, who, "release.applied", "project:"+v.Name, "planned it")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "someone other than who planned it applies it"})
		return
	}
	base, _, err := s.base(r.Context(), v.Spec)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if base != p.Base {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the project has changed since it was planned: plan it again"})
		return
	}
	spec := p.Spec
	keepDeliveryApprovals(v.Spec, spec, p.By)
	if err := s.saveSpec(r.Context(), spec); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if p.Semantic != nil {
		if err := s.files.PutFile(r.Context(), "semantic/"+v.Name+".yaml", []byte(*p.Semantic), "application/yaml"); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the spec is saved, the semantic model isn't: " + err.Error()})
			return
		}
	}
	now := s.now().UTC()
	p.State, p.AppliedBy, p.AppliedAt = "applied", who, &now
	if err := s.putPlan(r.Context(), v.Name, p); err != nil {
		s.log.Error("recording a release", "project", v.Name, "plan", p.ID, "error", err)
	}
	s.log.Info("release applied", "project", v.Name, "ref", p.Ref, "commit", p.Commit, "by", who)
	s.record(r, who, "release.applied", "project:"+v.Name,
		fmt.Sprintf("ref %s (%s) for %s, planned by %s; %s", p.Ref, short(p.Commit), p.Target, p.By, strings.Join(p.Changes, "; ")))
	p.Spec, p.Semantic = nil, nil
	writeJSON(w, http.StatusOK, p)
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
