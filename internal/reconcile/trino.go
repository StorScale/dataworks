package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/StorScale/storscale-platform/internal/project"
)

// TrinoLimits keeps Trino's resource groups (its database-backed
// configuration, which Trino reads every second): a group per project, with
// the project's limits or the install's, and a group per person, all under
// one cap for the install.
//
//	global                 Total
//	├── project-<name>     the project's: its members' queries tagged
//	│                      project:<name>, and its own pipelines' queries
//	└── people
//	    └── ${USER}        Person: everything else, per person
type TrinoLimits struct {
	URL         string // postgres://trino:<password>@trino-db:5432/trino
	Environment string // Trino's node.environment ("docker", the image's)
	Project     project.Limits
	Person      project.Limits
	Total       project.Limits
	written     string // a hash of what's in the database, as last written
}

// trinoGroup is a row of resource_groups; parent is an index into the list.
type trinoGroup struct {
	name         string
	hard, queued int
	memory       string
	parent       int // -1: a root
}

// trinoSelector is a row of selectors.
type trinoSelector struct {
	group    int // index into the groups
	priority int
	user     string // a regex, or ""
	tags     []string
}

// layout is the groups and selectors for these projects.
func (t *TrinoLimits) layout(statuses []*project.Status) ([]trinoGroup, []trinoSelector) {
	or := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	ors := func(v, def string) string {
		if v != "" {
			return v
		}
		return def
	}
	groups := []trinoGroup{
		{name: "global", hard: or(t.Total.Queries, 20), queued: or(t.Total.Queued, 1000), memory: ors(t.Total.QueryMemory, "100%"), parent: -1},
		{name: "people", hard: or(t.Total.Queries, 20), queued: or(t.Total.Queued, 1000), memory: "100%", parent: 0},
		{name: "${USER}", hard: or(t.Person.Queries, 4), queued: or(t.Person.Queued, 100), memory: ors(t.Person.QueryMemory, "50%"), parent: 1},
	}
	// The catch-all: whatever names no project of its own is its person's.
	selectors := []trinoSelector{{group: 2, priority: 1}}

	// A pipelines account in one project alone is that project's (on
	// Kubernetes each has its own); one shared by several is a person.
	accountProjects := map[string]int{}
	live := []*project.Status{}
	for _, st := range statuses {
		if st.Spec == nil || st.Phase == project.Deleting {
			continue
		}
		live = append(live, st)
		for _, m := range st.Members {
			if m.Role == "pipelines" {
				accountProjects[m.Username]++
			}
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Name < live[j].Name })
	for _, st := range live {
		l := project.Limits{}
		if st.Spec.Spec.Limits != nil {
			l = *st.Spec.Spec.Limits
		}
		groups = append(groups, trinoGroup{name: "project-" + st.Name, hard: or(l.Queries, or(t.Project.Queries, 4)),
			queued: or(l.Queued, or(t.Project.Queued, 40)), memory: ors(l.QueryMemory, ors(t.Project.QueryMemory, "50%")), parent: 0})
		g := len(groups) - 1
		var people, accounts []string
		for _, m := range st.Members {
			if m.Role == "pipelines" && accountProjects[m.Username] == 1 {
				accounts = append(accounts, regexp.QuoteMeta(m.Username))
			} else {
				people = append(people, regexp.QuoteMeta(m.Username))
			}
		}
		sort.Strings(people)
		sort.Strings(accounts)
		if len(people) > 0 {
			selectors = append(selectors, trinoSelector{group: g, priority: 100, user: "^(" + strings.Join(people, "|") + ")$",
				tags: []string{"project:" + st.Name}})
		}
		if len(accounts) > 0 {
			selectors = append(selectors, trinoSelector{group: g, priority: 200, user: "^(" + strings.Join(accounts, "|") + ")$"})
		}
	}
	return groups, selectors
}

// Sync writes the layout, when it has changed since it was last written.
// It's the install's, not a project's: it fails no project's status.
func (t *TrinoLimits) Sync(ctx context.Context, statuses []*project.Status) error {
	groups, selectors := t.layout(statuses)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%+v|%+v|%s", groups, selectors, t.Environment)))
	hash := hex.EncodeToString(sum[:])
	if hash == t.written {
		return nil
	}
	c, err := pgx.Connect(ctx, t.URL)
	if err != nil {
		return fmt.Errorf("Trino's limits database: %w", err) // pgx names the host, not the password
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Trino makes these tables as it starts: until then, there's nothing to write to.
	if _, err := tx.Exec(ctx, "DELETE FROM selectors"); err != nil {
		return fmt.Errorf("Trino's limits (has Trino started?): %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM resource_groups"); err != nil {
		return err
	}
	ids := make([]int64, len(groups))
	for i, g := range groups {
		var parent any
		if g.parent >= 0 {
			parent = ids[g.parent]
		}
		if err := tx.QueryRow(ctx, `INSERT INTO resource_groups (name, soft_memory_limit, max_queued, hard_concurrency_limit,
			scheduling_policy, parent, environment) VALUES ($1, $2, $3, $4, 'fair', $5, $6) RETURNING resource_group_id`,
			g.name, g.memory, g.queued, g.hard, parent, t.Environment).Scan(&ids[i]); err != nil {
			return fmt.Errorf("resource group %s: %w", g.name, err)
		}
	}
	for _, s := range selectors {
		var user, tags any
		if s.user != "" {
			user = s.user
		}
		if len(s.tags) > 0 {
			b, _ := json.Marshal(s.tags)
			tags = string(b)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO selectors (resource_group_id, priority, user_regex, client_tags) VALUES ($1, $2, $3, $4)`,
			ids[s.group], s.priority, user, tags); err != nil {
			return fmt.Errorf("selector for %s: %w", groups[s.group].name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	t.written = hash
	return nil
}
