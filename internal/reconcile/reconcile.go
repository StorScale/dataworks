// Package reconcile applies projects to the systems they live in: Keycloak
// (groups, and who's in them), Apache Ranger (the same groups, and the
// tables' policies), Buckets (the files' bucket and its policies), Nessie
// (the tables' namespace) and Airflow (the pipelines' git repo). Every step is idempotent: applying a project twice
// does nothing the second time, and a project that's gone is undone.
package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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
	// ProjectAirflows, on Kubernetes, gives each project with a repo an
	// Airflow of its own; nil (Compose): projects' repos are bundles in the
	// shared Airflow.
	ProjectAirflows *ProjectAirflows
	Store           store.Store
	Log             *slog.Logger
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
	if err == nil {
		part("ranger", r.applyRanger(ctx, p, people))
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

func (r *Reconciler) applyRanger(ctx context.Context, p *project.Project, ppl people) error {
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
	if _, err := r.Ranger.ensureGroups(ctx, named); err != nil {
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
	return r.Ranger.syncPolicies(ctx, p.RangerPrefix(), p.RangerPolicies())
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
		due := st == nil || st.Observed != p.Hash() || since > resync || (st.Phase != project.Ready && since > retry)
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
