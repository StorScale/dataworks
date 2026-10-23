package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/project"
)

// Data products (use case 10.3): tables projects publish for others, read in
// place by the projects their producers approve, certified by the governance
// group. Requests to read one wait in the platform's bucket
// (product-requests/<producer>/<product>/<consumer>.json) until the
// producer's editors answer.

type productRequest struct {
	Consumer string `json:"consumer"`
	By       string `json:"by"`
	At       string `json:"at"`
	Why      string `json:"why,omitempty"`
}

type productView struct {
	project.Product
	Project    string           `json:"project"`
	Certified  bool             `json:"certified"`
	Requests   []productRequest `json:"requests"`   // to its producer's editors (and administrators)
	CanApprove bool             `json:"canApprove"` // the viewer edits its project
}

func requestKey(producer, product, consumer string) string {
	return "product-requests/" + producer + "/" + product + "/" + consumer + ".json"
}

func (s *server) requests(r *http.Request, producer, product string) []productRequest {
	out := []productRequest{}
	keys, err := s.files.ListFiles(r.Context(), "product-requests/"+producer+"/"+product+"/")
	if err != nil {
		return out
	}
	for _, k := range keys {
		var q productRequest
		if data, err := s.files.GetFile(r.Context(), k); err == nil && data != nil && json.Unmarshal(data, &q) == nil {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// editorOfProject says whether the person edits the project (or administers the platform).
func (s *server) editorOfProject(r *http.Request, u user, name string) bool {
	if s.isAdmin(u) {
		return true
	}
	st, err := s.store.Status(r.Context(), name)
	return err == nil && st != nil && editorIn(st.Members, u.Username)
}

// handleProducts are every project's products, to anyone signed in: what
// they are is no secret, reading them is (Ranger's).
func (s *server) handleProducts(w http.ResponseWriter, r *http.Request) {
	sess := s.signedIn(w, r)
	if sess == nil {
		return
	}
	specs, err := s.store.Projects(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the project store isn't answering")
		return
	}
	out := []productView{}
	for _, p := range specs {
		mine := s.editorOfProject(r, sess.User, p.Metadata.Name)
		for _, pr := range p.Spec.Products {
			v := productView{Product: pr, Project: p.Metadata.Name, Certified: pr.Certified(), Requests: []productRequest{}, CanApprove: mine}
			if mine && s.files != nil {
				v.Requests = s.requests(r, p.Metadata.Name, pr.Name)
			}
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Certified != out[j].Certified {
			return out[i].Certified
		}
		return out[i].Project+"/"+out[i].Name < out[j].Project+"/"+out[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"products": out,
		"canCertify": s.platformCan(r.Context(), sess.User, project.PermCertify)})
}

// productOf is the request's producer's spec and its product, or it answers.
func (s *server) productOf(w http.ResponseWriter, r *http.Request) (*project.Project, *project.Product) {
	p, err := s.store.Project(r.Context(), r.PathValue("project"))
	if err != nil || p == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return nil, nil
	}
	pr := p.ProductOf(r.PathValue("product"))
	if pr == nil {
		s.fail(w, r, http.StatusNotFound, "no such product")
		return nil, nil
	}
	return p, pr
}

func (s *server) changeRequest(w http.ResponseWriter, r *http.Request) *session {
	sess := s.signedIn(w, r)
	if sess == nil {
		return nil
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		s.fail(w, r, http.StatusForbidden, "missing X-Platform-Request")
		return nil
	}
	if s.files == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "the platform keeps no files here")
		return nil
	}
	return sess
}

// handleRequestProduct asks for a product for a project (its editors).
func (s *server) handleRequestProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.changeRequest(w, r)
	if sess == nil {
		return
	}
	p, pr := s.productOf(w, r)
	if pr == nil {
		return
	}
	var body struct {
		Consumer string `json:"consumer"`
		Why      string `json:"why"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body)
	switch {
	case body.Consumer == "" || body.Consumer == p.Metadata.Name:
		s.fail(w, r, http.StatusUnprocessableEntity, "the project to read it in (consumer), not its own")
		return
	case !s.editorOfProject(r, sess.User, body.Consumer):
		s.fail(w, r, http.StatusForbidden, "the editors of "+body.Consumer+" ask for it")
		return
	case slices.Contains(pr.Consumers, body.Consumer):
		s.fail(w, r, http.StatusConflict, body.Consumer+" reads it already")
		return
	}
	q := productRequest{Consumer: body.Consumer, By: sess.User.Username, At: s.now().UTC().Format("2006-01-02T15:04:05Z"), Why: strings.TrimSpace(body.Why)}
	data, _ := json.Marshal(q)
	if err := s.files.PutFile(r.Context(), requestKey(p.Metadata.Name, pr.Name, body.Consumer), data, "application/json"); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, sess.User.Username, "product.requested", "project:"+p.Metadata.Name+"/product:"+pr.Name, "for "+body.Consumer)
	writeJSON(w, http.StatusOK, q)
}

// handleAnswerRequest approves (or declines) a request: the producer's editors.
func (s *server) handleAnswerRequest(w http.ResponseWriter, r *http.Request) {
	sess := s.changeRequest(w, r)
	if sess == nil {
		return
	}
	p, pr := s.productOf(w, r)
	if pr == nil {
		return
	}
	if !s.editorOfProject(r, sess.User, p.Metadata.Name) {
		s.fail(w, r, http.StatusForbidden, "the editors of "+p.Metadata.Name+" answer requests for its products")
		return
	}
	answer := r.PathValue("answer")
	if answer != "approve" && answer != "decline" {
		s.fail(w, r, http.StatusNotFound, "approve or decline")
		return
	}
	consumer, approve := r.PathValue("consumer"), answer == "approve"
	key := requestKey(p.Metadata.Name, pr.Name, consumer)
	if data, err := s.files.GetFile(r.Context(), key); err != nil || data == nil {
		s.fail(w, r, http.StatusNotFound, "no request from "+consumer)
		return
	}
	if approve && !slices.Contains(pr.Consumers, consumer) {
		pr.Consumers = append(pr.Consumers, consumer)
		sort.Strings(pr.Consumers)
		if err := s.saveSpec(r.Context(), p); err != nil {
			s.fail(w, r, http.StatusBadGateway, err.Error())
			return
		}
		s.kick(r.Context(), p.Metadata.Name)
	}
	if err := s.files.DeleteFile(r.Context(), key); err != nil {
		s.log.Warn("a product request answered, not removed", "key", key, "error", err)
	}
	s.record(r, sess.User.Username, "product."+map[bool]string{true: "approved", false: "declined"}[approve],
		"project:"+p.Metadata.Name+"/product:"+pr.Name, "for "+consumer)
	writeJSON(w, http.StatusOK, map[string]any{"consumers": pr.Consumers})
}

// handleRemoveConsumer takes a project's reading of a product away: the producer's editors.
func (s *server) handleRemoveConsumer(w http.ResponseWriter, r *http.Request) {
	sess := s.changeRequest(w, r)
	if sess == nil {
		return
	}
	p, pr := s.productOf(w, r)
	if pr == nil {
		return
	}
	if !s.editorOfProject(r, sess.User, p.Metadata.Name) {
		s.fail(w, r, http.StatusForbidden, "the editors of "+p.Metadata.Name+" decide who reads its products")
		return
	}
	consumer := r.PathValue("consumer")
	if !slices.Contains(pr.Consumers, consumer) {
		s.fail(w, r, http.StatusNotFound, consumer+" doesn't read it")
		return
	}
	pr.Consumers = slices.DeleteFunc(pr.Consumers, func(c string) bool { return c == consumer })
	if err := s.saveSpec(r.Context(), p); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.kick(r.Context(), p.Metadata.Name)
	s.record(r, sess.User.Username, "product.consumer-removed", "project:"+p.Metadata.Name+"/product:"+pr.Name, consumer)
	writeJSON(w, http.StatusOK, map[string]any{"consumers": pr.Consumers})
}

// handleCertify certifies a product as it is now (POST), or takes its
// certification away (DELETE): the governance group.
func (s *server) handleCertify(w http.ResponseWriter, r *http.Request) {
	sess := s.changeRequest(w, r)
	if sess == nil {
		return
	}
	if !s.platformCan(r.Context(), sess.User, project.PermCertify) {
		s.fail(w, r, http.StatusForbidden, "the governance group (a platform role with certify-products) certifies products")
		return
	}
	p, pr := s.productOf(w, r)
	if pr == nil {
		return
	}
	certify := r.Method == http.MethodPost
	if certify {
		pr.Certification = &project.Certification{By: sess.User.Username, At: s.now().UTC(), Hash: pr.PromiseHash()}
	} else {
		pr.Certification = nil
	}
	if err := s.saveSpec(r.Context(), p); err != nil {
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, sess.User.Username, "product."+map[bool]string{true: "certified", false: "uncertified"}[certify],
		"project:"+p.Metadata.Name+"/product:"+pr.Name, fmt.Sprintf("version %s, table %s", pr.Version, pr.Table))
	writeJSON(w, http.StatusOK, map[string]any{"certified": pr.Certified(), "certification": pr.Certification})
}
