package project

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// Roles of the company's own (use case 16.1): an administrator defines a
// role as a set of the platform's permissions, and a project gives it to
// people and Keycloak groups as it gives editor and reader, which are the
// same permissions, built in. The operator puts a custom role's people in a
// Keycloak group of their own per project (<project>-role-<name>), and each
// system's rules name that group as the role's permissions say: Ranger's
// (tables, masks, row filters), Buckets', the project's Airflow; platformd
// and semanticd read each member's permissions from the project's status.

// The platform's permissions, in a project.
const (
	PermRead         = "read"           // the tables readers may read, as readers see them (row filters, masks)
	PermReadAll      = "read-all"       // every table as it is: no readers' row filters or masks, tags' masks lifted
	PermWriteTables  = "write-tables"   // make, change and drop tables (and read them all)
	PermReadFiles    = "read-files"     // the project's bucket
	PermWriteFiles   = "write-files"    // the bucket, and uploads to its landing area
	PermRunPipelines = "run-pipelines"  // start and clear runs, table maintenance; operate its Airflow
	PermChangeMetric = "change-metrics" // the semantic model and its tests
	PermBuild        = "build"          // flows, apps, knowledge banks, agents, scheduled notebooks
	PermModels       = "models"         // track runs and register models in MLflow
	PermCallModels   = "call-models"    // call the project's served models
	PermApprove      = "approve"        // approve agents, model versions and deliveries (never one's own)
	PermClassify     = "classify"       // confirm or remove the catalog's tags on its columns
	PermConnections  = "connections"    // give and change the project's credentials
	PermRelease      = "release"        // plan and apply releases; tags and history
	PermSeeUsage     = "see-usage"      // the project's usage and cost
	PermSeeAudit     = "see-audit"      // the project's audit log
	PermMuteAlerts   = "mute-alerts"    // mute the project's alerts for a while
)

// A Permission is one of the platform's, as the Roles page lists them.
type Permission struct {
	Name string `json:"name"`
	Says string `json:"says"`
}

// Permissions are the platform's, in the order the Roles page shows them.
var Permissions = []Permission{
	{PermRead, "Read the tables the project shows readers, as readers see them (its row filters and masks)"},
	{PermReadAll, "Read every table of the project as it is: no readers' row filters or masks, and tagged columns unmasked"},
	{PermWriteTables, "Make, change and drop the project's tables"},
	{PermReadFiles, "Read the project's files (its bucket)"},
	{PermWriteFiles, "Write the project's files, and upload to its landing area"},
	{PermRunPipelines, "Start and clear the project's pipelines' runs, and its tables' maintenance"},
	{PermChangeMetric, "Change the project's metrics (its semantic model) and run their tests"},
	{PermBuild, "Build the project's flows, apps, knowledge banks and agents, and schedule notebooks"},
	{PermModels, "Track runs and register models in the project's MLflow"},
	{PermCallModels, "Call the project's served models"},
	{PermApprove, "Approve agents, model versions and deliveries (never one's own)"},
	{PermClassify, "Confirm or remove the catalog's tags on the project's columns"},
	{PermConnections, "Give and change the project's credentials (its connections)"},
	{PermRelease, "Plan and apply releases, make tags, and restore from history"},
	{PermSeeUsage, "See the project's usage and cost"},
	{PermSeeAudit, "See the project's audit log"},
	{PermMuteAlerts, "Mute the project's alerts for a while"},
}

func permissionNames() []string {
	out := make([]string, len(Permissions))
	for i, p := range Permissions {
		out[i] = p.Name
	}
	return out
}

// The platform-wide permissions (a platform role's, given to Keycloak groups
// across every project; an administrator has them all).
const (
	PermSeeAllUsage    = "see-all-usage"    // every project's usage and cost (finance)
	PermSeeGovernance  = "see-governance"   // the Governance page across projects
	PermCertify        = "certify-products" // certify data products, and take certification away
	PermProtectPersons = "protect-personal" // approve deliveries of personal columns; what was sent about a person; erasures
)

// PlatformPermissions are the platform-wide ones, as the Roles page lists them.
var PlatformPermissions = []Permission{
	{PermSeeAllUsage, "See every project's usage and cost"},
	{PermSeeGovernance, "See the Governance page: every project's approvals, agents, rules, products and erasures"},
	{PermCertify, "Certify data products, and take certification away"},
	{PermProtectPersons, "Approve deliveries that send personal columns, search what was sent about a person, and record erasures"},
}

// Role scopes: a project's (given to its members in its spec), or the platform's (given to Keycloak groups on the Roles page).
const (
	ScopeProject  = ""
	ScopePlatform = "platform"
)

// A Role is a named set of the platform's permissions: a project's, given in
// a project's spec, or the platform's, given to Keycloak groups.
type Role struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
	Scope       string   `json:"scope,omitempty"`  // "" a project's, "platform" the platform's
	Groups      []string `json:"groups,omitempty"` // a platform role's: the Keycloak groups given it
	BuiltIn     bool     `json:"builtIn,omitempty"`
	ChangedBy   string   `json:"changedBy,omitempty"`
	ChangedAt   string   `json:"changedAt,omitempty"`
}

// BuiltInRoles are editor and reader, as permissions.
func BuiltInRoles() []Role {
	return []Role{
		{Name: Editor, Description: "Makes and changes the project's tables, files, pipelines, metrics and models", Permissions: permissionNames(), BuiltIn: true},
		{Name: Reader, Description: "Reads what the project shows readers, and calls its models", Permissions: []string{PermRead, PermReadFiles, PermCallModels}, BuiltIn: true},
	}
}

var roleNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,23}$`)

// reservedRoles are names a custom role can't take: the built-in roles, and the members' kinds.
var reservedRoles = []string{Editor, Reader, "pipelines", "agent", "serving", "admin", "editors", "readers"}

// Check says what's wrong with a role an administrator defines, or nil.
func (r Role) Check() error {
	switch {
	case !roleNameRE.MatchString(r.Name):
		return fmt.Errorf("a role's name is 2 to 24 lowercase letters, digits and dashes, starting with a letter: %q isn't", r.Name)
	case slices.Contains(reservedRoles, r.Name):
		return fmt.Errorf("%s is the platform's own", r.Name)
	case len(r.Permissions) == 0:
		return fmt.Errorf("a role gives at least one permission")
	case len(r.Description) > 300:
		return fmt.Errorf("a description of up to 300 characters")
	case r.Scope != ScopeProject && r.Scope != ScopePlatform:
		return fmt.Errorf("a role is a project's or the platform's, not %q", r.Scope)
	case r.Scope == ScopeProject && len(r.Groups) > 0:
		return fmt.Errorf("a project's role is given in a project's spec, not to groups here")
	case r.Scope == ScopePlatform && len(r.Groups) == 0:
		return fmt.Errorf("a platform role is given to at least one Keycloak group")
	}
	known := permissionNames()
	if r.Scope == ScopePlatform {
		known = platformPermissionNames()
	}
	for _, p := range r.Permissions {
		if !slices.Contains(known, p) {
			return fmt.Errorf("no permission %q for a %s role: they're %v", p, map[string]string{"": "project's", "platform": "platform"}[r.Scope], known)
		}
	}
	for _, g := range r.Groups {
		if !principalRE.MatchString(g) {
			return fmt.Errorf("%q isn't a Keycloak group's name", g)
		}
	}
	return nil
}

func platformPermissionNames() []string {
	out := make([]string, len(PlatformPermissions))
	for i, p := range PlatformPermissions {
		out[i] = p.Name
	}
	return out
}

// AllPlatformPermissions are the platform-wide permissions, as an administrator has them.
func AllPlatformPermissions() []string { return platformPermissionNames() }

// Has says whether the role gives a permission, as the systems apply it:
// writing the tables reads them all, and writing the files reads them.
func (r Role) Has(perm string) bool {
	if slices.Contains(r.Permissions, perm) {
		return true
	}
	switch perm {
	case PermReadAll:
		return r.Has(PermWriteTables)
	case PermRead:
		return r.Has(PermReadAll)
	case PermReadFiles:
		return r.Has(PermWriteFiles)
	}
	return false
}

// Granted are the permissions the role gives, implied ones with them, in the platform's order.
func (r Role) Granted() []string {
	out := []string{} // never null: the Roles page lists it
	from := Permissions
	if r.Scope == ScopePlatform {
		from = PlatformPermissions
	}
	for _, p := range from {
		if r.Has(p.Name) {
			out = append(out, p.Name)
		}
	}
	return out
}

// IsBuiltIn says whether a role is editor or reader.
func IsBuiltIn(role string) bool { return role == Editor || role == Reader }

// RoleOf is a role of the project's members, built in or among the defined ones; ok is false for one not defined.
func (p *Project) RoleOf(name string) (Role, bool) {
	for _, r := range BuiltInRoles() {
		if r.Name == name {
			return r, true
		}
	}
	r, ok := p.Roles[name]
	return r, ok && r.Scope == ScopeProject // a platform role isn't a project's
}

// CustomRoles are the custom roles the project's members are given, sorted.
func (p *Project) CustomRoles() []string {
	var out []string
	for _, m := range p.Spec.Members {
		if !IsBuiltIn(m.Role) && !slices.Contains(out, m.Role) {
			out = append(out, m.Role)
		}
	}
	for _, sa := range p.Spec.ServiceAccounts { // a service account is a member in its role
		if !IsBuiltIn(sa.Role) && !slices.Contains(out, sa.Role) {
			out = append(out, sa.Role)
		}
	}
	sort.Strings(out)
	return out
}

// groupsWith are the project's members' groups whose role gives a permission
// (and, with without, doesn't give that one): readers', editors' and each
// defined custom role's that a member is given.
func (p *Project) groupsWith(perm, without string) []string {
	var out []string
	for _, name := range append([]string{Reader, Editor}, p.CustomRoles()...) {
		r, ok := p.RoleOf(name)
		if ok && r.Has(perm) && (without == "" || !r.Has(without)) {
			out = append(out, p.Group(name))
		}
	}
	return out
}

// The groups by how they read the project's tables: masked (readers' rules), whole (every table as it is) and writing.
func (p *Project) maskedReaders() []string { return p.groupsWith(PermRead, PermReadAll) }
func (p *Project) wholeReaders() []string  { return p.groupsWith(PermReadAll, PermWriteTables) }
func (p *Project) tableWriters() []string  { return p.groupsWith(PermWriteTables, "") }

// Permits says whether a project's member may do something, by their role and
// the permissions the operator recorded for it (an editor everything; a reader
// what the built-in reader gives, should none be recorded yet).
func Permits(role string, can []string, perm string) bool {
	switch {
	case role == Editor:
		return true
	case len(can) > 0:
		return slices.Contains(can, perm)
	case role == Reader:
		r, _ := (&Project{}).RoleOf(Reader)
		return r.Has(perm)
	}
	return false
}

// AllPermissions are every permission, as an administrator has them in every project.
func AllPermissions() []string { return permissionNames() }

// RolesKey is where platformd keeps the defined roles, in the platform's bucket: {name: Role}.
const RolesKey = "roles/roles.json"

// ReadingRoleGroups are the groups of the project's custom roles that read its tables (16.1): what a product it consumes is read by, besides its readers, editors and pipelines.
func (p *Project) ReadingRoleGroups() []string {
	var out []string
	for _, name := range p.CustomRoles() {
		if r, ok := p.RoleOf(name); ok && r.Has(PermRead) {
			out = append(out, p.Group(name))
		}
	}
	return out
}
