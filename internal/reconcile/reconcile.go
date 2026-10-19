// Package reconcile applies projects to the systems they live in: Keycloak
// (groups, and who's in them), Apache Ranger (the same groups, and the
// tables' policies), Buckets (the files' bucket and its policies), Nessie
// (the tables' namespace) and Airflow (the pipelines' git repo). Every step is idempotent: applying a project twice
// does nothing the second time, and a project that's gone is undone.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/store"
)

type Reconciler struct {
	Keycloak *Keycloak
	Ranger   *Ranger
	Buckets  *Buckets
	Nessie   *Nessie
	Airflow  *Airflow // nil: no pipelines from git
	// DefaultStorage is a project bucket's size limit when it names none
	// (as 500Gi; "": none).
	DefaultStorage string
	// TrinoLimits keeps Trino's resource groups from the projects' limits;
	// nil: Trino has no limits but its own.
	TrinoLimits *TrinoLimits
	// Sources keeps Trino's catalogs of the projects' sources; nil: Trino
	// makes none (catalog management isn't dynamic, or no operator account).
	Sources *TrinoSources
	// Reviewer keeps projects' merge requests' data branches; nil: none.
	Reviewer *Reviewer
	// kicked are projects to apply at the next pass, whatever their last
	// (a GitLab merge request's event, through platformd).
	kicked sync.Map
	// Copies copies projects' sources into their tables (Kubernetes); nil:
	// a source that asks to be copied is told it can't be, here.
	Copies *Copies
	// ProjectAirflows, on Kubernetes, gives each project with a repo an
	// Airflow of its own; nil (Compose): projects' repos are bundles in the
	// shared Airflow.
	ProjectAirflows *ProjectAirflows
	Store           store.Store
	Log             *slog.Logger
	// Files reads the store's files, for the assistants platformd registers
	// (use case 10.4); nil: no assistants' clients. PlatformURL is the
	// platform's address, their tokens' MCP audience.
	Files fileReader
	// Protection by tag (12.5): what the catalog's tags mean, and whether
	// a suggested tag counts before someone confirms it.
	Protection       project.ProtectionRules
	ProtectSuggested bool
	PlatformURL      string
	assistantsDigest string
	assistantsAt     time.Time
	// RangerUserPassword starts the password of people the operator adds to
	// Ranger (who never sign in to Ranger: Trino's plugin needs them listed).
	RangerUserPassword string
}

// Apply makes the systems hold what the project says, and reports how each went.
func (r *Reconciler) Apply(ctx context.Context, p *project.Project) *project.Status {
	st := &project.Status{Name: p.Metadata.Name, Observed: p.Hash(), Spec: p}
	part := func(system string, err error) {
		pt := project.Part{System: system, OK: err == nil, Message: "applied"}
		if err != nil {
			pt.Message = err.Error()
		}
		st.Parts = append(st.Parts, pt)
	}

	people, err := r.members(ctx, p)
	st.Members = people.list
	part("keycloak", err)
	// The open merge requests (their data branches' policies go with the
	// project's); on GitLab's error, those of before, kept.
	var before []int
	var prevSpec *project.Project
	if prev, _ := r.Store.Status(ctx, p.Metadata.Name); prev != nil {
		before, prevSpec = prev.Reviews, prev.Spec
	}
	mrs, gl, reviewsErr := r.openReviews(ctx, p)
	st.Reviews = iids(mrs)
	if reviewsErr != nil {
		st.Reviews = before
	}
	if err == nil {
		part("ranger", r.applyRanger(ctx, p, people, st.Reviews))
	} else {
		part("ranger", fmt.Errorf("waiting for Keycloak"))
	}
	part("buckets", r.applyBuckets(ctx, p))
	if p.Spec.Tables != nil {
		err := r.Nessie.ensureNamespace(ctx, p.Spec.Tables.Namespace)
		if err == nil {
			err = r.Nessie.syncTags(ctx, p)
		}
		part("nessie", err)
	}
	if r.Sources != nil {
		conns := func(name string) (map[string]string, error) { return r.Store.Connection(ctx, p.Metadata.Name, name) }
		if err := r.Sources.Apply(ctx, p, conns); len(p.Spec.Sources) > 0 {
			part("trino", err)
		} else if err != nil && r.Log != nil {
			// Sources that went: their catalogs go, when Trino's there.
			r.Log.Warn("removing the sources' catalogs", "project", p.Metadata.Name, "error", err)
		}
	}
	if len(p.Spec.Imports) > 0 {
		part("imports", r.applyImports(ctx, p))
	} else if r.Airflow != nil {
		if err := r.applyImports(ctx, p); err != nil && r.Log != nil { // those it had go
			r.Log.Warn("removing the imports' connections", "project", p.Metadata.Name, "error", err)
		}
	}
	copying := slices.ContainsFunc(p.Spec.Sources, func(s project.Source) bool { return s.Copy != nil })
	switch {
	case r.Copies != nil:
		conns := func(name string) (map[string]string, error) { return r.Store.Connection(ctx, p.Metadata.Name, name) }
		if err := r.Copies.Apply(ctx, p, conns); copying {
			part("copy", err)
		} else if err != nil && r.Log != nil {
			r.Log.Warn("removing the sources' copies", "project", p.Metadata.Name, "error", err)
		}
	case copying:
		part("copy", errors.New("copying a source needs Kubernetes (Compose can't start its pods)"))
	}
	switch {
	case r.ownAirflow(p):
		part("airflow", r.applyProjectAirflow(ctx, p))
	case r.ProjectAirflows != nil:
		if err := r.teardownProjectAirflow(ctx, p); err != nil { // the repo went: its Airflow goes
			part("airflow", err)
		}
	}
	if r.Airflow != nil && !r.ownAirflow(p) {
		if pl := p.Spec.Pipelines; pl != nil && pl.Repo != "" {
			part("airflow", r.applyAirflow(ctx, p))
		} else if err := r.applyAirflow(ctx, p); err != nil && r.Log != nil {
			// The repo went (or there never was one): its connection goes, if
			// any. A project without a repo doesn't wait on Airflow, which may
			// be starting, and itself waits for projects (setup-trino); the
			// next resync tries again.
			r.Log.Warn("removing the git connection", "project", p.Metadata.Name, "error", err)
		}
	}
	if len(p.Spec.Deliveries) > 0 || prevSpec != nil && len(prevSpec.Spec.Deliveries) > 0 {
		err := r.applyDeliveries(ctx, p, prevSpec)
		if len(p.Spec.Deliveries) > 0 {
			part("deliveries", err)
		} else if err != nil && r.Log != nil {
			r.Log.Warn("removing the deliveries' connections", "project", p.Metadata.Name, "error", err)
		}
	}
	if pl := p.Spec.Pipelines; r.Reviewer != nil && (pl != nil && pl.Reviews != nil || len(before) > 0) {
		err := reviewsErr
		if err == nil {
			err = r.applyReviews(ctx, p, mrs, gl, before)
		}
		if pl != nil && pl.Reviews != nil {
			part("reviews", err)
		} else if err != nil && r.Log != nil {
			r.Log.Warn("removing the merge requests' branches", "project", p.Metadata.Name, "error", err)
		}
	}
	st.Phase = project.Ready
	for _, pt := range st.Parts {
		if !pt.OK {
			st.Phase = project.Failed
		}
	}
	st.Updated = time.Now().UTC()
	return st
}

// Delete undoes a project: its groups and policies go; its data (the bucket,
// the tables) stays, for a person to remove on purpose.
func (r *Reconciler) Delete(ctx context.Context, p *project.Project) error {
	var errs []error
	keep := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	keep(r.Ranger.syncPolicies(ctx, p.RangerPrefix(), nil))
	if r.Sources != nil {
		keep(r.Sources.Remove(ctx, p.Metadata.Name))
	}
	if r.Copies != nil { // its tables stay, as its data does
		keep(r.Copies.Remove(ctx, p))
	}
	keep(r.removeImports(ctx, p))
	keep(r.removeDeliveries(ctx, p)) // their targets' credentials with them
	if r.Reviewer != nil {           // its merge requests' branches and catalogs
		if prev, _ := r.Store.Status(ctx, p.Metadata.Name); prev != nil {
			keep(r.applyReviews(ctx, p, nil, nil, prev.Reviews))
		}
	}
	keep(r.Ranger.syncGroupMembers(ctx, p.Groups(), nil, r.RangerUserPassword))
	keep(r.Ranger.deleteGroups(ctx, p.Groups()))
	if r.ProjectAirflows != nil {
		keep(r.teardownProjectAirflow(ctx, p))
	}
	if r.Airflow != nil {
		keep(r.Airflow.API.DeleteConnection(ctx, p.GitConnID()))
	}
	for _, g := range p.Groups() {
		keep(r.Keycloak.deleteGroup(ctx, g))
		keep(r.Buckets.deletePolicy(ctx, g))
	}
	if r.Nessie != nil { // its tags go with it (the history they kept, after GC's while)
		gone := *p
		if gone.Spec.Tables != nil {
			t := *gone.Spec.Tables
			t.Tags = nil
			gone.Spec.Tables = &t
		}
		keep(r.Nessie.syncTags(ctx, &gone))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%v", errs)
	}
	return nil
}

// people are a project's members, by the group they belong in.
type people struct {
	list    []project.Person
	byGroup map[string]map[string]kcUser // project group -> user ID -> user
}

// members works out who's in the project, and makes the project's Keycloak
// groups hold exactly them. An editor is only in the editors' group: the
// readers' row filters and masks must not apply to them.
func (r *Reconciler) members(ctx context.Context, p *project.Project) (people, error) {
	out := people{byGroup: map[string]map[string]kcUser{}}
	role := map[string]string{}  // user ID -> highest role
	via := map[string]string{}   // user ID -> the group they came through
	users := map[string]kcUser{} // user ID -> user
	for _, m := range p.Spec.Members {
		var found []kcUser
		if m.Group != "" {
			g, err := r.Keycloak.group(ctx, m.Group)
			if err != nil {
				return out, err
			}
			if g == nil {
				return out, fmt.Errorf("no Keycloak group %q", m.Group)
			}
			if found, err = r.Keycloak.members(ctx, g.ID); err != nil {
				return out, err
			}
		} else {
			u, err := r.Keycloak.user(ctx, m.User)
			if err != nil {
				return out, err
			}
			if u == nil {
				return out, fmt.Errorf("no Keycloak user %q", m.User)
			}
			found = []kcUser{*u}
		}
		for _, u := range found {
			users[u.ID] = u
			if role[u.ID] != project.Editor {
				role[u.ID], via[u.ID] = m.Role, m.Group
			}
		}
	}
	wanted := map[string]map[string]kcUser{p.Group(project.Reader): {}, p.Group(project.Editor): {}}
	for id, rl := range role {
		wanted[p.Group(rl)][id] = users[id]
		out.list = append(out.list, project.Person{Username: users[id].Username, Role: rl, Via: via[id]})
	}
	if pl := p.Spec.Pipelines; pl != nil {
		clientID := pl.ServiceAccount
		if r.ownAirflow(p) { // its own service account, made with its Airflow's keys
			if err := r.ProjectAirflows.identity(ctx, r.Keycloak, p); err != nil {
				return out, fmt.Errorf("the pipelines' service account: %w", err)
			}
			clientID = p.PipelinesClientID()
		}
		sa, err := r.Keycloak.serviceAccount(ctx, clientID)
		if err != nil {
			return out, err
		}
		if sa == nil {
			return out, fmt.Errorf("Keycloak client %q has no service account", clientID)
		}
		wanted[p.Group("pipelines")] = map[string]kcUser{sa.ID: *sa}
		out.list = append(out.list, project.Person{Username: sa.Username, Role: "pipelines"})
	} else if err := r.Keycloak.deleteGroup(ctx, p.Group("pipelines")); err != nil {
		return out, err
	}
	// Agents of the project's own (12.2): an approved one, whose secret the
	// platform kept at its approval, gets its client, and its service
	// account reads as the project's readers do; the rest have no client.
	agents := map[string]bool{}
	for _, a := range p.Spec.Agents {
		if !a.Approved() || r.Store == nil {
			continue
		}
		sec, err := r.Store.Connection(ctx, p.Metadata.Name, a.ConnectionName(p.Metadata.Name))
		if err != nil || sec == nil || sec["secret"] == "" {
			continue // approved, its secret not kept yet (or gone): no client
		}
		clientID := a.ClientID(p.Metadata.Name)
		if err := r.Keycloak.ensureAgentClient(ctx, clientID, a.Name+", an agent of "+p.Metadata.Name, sec["secret"], r.PlatformURL); err != nil {
			return out, err
		}
		sa, err := r.Keycloak.serviceAccount(ctx, clientID)
		if err != nil {
			return out, err
		}
		if sa == nil {
			continue
		}
		wanted[p.Group(project.Reader)][sa.ID] = *sa
		out.list = append(out.list, project.Person{Username: sa.Username, Role: "agent"})
		agents[clientID] = true
	}
	if err := r.Keycloak.deleteClientsExcept(ctx, "agent-"+p.Metadata.Name+"-", agents); err != nil {
		return out, err
	}
	for name, want := range wanted {
		g, err := r.Keycloak.ensureGroup(ctx, name)
		if err != nil {
			return out, err
		}
		if err := r.Keycloak.syncMembers(ctx, g, want); err != nil {
			return out, fmt.Errorf("group %s: %w", name, err)
		}
	}
	sort.Slice(out.list, func(i, j int) bool { return out.list[i].Username < out.list[j].Username })
	out.byGroup = wanted
	return out, nil
}

func (r *Reconciler) applyRanger(ctx context.Context, p *project.Project, ppl people, reviews []int) error {
	want := map[string][]string{} // username -> project groups
	for g, us := range ppl.byGroup {
		for _, u := range us {
			want[u.Username] = append(want[u.Username], g)
		}
	}
	// The policies name the readers' and editors' groups (and the pipelines',
	// when there are pipelines) whether or not anyone's in them yet.
	named := []string{p.Group(project.Reader), p.Group(project.Editor)}
	if p.Spec.Pipelines != nil {
		named = append(named, p.Group("pipelines"))
	}
	have, err := r.Ranger.ensureGroups(ctx, named)
	if err != nil {
		return err
	}
	if err := r.Ranger.syncGroupMembers(ctx, p.Groups(), want, r.RangerUserPassword); err != nil {
		return err
	}
	if p.Spec.Pipelines == nil {
		if err := r.Ranger.deleteGroups(ctx, []string{p.Group("pipelines")}); err != nil {
			return err
		}
	}
	// Another project's groups (a data product's consumers) are left out
	// until Ranger has them: it refuses a policy naming a group it doesn't
	// know, and the next resync adds them. Ranger's public group is everyone.
	have["public"] = 0
	policies := append(p.RangerPolicies(), p.ReviewPolicies(reviews)...)
	policies = append(policies, p.ProtectionPolicies(r.classification(ctx, p), r.Protection, r.ProtectSuggested)...)
	return r.Ranger.syncPolicies(ctx, p.RangerPrefix(), knownGroups(policies, have))
}

// classification is what the catalog has tagged of a project's columns, as
// catalog-sync wrote it (classification/<project>.json); none when the
// platform keeps no files here, or nothing's tagged yet.
func (r *Reconciler) classification(ctx context.Context, p *project.Project) []project.ProtectedColumn {
	if r.Files == nil {
		return nil
	}
	b, err := r.Files.GetFile(ctx, "classification/"+p.Metadata.Name+".json")
	if err != nil || b == nil {
		return nil
	}
	var c project.Classification
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	return c.Columns
}

func (r *Reconciler) applyBuckets(ctx context.Context, p *project.Project) error {
	policies := p.BucketsPolicies()
	if p.Spec.Files != nil {
		if err := r.Buckets.ensureBucket(ctx, p.Spec.Files.Bucket); err != nil {
			return err
		}
		// Its size limit: the project's, else the install's, else none.
		storage := r.DefaultStorage
		if l := p.Spec.Limits; l != nil && l.Storage != "" {
			storage = l.Storage
		}
		if err := r.Buckets.setQuota(ctx, p.Spec.Files.Bucket, project.StorageBytes(storage)); err != nil {
			return fmt.Errorf("bucket %s's size limit: %w", p.Spec.Files.Bucket, err)
		}
	}
	for _, g := range p.Groups() {
		var err error
		if st, ok := policies[g]; ok {
			err = r.Buckets.putPolicy(ctx, g, st)
		} else {
			err = r.Buckets.deletePolicy(ctx, g)
		}
		if err != nil {
			return fmt.Errorf("policy %s: %w", g, err)
		}
	}
	return nil
}

// Run applies the store's projects until ctx ends: a project whose spec
// changed at once, every project again each resync (Keycloak groups' members
// change without the project changing), and a failed one again after retry.
func (r *Reconciler) Run(ctx context.Context, every, resync, retry time.Duration) {
	last := map[string]time.Time{}
	trinoErr := ""
	for {
		r.pass(ctx, last, resync, retry)
		if r.TrinoLimits != nil {
			err := r.syncTrinoLimits(ctx)
			if msg := fmt.Sprint(err); err != nil && msg != trinoErr {
				r.Log.Warn("Trino's limits", "error", err) // once per new error: Trino may be starting
			} else if err == nil && trinoErr != "" {
				r.Log.Info("Trino's limits written")
			}
			trinoErr = ""
			if err != nil {
				trinoErr = fmt.Sprint(err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (r *Reconciler) pass(ctx context.Context, last map[string]time.Time, resync, retry time.Duration) {
	r.applyAssistants(ctx, resync)
	specs, err := r.Store.Projects(ctx)
	if err != nil {
		r.Log.Error("reading projects", "error", err)
		return
	}
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		r.Log.Error("reading statuses", "error", err)
		return
	}
	byName := map[string]*project.Status{}
	for _, st := range statuses {
		byName[st.Name] = st
	}
	live := map[string]bool{}
	for _, p := range specs {
		live[p.Metadata.Name] = true
		st := byName[p.Metadata.Name]
		since := time.Since(last[p.Metadata.Name])
		_, kick := r.kicked.LoadAndDelete(p.Metadata.Name)
		due := kick || st == nil || st.Observed != p.Hash() || since > resync || (st.Phase != project.Ready && since > retry)
		if !due {
			continue
		}
		if st == nil || st.Observed != p.Hash() {
			r.Store.PutStatus(ctx, &project.Status{Name: p.Metadata.Name, Phase: project.Applying, Observed: p.Hash(),
				Spec: p, Updated: time.Now().UTC(), Members: memberList(st)})
		}
		got := r.Apply(ctx, p)
		last[p.Metadata.Name] = time.Now()
		if err := r.Store.PutStatus(ctx, got); err != nil {
			r.Log.Error("writing status", "project", p.Metadata.Name, "error", err)
		}
		if st == nil || st.Phase != got.Phase || st.Observed != got.Observed {
			r.Log.Info("applied", "project", p.Metadata.Name, "phase", got.Phase, "parts", got.Parts)
		}
	}
	for _, st := range statuses {
		if live[st.Name] {
			continue
		}
		if st.Spec == nil {
			r.Store.DeleteStatus(ctx, st.Name)
			continue
		}
		st.Phase, st.Updated = project.Deleting, time.Now().UTC()
		r.Store.PutStatus(ctx, st)
		if err := r.Delete(ctx, st.Spec); err != nil {
			r.Log.Error("deleting", "project", st.Name, "error", err)
			continue
		}
		r.Store.DeleteStatus(ctx, st.Name)
		delete(last, st.Name)
		r.Log.Info("deleted", "project", st.Name)
	}
}

func memberList(st *project.Status) []project.Person {
	if st == nil {
		return nil
	}
	return st.Members
}

// Kick has the next pass apply a project at once (within seconds).
func (r *Reconciler) Kick(name string) { r.kicked.Store(name, true) }

// ownAirflow says whether the project runs its pipelines in an Airflow of
// its own: on Kubernetes, with a repo.
func (r *Reconciler) ownAirflow(p *project.Project) bool {
	pl := p.Spec.Pipelines
	return r.ProjectAirflows != nil && pl != nil && pl.Repo != ""
}

func (r *Reconciler) syncTrinoLimits(ctx context.Context) error {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return err
	}
	return r.TrinoLimits.Sync(ctx, statuses)
}
