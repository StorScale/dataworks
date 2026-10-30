package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/StorScale/dataworks/internal/project"
	"github.com/StorScale/dataworks/internal/store"
)

// A project's service accounts: applications' identities in it (a CRM, a
// company system), each a member in a role. Registered by its editors;
// approved by an administrator other than the one who registered it, since
// a service account may be an editor and only administrators make members;
// revoked at once. Approval makes the secret (shown once, kept as the
// project's connection <project>-svc-<name>), and so does a rotation; the
// operator makes the client, and changes its secret, from that.

type serviceAccountView struct {
	project.ServiceAccount
	ClientID   string `json:"clientId"`
	Username   string `json:"username"` // as its tokens name it, in the audit log and the project's members
	Connection string `json:"connection"`
	Approved   bool   `json:"approved"`
	TokenURL   string `json:"tokenUrl"`
}

func (s *server) serviceAccountViews(p *project.Project) []serviceAccountView {
	out := []serviceAccountView{}
	for _, sa := range p.Spec.ServiceAccounts {
		out = append(out, serviceAccountView{ServiceAccount: sa, ClientID: sa.ClientID(p.Metadata.Name), Username: sa.Username(p.Metadata.Name),
			Connection: sa.ConnectionName(p.Metadata.Name), Approved: sa.Approved(), TokenURL: s.tokenURL()})
	}
	return out
}

// tokenURL is where a service account gets its token (client credentials).
func (s *server) tokenURL() string {
	return s.cfg.PlatformURL + "/sso/realms/lakehouse/protocol/openid-connect/token"
}

// handleServiceAccounts lists a project's service accounts, to its members.
func (s *server) handleServiceAccounts(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"serviceAccounts": s.serviceAccountViews(v.Spec),
		"canEdit": v.can(project.PermBuild), "canApprove": s.isAdmin(sess.User)})
}

// handlePutServiceAccount registers a service account, or changes one (a
// changed owner or role takes its approval away).
func (s *server) handlePutServiceAccount(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermBuild, "register its service accounts")
	if sess == nil {
		return
	}
	if isServiceAccount(sess.User) {
		s.fail(w, r, http.StatusForbidden, "a service account doesn't register service accounts: a person does")
		return
	}
	var sa project.ServiceAccount
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sa); err != nil {
		s.fail(w, r, http.StatusBadRequest, "a service account, as JSON: "+err.Error())
		return
	}
	sa.Name = r.PathValue("account")
	if sa.Owner == "" {
		sa.Owner = sess.User.Username
	}
	p := *v.Spec
	p.Spec.ServiceAccounts = slices.Clone(p.Spec.ServiceAccounts)
	i := slices.IndexFunc(p.Spec.ServiceAccounts, func(x project.ServiceAccount) bool { return x.Name == sa.Name })
	added := i < 0
	if added {
		p.Spec.ServiceAccounts = append(p.Spec.ServiceAccounts, sa)
	} else {
		p.Spec.ServiceAccounts[i] = sa
	}
	project.KeepServiceAccountState(v.Spec, &p)
	if added {
		got := p.ServiceAccountOf(sa.Name)
		got.RegisteredBy, got.RegisteredAt = sess.User.Username, s.now().UTC()
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(r, sess.User.Username, "serviceaccount.registered", "project:"+v.Name+"/serviceaccount:"+sa.Name,
		map[bool]string{true: "registered ", false: "changed "}[added]+sa.String())
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, s.serviceAccountViews(&p))
}

// handleApproveServiceAccount approves a service account (an administrator
// other than the one who registered it), making its secret, shown once.
func (s *server) handleApproveServiceAccount(w http.ResponseWriter, r *http.Request) {
	s.issueServiceAccountSecret(w, r, false)
}

// handleRotateServiceAccount gives an approved service account a new secret
// (an administrator), shown once: the old one stops working within a
// minute, when the operator changes the client; tokens already issued last
// until they expire.
func (s *server) handleRotateServiceAccount(w http.ResponseWriter, r *http.Request) {
	s.issueServiceAccountSecret(w, r, true)
}

func (s *server) issueServiceAccountSecret(w http.ResponseWriter, r *http.Request, rotate bool) {
	what := map[bool]string{false: "approved", true: "rotated"}[rotate]
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return
	}
	v := s.project(w, r, sess)
	if v == nil {
		return
	}
	name := r.PathValue("account")
	p := *v.Spec
	p.Spec.ServiceAccounts = slices.Clone(p.Spec.ServiceAccounts)
	sa := p.ServiceAccountOf(name)
	target := "project:" + v.Name + "/serviceaccount:" + name
	switch {
	case !s.isAdmin(sess.User):
		s.recordRefusal(r, sess.User.Username, "serviceaccount."+what, target, "not an administrator")
		s.fail(w, r, http.StatusForbidden, "a service account is a member of the project, which only administrators make: an administrator approves it")
		return
	case sa == nil:
		s.fail(w, r, http.StatusNotFound, "no such service account")
		return
	case !rotate && sa.RegisteredBy == sess.User.Username:
		s.recordRefusal(r, sess.User.Username, "serviceaccount.approved", target, "approving their own")
		s.fail(w, r, http.StatusForbidden, "a service account is approved by an administrator other than the one who registered it")
		return
	case !rotate && sa.Approved():
		writeJSON(w, http.StatusOK, map[string]any{"approved": true, "by": sa.ApprovedBy, "at": sa.ApprovedAt})
		return
	case rotate && !sa.Approved():
		s.fail(w, r, http.StatusConflict, "it isn't approved: its secret is made when it is")
		return
	}
	b := make([]byte, 24)
	rand.Read(b)
	secret := hex.EncodeToString(b)
	conn := store.StoredConnection{ConnectionMeta: store.ConnectionMeta{Name: sa.ConnectionName(v.Name), Kind: "serviceaccount", AddedBy: sess.User.Username, AddedAt: s.now().UTC()},
		Data: map[string]string{"clientId": sa.ClientID(v.Name), "secret": secret}}
	if err := s.store.PutConnection(r.Context(), v.Name, conn); err != nil {
		s.fail(w, r, http.StatusBadGateway, "the service account's secret couldn't be kept: "+err.Error())
		return
	}
	if !rotate {
		sa.ApprovedBy, sa.ApprovedAt = sess.User.Username, s.now().UTC()
		if err := s.saveSpec(r.Context(), &p); err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	s.record(r, sess.User.Username, "serviceaccount."+what, target, sa.String())
	s.kick(r.Context(), v.Name)
	writeJSON(w, http.StatusOK, map[string]any{"approved": true, "by": sa.ApprovedBy, "at": sa.ApprovedAt,
		"clientId": sa.ClientID(v.Name), "secret": secret, "tokenUrl": s.tokenURL(), "username": sa.Username(v.Name),
		"note": "The secret is shown once: give it to the application now. The operator makes (or changes) its client within a minute."})
}

// handleDeleteServiceAccount revokes a service account: out of the spec,
// its secret gone; the operator removes its client and membership at its
// next pass, and new tokens are refused.
func (s *server) handleDeleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	sess, v := s.editorOf(w, r, project.PermBuild, "revoke its service accounts")
	if sess == nil {
		return
	}
	name := r.PathValue("account")
	p := *v.Spec
	before := len(p.Spec.ServiceAccounts)
	p.Spec.ServiceAccounts = slices.DeleteFunc(slices.Clone(p.Spec.ServiceAccounts), func(x project.ServiceAccount) bool { return x.Name == name })
	if len(p.Spec.ServiceAccounts) == before {
		s.fail(w, r, http.StatusNotFound, "no such service account")
		return
	}
	if err := s.saveSpec(r.Context(), &p); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.store.DeleteConnection(r.Context(), v.Name, v.Name+"-svc-"+name)
	s.record(r, sess.User.Username, "serviceaccount.revoked", "project:"+v.Name+"/serviceaccount:"+name, "revoked at "+s.now().UTC().Format(time.RFC3339))
	s.kick(r.Context(), v.Name)
	w.WriteHeader(http.StatusNoContent)
}
