package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/dataworks/internal/project"
)

// Roles of the company's own (use case 16.1): the administrators define a
// role as a set of the platform's permissions, kept here in the platform's
// bucket (project.RolesKey), and a project gives it to people and Keycloak
// groups as it gives editor and reader. The operator applies it in every
// system within a minute of a change (each project giving it is applied
// again); a role a project gives can't be removed.

var rolesMu sync.Mutex // one change to the roles' file at a time

// roleDefs are the defined roles (none, without a file).
func (s *server) roleDefs(ctx context.Context) (map[string]project.Role, error) {
	out := map[string]project.Role{}
	if s.files == nil {
		return out, nil
	}
	b, err := s.files.GetFile(ctx, project.RolesKey)
	if err != nil || b == nil {
		return out, err
	}
	return out, json.Unmarshal(b, &out)
}

type roleView struct {
	project.Role
	Granted  []string `json:"granted"`  // with what its permissions imply (writing the tables reads them all)
	Projects []string `json:"projects"` // the projects giving it
}

// handleRoles are the roles (editor and reader, built in, then the
// defined ones), each with the projects giving it, and the platform's
// permissions: to anyone signed in, as a project's page names them.
func (s *server) handleRoles(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	defs, err := s.roleDefs(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the roles' file: "+err.Error())
		return
	}
	using, err := s.rolesInUse(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	var out []roleView
	for _, b := range append(project.BuiltInRoles(), s.builtInPlatformRoles()...) {
		out = append(out, roleView{Role: b, Granted: b.Granted(), Projects: orEmpty(using[b.Name])})
	}
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, roleView{Role: defs[n], Granted: defs[n].Granted(), Projects: orEmpty(using[n])})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out, "permissions": project.Permissions, "platformPermissions": project.PlatformPermissions,
		"canEdit": s.isAdmin(sess.User) && s.files != nil})
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// rolesInUse are, for each role, the projects whose members are given it.
func (s *server) rolesInUse(ctx context.Context) (map[string][]string, error) {
	specs, err := s.store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, p := range specs {
		seen := map[string]bool{}
		for _, m := range p.Spec.Members {
			if !seen[m.Role] {
				seen[m.Role] = true
				out[m.Role] = append(out[m.Role], p.Metadata.Name)
			}
		}
	}
	return out, nil
}

// handlePutRole defines a role, or changes one (the administrators): its
// description and permissions. Every project giving it is applied again.
func (s *server) handlePutRole(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	if s.files == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform keeps no files here (no STORE_URL)")
		return
	}
	var role project.Role
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&role); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a role, as JSON: its description and permissions")
		return
	}
	role.Name, role.BuiltIn = r.PathValue("role"), false
	for _, b := range s.builtInPlatformRoles() { // the install's settings make these
		if b.Name == role.Name {
			s.fail(w, r, http.StatusBadRequest, role.Name+" is the install's (its settings' groups): define another")
			return
		}
	}
	if err := role.Check(); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	role.ChangedBy, role.ChangedAt = sess.User.Username, time.Now().UTC().Format(time.RFC3339)
	rolesMu.Lock()
	defer rolesMu.Unlock()
	defs, err := s.roleDefs(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the roles' file: "+err.Error())
		return
	}
	before, had := defs[role.Name]
	if had && before.Scope != role.Scope {
		s.fail(w, r, http.StatusConflict, role.Name+" is a "+map[string]string{"": "project's", "platform": "platform"}[before.Scope]+" role: remove it first to make it another kind")
		return
	}
	defs[role.Name] = role
	platformRoleCache.Lock()
	platformRoleCache.at = time.Time{} // read again at the next check
	platformRoleCache.Unlock()
	if err := s.putRoles(r.Context(), defs); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	detail := "permissions " + strings.Join(role.Permissions, ", ")
	if had {
		detail = "was " + strings.Join(before.Permissions, ", ") + "; now " + strings.Join(role.Permissions, ", ")
	}
	if role.Scope == project.ScopePlatform {
		detail += "; given to groups " + strings.Join(role.Groups, ", ")
		if had {
			detail += " (were " + strings.Join(before.Groups, ", ") + ")"
		}
	}
	s.log.Info("role saved", "role", role.Name, "by", sess.User.Username)
	s.record(r, sess.User.Username, "role.saved", "role:"+role.Name, detail)
	writeJSON(w, http.StatusOK, roleView{Role: role, Granted: role.Granted(), Projects: []string{}})
}

// handleDeleteRole removes a role (the administrators), once no project gives it.
func (s *server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	sess := s.admin(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	name := r.PathValue("role")
	rolesMu.Lock()
	defer rolesMu.Unlock()
	defs, err := s.roleDefs(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the roles' file: "+err.Error())
		return
	}
	if _, ok := defs[name]; !ok {
		s.fail(w, r, http.StatusNotFound, "no role "+name)
		return
	}
	using, err := s.rolesInUse(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	if ps := using[name]; len(ps) > 0 {
		s.recordRefusal(r, sess.User.Username, "role.removed", "role:"+name, "in use by "+strings.Join(ps, ", "))
		s.fail(w, r, http.StatusConflict, "the projects "+strings.Join(ps, ", ")+" give "+name+": change their members first")
		return
	}
	delete(defs, name)
	platformRoleCache.Lock()
	platformRoleCache.at = time.Time{}
	platformRoleCache.Unlock()
	if err := s.putRoles(r.Context(), defs); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, sess.User.Username, "role.removed", "role:"+name, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) putRoles(ctx context.Context, defs map[string]project.Role) error {
	b, _ := json.MarshalIndent(defs, "", "  ")
	return s.files.PutFile(ctx, project.RolesKey, b, "application/json")
}

// Platform roles (16.1): finance, the governance group and the
// data-protection approvers, built in from the install's settings
// (USAGE_VIEWER_GROUPS, PRODUCT_CERTIFIER_GROUPS, DELIVERY_APPROVER_GROUPS),
// and the platform roles the administrators define, each given to Keycloak
// groups. Someone may do what any of their groups' platform roles gives; an
// administrator, everything.

// builtInPlatformRoles are the platform roles the install's settings make.
func (s *server) builtInPlatformRoles() []project.Role {
	return []project.Role{
		{Name: "finance", Description: "Sees every project's usage and cost", Permissions: []string{project.PermSeeAllUsage},
			Scope: project.ScopePlatform, Groups: s.cfg.UsageGroups, BuiltIn: true},
		{Name: "governance", Description: "Certifies data products, and sees the Governance page", Permissions: []string{project.PermCertify, project.PermSeeGovernance},
			Scope: project.ScopePlatform, Groups: s.cfg.ProductCertifierGroups, BuiltIn: true},
		{Name: "data-protection", Description: "Approves deliveries of personal data, finds what was sent about a person, records erasures",
			Permissions: []string{project.PermProtectPersons, project.PermSeeGovernance}, Scope: project.ScopePlatform, Groups: s.cfg.DeliveryApproverGroups, BuiltIn: true},
	}
}

var platformRoleCache struct {
	sync.Mutex
	at    time.Time
	roles []project.Role
}

// platformRoles are the built-in platform roles and the defined ones (read again every half minute).
func (s *server) platformRoles(ctx context.Context) []project.Role {
	out := s.builtInPlatformRoles()
	platformRoleCache.Lock()
	defer platformRoleCache.Unlock()
	if time.Since(platformRoleCache.at) > 30*time.Second {
		if defs, err := s.roleDefs(ctx); err == nil {
			platformRoleCache.roles = nil
			for _, r := range defs {
				if r.Scope == project.ScopePlatform {
					platformRoleCache.roles = append(platformRoleCache.roles, r)
				}
			}
			platformRoleCache.at = time.Now()
		}
	}
	return append(out, platformRoleCache.roles...)
}

// platformCan says whether someone may do something across the platform: by their groups' platform roles (an administrator, anything).
func (s *server) platformCan(ctx context.Context, u user, perm string) bool {
	if s.isAdmin(u) {
		return true
	}
	for _, r := range s.platformRoles(ctx) {
		if slices.Contains(r.Permissions, perm) && s.inGroups(u.Groups, r.Groups) {
			return true
		}
	}
	return false
}

// platformGroupsWith are the Keycloak groups whose platform roles give a permission (an administrator's aside), to name them.
func (s *server) platformGroupsWith(ctx context.Context, perm string) []string {
	var out []string
	for _, r := range s.platformRoles(ctx) {
		if slices.Contains(r.Permissions, perm) {
			for _, g := range r.Groups {
				if !slices.Contains(out, g) {
					out = append(out, g)
				}
			}
		}
	}
	return out
}
