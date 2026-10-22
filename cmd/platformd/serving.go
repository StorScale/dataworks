package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/StorScale/storscale-platform/internal/project"
)

// A project's models answering as an API (use case 15.1, R15.4): each
// serving's server (storscale_serve.py) loads its model's approved version
// and answers platformd alone (on Kubernetes a server of the serving's own,
// a network policy keeping everyone else out; in Compose the shared one).
// platformd signs the caller in (the web app's session or a Keycloak token),
// lets the project's people and its agents call it, keeps the project to a
// few calls at a time, and records every call, and every refusal, in the
// audit log: who, which version, how many rows; never the rows.

const servingCallsAtOnce = 8 // a project's calls in flight, across its servings

var servingSlots sync.Map // project -> chan struct{}

func servingSlot(projectName string) chan struct{} {
	ch, _ := servingSlots.LoadOrStore(projectName, make(chan struct{}, servingCallsAtOnce))
	return ch.(chan struct{})
}

// servingURL is a serving's server's address: SERVING_URL, with {server}
// standing for its name on Kubernetes (model-<project>-<serving>).
func (s *server) servingURL(p *project.Project, sv project.Serving) string {
	base := strings.ReplaceAll(s.cfg.ServingURL, "{server}", p.ServingName(sv))
	return strings.TrimRight(base, "/") + "/p/" + p.Metadata.Name + "/" + sv.Name
}

var servingHTTP = &http.Client{Timeout: 2 * time.Minute}

// servingOf is the request's project, serving and the caller, a member of it;
// a refusal is recorded.
func (s *server) servingOf(w http.ResponseWriter, r *http.Request) (*project.Project, *project.Serving, string, bool) {
	who, _, ok := s.person(w, r)
	if !ok {
		return nil, nil, "", false
	}
	if s.cfg.ServingURL == "" {
		s.fail(w, r, http.StatusServiceUnavailable, "models aren't served here (no SERVING_URL)")
		return nil, nil, "", false
	}
	name := r.PathValue("name")
	if s.mlflowRole(r, name, who) == "" {
		s.recordRefusal(r, who, "model.call", "project:"+name+"/serving:"+r.PathValue("serving"), "not a member of the project")
		s.fail(w, r, http.StatusForbidden, "not a member of the project")
		return nil, nil, "", false
	}
	p, err := s.store.Project(r.Context(), name)
	if err != nil || p == nil {
		s.fail(w, r, http.StatusNotFound, "no such project")
		return nil, nil, "", false
	}
	sv := p.ServingOf(r.PathValue("serving"))
	if sv == nil {
		s.fail(w, r, http.StatusNotFound, "the project serves no model as "+r.PathValue("serving"))
		return nil, nil, "", false
	}
	return p, sv, who, true
}

type servingStatus struct {
	project.Serving
	MaxRows int    `json:"maxRows"`
	URL     string `json:"url"`               // where a caller posts rows
	Version string `json:"version,omitempty"` // the approved version served now
	Error   string `json:"error,omitempty"`   // why none is
}

// handleServings are the project's servings, each with the version it
// serves now (or why none) and the address to call: its members.
func (s *server) handleServings(w http.ResponseWriter, r *http.Request) {
	who, _, ok := s.person(w, r)
	if !ok {
		return
	}
	p, _, ok := s.projectRole(w, r, who)
	if !ok {
		return
	}
	out := []servingStatus{}
	for _, sv := range p.Spec.Serving {
		st := servingStatus{Serving: sv, MaxRows: sv.Rows(), URL: s.cfg.PlatformURL + "/api/projects/" + p.Metadata.Name + "/serving/" + sv.Name + "/predict"}
		if s.cfg.ServingURL == "" {
			st.Error = "models aren't served here"
		} else if res, err := servingHTTP.Get(s.servingURL(p, sv)); err != nil {
			st.Error = "its server isn't answering (starting?)"
		} else {
			var body struct {
				Version *string `json:"version"`
				Error   string  `json:"error"`
			}
			json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&body)
			res.Body.Close()
			if body.Version != nil {
				st.Version = *body.Version
			}
			st.Error = body.Error
			if res.StatusCode == http.StatusNotFound {
				st.Error = "its server doesn't have it yet"
			}
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"serving": out})
}

// handleServingPredict scores rows with a served model's approved version:
// the project's people and its agents; each call recorded (who, the
// version, how many rows), a project's calls a few at a time. The body is
// MLflow's: {"dataframe_records": [...]}, {"dataframe_split": {...}} or
// {"instances": [...]}; the answer {"predictions": [...], "model", "version"}.
func (s *server) handleServingPredict(w http.ResponseWriter, r *http.Request) {
	p, sv, who, ok := s.servingOf(w, r)
	if !ok {
		return
	}
	resource := "project:" + p.Metadata.Name + "/serving:" + sv.Name
	slot := servingSlot(p.Metadata.Name)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	default:
		s.recordRefusal(r, who, "model.call", resource, "the project's calls at their limit")
		w.Header().Set("Retry-After", "1")
		s.fail(w, r, http.StatusTooManyRequests, fmt.Sprintf("the project's models answer %d calls at a time: try again in a moment", servingCallsAtOnce))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20+1))
	if err != nil || len(body) > 32<<20 {
		s.fail(w, r, http.StatusRequestEntityTooLarge, "a call is up to 32 MB")
		return
	}
	rows := countRows(body)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, s.servingURL(p, *sv)+"/invocations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := servingHTTP.Do(req)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "the model's server isn't answering (starting?)")
		return
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	var answer struct {
		Version string `json:"version"`
		Error   string `json:"error"`
	}
	json.Unmarshal(out, &answer)
	if res.StatusCode == http.StatusOK {
		s.record(r, who, "model.called", resource, fmt.Sprintf("%s version %s, %d rows", sv.Model, answer.Version, rows))
	} else {
		s.recordRefusal(r, who, "model.call", resource, fmt.Sprintf("HTTP %d: %s", res.StatusCode, answer.Error))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	w.Write(out)
}

// countRows is how many rows a call sends, for the audit log (-1: not plain).
func countRows(body []byte) int {
	var in struct {
		Records []json.RawMessage `json:"dataframe_records"`
		Split   *struct {
			Data []json.RawMessage `json:"data"`
		} `json:"dataframe_split"`
		Instances []json.RawMessage `json:"instances"`
		Inputs    []json.RawMessage `json:"inputs"`
	}
	if json.Unmarshal(body, &in) != nil {
		return -1
	}
	switch {
	case in.Records != nil:
		return len(in.Records)
	case in.Split != nil:
		return len(in.Split.Data)
	case in.Instances != nil:
		return len(in.Instances)
	case in.Inputs != nil:
		return len(in.Inputs)
	}
	return -1
}
