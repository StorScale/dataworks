package project

import (
	"fmt"
	"slices"
	"time"
)

// A ServiceAccount is an application's identity in the project: a company
// system that works with the project as a member would, signed in as itself
// (a CRM that keeps the project's metric definitions, a tool that reads its
// tables). It's a confidential Keycloak client, svc-<project>-<name>, which
// signs in with its secret (OAuth's client credentials), and whose service
// account is a member of the project in Role: what it may do, read and
// change is that role's, here and nowhere else.
//
// Registered by the project's editors, approved by an administrator other
// than the one who registered it (a service account may be an editor, and
// only administrators make people members), and revoked at once by taking it
// out of the spec. Approval makes its secret, shown once and kept as the
// project's connection <project>-svc-<name>; the operator makes its client
// from that. A changed owner or role needs approval again.
type ServiceAccount struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner"` // a person: who answers for it
	Role        string `json:"role"`  // reader, editor or a custom role (16.1)
	// The platform's, kept as they were on each save.
	RegisteredBy string    `json:"registeredBy,omitempty"`
	RegisteredAt time.Time `json:"registeredAt,omitzero"`
	ApprovedBy   string    `json:"approvedBy,omitempty"`
	ApprovedAt   time.Time `json:"approvedAt,omitzero"`
}

// ServiceAccountClientID is the Keycloak client of a project's service account.
func ServiceAccountClientID(project, name string) string { return "svc-" + project + "-" + name }

// ClientID is the service account's Keycloak client, in a project.
func (sa ServiceAccount) ClientID(project string) string { return ServiceAccountClientID(project, sa.Name) }

// ConnectionName is the project's connection holding its secret.
func (sa ServiceAccount) ConnectionName(project string) string { return project + "-svc-" + sa.Name }

// Username is the name its tokens carry: Keycloak's for a client's service account.
func (sa ServiceAccount) Username(project string) string { return "service-account-" + sa.ClientID(project) }

// Approved says whether it may sign in: approved, and not changed since.
func (sa ServiceAccount) Approved() bool { return sa.ApprovedBy != "" }

// Fingerprint is what approval covers: a change to it needs approval again.
func (sa ServiceAccount) Fingerprint() string { return sa.Owner + "|" + sa.Role }

// String is the service account in words, for the audit log.
func (sa ServiceAccount) String() string {
	return fmt.Sprintf("%s (owner %s; role %s)", sa.Name, sa.Owner, sa.Role)
}

func (p *Project) checkServiceAccounts(bad func(string, ...any)) {
	seen := map[string]bool{}
	for _, sa := range p.Spec.ServiceAccounts {
		switch {
		case !agentNameRE.MatchString(sa.Name):
			bad("serviceAccounts: name %q: lowercase letters, digits and dashes, starting with a letter", sa.Name)
		case seen[sa.Name]:
			bad("serviceAccounts: %q twice", sa.Name)
		}
		seen[sa.Name] = true
		if !principalRE.MatchString(sa.Owner) {
			bad("serviceAccounts: %s: owner %q isn't a person's name", sa.Name, sa.Owner)
		}
		switch {
		case slices.Contains([]string{"pipelines", "agent", "serving"}, sa.Role):
			bad("serviceAccounts: %s: role %q is the platform's own: reader, editor or a custom role", sa.Name, sa.Role)
		case !roleNameRE.MatchString(sa.Role):
			bad("serviceAccounts: %s: role %q: reader, editor or a custom role's name", sa.Name, sa.Role)
		}
		if sa.ApprovedBy != "" && sa.ApprovedBy == sa.RegisteredBy {
			bad("serviceAccounts: %s: approved by the one who registered it", sa.Name)
		}
	}
}

// KeepServiceAccountState keeps what only the platform sets on a project's
// service accounts across a save, and takes an approval away when what it
// covered (its owner, its role) changed.
func KeepServiceAccountState(before, p *Project) {
	prev := map[string]ServiceAccount{}
	if before != nil {
		for _, sa := range before.Spec.ServiceAccounts {
			prev[sa.Name] = sa
		}
	}
	for i := range p.Spec.ServiceAccounts {
		sa := &p.Spec.ServiceAccounts[i]
		old, had := prev[sa.Name]
		sa.RegisteredBy, sa.RegisteredAt = old.RegisteredBy, old.RegisteredAt
		sa.ApprovedBy, sa.ApprovedAt = old.ApprovedBy, old.ApprovedAt
		if had && old.Fingerprint() != sa.Fingerprint() {
			sa.ApprovedBy, sa.ApprovedAt = "", time.Time{}
		}
	}
}

// RegisterNewServiceAccounts records who registered the service accounts a
// release brings that the project hadn't (the person who planned it), so
// that someone else approves each.
func RegisterNewServiceAccounts(p *Project, by string, at time.Time) {
	for i := range p.Spec.ServiceAccounts {
		if sa := &p.Spec.ServiceAccounts[i]; sa.RegisteredBy == "" {
			sa.RegisteredBy, sa.RegisteredAt = by, at
		}
	}
}

// ServiceAccountOf is the project's service account of that name, or nil.
func (p *Project) ServiceAccountOf(name string) *ServiceAccount {
	i := slices.IndexFunc(p.Spec.ServiceAccounts, func(sa ServiceAccount) bool { return sa.Name == name })
	if i < 0 {
		return nil
	}
	return &p.Spec.ServiceAccounts[i]
}
