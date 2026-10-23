package reconcile

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/project"
)

// Roles of the company's own (use case 16.1): platformd keeps the roles the
// administrators define in the platform's bucket (RolesKey), and each pass
// reads them, so a project whose members are given a role is applied again
// when the role changes (its hash covers its permissions). A custom role's
// people are a Keycloak group of the project's (<project>-role-<name>), which
// Ranger's, Buckets' and the project's Airflow's rules name by the role's
// permissions; their permissions are in the project's status, for platformd
// and semanticd.

// RolesKey is the roles' file in the platform's bucket: {name: Role}.
const RolesKey = project.RolesKey

// roles are the defined roles; none when there's no file (or no store).
func (r *Reconciler) roles(ctx context.Context) map[string]project.Role {
	out := map[string]project.Role{}
	if r.Files == nil {
		return out
	}
	b, err := r.Files.GetFile(ctx, RolesKey)
	if err != nil || b == nil {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil && r.Log != nil {
		r.Log.Warn("the roles' file", "error", err)
	}
	return out
}

// roleRank orders a member's roles: an editor's above all, a reader's below any other, the rest by how much they give.
func roleRank(p *project.Project, role string) int {
	switch role {
	case project.Editor:
		return 1000
	case project.Reader:
		return 0
	}
	if r, ok := p.RoleOf(role); ok {
		return 1 + len(r.Granted())
	}
	return -1
}

// removeRoleGroups removes the groups of custom roles the project's members
// were given last time and aren't now (or that went undefined), from Keycloak
// and Buckets; they're returned, for Ranger to remove once its policies no
// longer name them.
func (r *Reconciler) removeRoleGroups(ctx context.Context, p *project.Project) ([]string, error) {
	if r.Store == nil {
		return nil, nil
	}
	prev, err := r.Store.Status(ctx, p.Metadata.Name)
	if err != nil || prev == nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, name := range p.CustomRoles() {
		if _, ok := p.RoleOf(name); ok {
			keep[p.Group(name)] = true
		}
	}
	gone := map[string]bool{}
	for _, m := range prev.Members {
		g := p.Group(m.Role)
		if strings.HasPrefix(g, p.Metadata.Name+"-role-") && !keep[g] {
			gone[g] = true
		}
	}
	var out []string
	for g := range gone {
		if err := r.Keycloak.deleteGroup(ctx, g); err != nil {
			return nil, err
		}
		if r.Buckets != nil {
			if err := r.Buckets.deletePolicy(ctx, g); err != nil {
				return nil, err
			}
		}
		out = append(out, g)
	}
	sort.Strings(out)
	return out, nil
}
