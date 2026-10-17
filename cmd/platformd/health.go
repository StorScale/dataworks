package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/StorScale/storscale-platform/internal/health"
)

// alertsSecret holds where alerts go (health.Targets): the platform's own.
const alertsSecret = "storscale-alerts"

// watch is the platform's health: its services, checked every few seconds;
// alerts to the administrators when one goes down or comes back, and when
// the platform's certificate nears its end.
type watch struct {
	checker  *health.Checker
	notifier *health.Notifier
	findings health.Findings // monitoring rules firing, setup steps that gave up
	kube     *health.Kube    // nil in Compose
	link     string          // the Health page

	mu      sync.Mutex
	certEnd time.Time
	certErr string
	certSay time.Time // when the certificate was last alerted on
	backups health.Backups
}

func (s *server) startHealth(ctx context.Context) {
	w := &watch{link: s.cfg.PlatformURL + "/admin/health"}
	var cache struct {
		sync.Mutex
		at time.Time
		t  health.Targets
	}
	w.notifier = &health.Notifier{Targets: func(ctx context.Context) (health.Targets, error) {
		cache.Lock()
		defer cache.Unlock()
		if time.Since(cache.at) < 30*time.Second {
			return cache.t, nil
		}
		if s.store == nil {
			return health.Targets{}, nil
		}
		sec, err := s.store.Secret(ctx, alertsSecret)
		if err != nil {
			return health.Targets{}, err
		}
		cache.at, cache.t = time.Now(), health.TargetsFrom(sec)
		return cache.t, nil
	}}
	checks := health.Services()
	if u := os.Getenv("GATEWAY_HEALTH_URL"); u != "" { // its port is the install's
		for i := range checks {
			if checks[i].Name == "gateway" {
				checks[i].URL = u
			}
		}
	}
	w.checker = &health.Checker{Checks: checks, Every: 15 * time.Second, Quiet: 5 * time.Minute,
		Notify: func(ch health.Change) {
			s.log.Info("service", "name", ch.Status.Name, "state", ch.Status.State, "reason", ch.Status.Reason)
			if until := w.notifier.Muted(ch.Status.Name); !until.IsZero() {
				return
			}
			w.send(ctx, s, health.FromChange(ch, w.link))
		}}
	if s.cfg.ProjectStore == "kubernetes" {
		if k, err := health.NewKube(checks); err == nil {
			w.checker.Pods, w.kube = k, k
		} else {
			s.log.Warn("health: no reasons from Kubernetes", "error", err)
		}
	}
	s.health = w
	go w.checker.Run(ctx)
	go w.watchCert(ctx, s)
	go w.watchFindings(ctx, s)
}

// watchFindings follows, every minute, the monitoring rules firing in
// Prometheus (storage near full, a drive offline: nothing else delivers
// them) and, on Kubernetes, the setup steps that gave up; and alerts on
// what starts and what ends.
func (w *watch) watchFindings(ctx context.Context, s *server) {
	prom := os.Getenv("PROMETHEUS_URL")
	if prom == "" {
		prom = "http://prometheus:9090"
	}
	client := &http.Client{Timeout: 15 * time.Second}
	started := time.Now()
	for {
		quiet := time.Since(started) < 5*time.Minute // the platform starting
		if firing, err := health.PrometheusAlerts(ctx, client, prom); err == nil {
			on, off := w.findings.Set("prometheus", firing)
			if !quiet {
				for _, f := range on {
					w.send(ctx, s, health.FromFinding(f, false, w.link))
				}
				for _, f := range off {
					w.send(ctx, s, health.FromFinding(f, true, w.link))
				}
			}
		}
		if w.kube != nil {
			if b, err := w.kube.Backups(ctx); err == nil {
				w.mu.Lock()
				w.backups = b
				w.mu.Unlock()
				on, off := w.findings.Set("backups", b.Overdue(time.Now(), 26*time.Hour))
				for _, f := range on {
					w.send(ctx, s, health.FromFinding(f, false, w.link))
				}
				for _, f := range off {
					w.send(ctx, s, health.FromFinding(f, true, w.link))
				}
			}
			if failed, err := w.kube.FailedJobs(ctx); err == nil {
				on, _ := w.findings.Set("jobs", failed)
				for _, f := range on {
					if f.Since.After(started) { // not again at every restart
						w.send(ctx, s, health.FromFinding(f, false, w.link))
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (w *watch) send(ctx context.Context, s *server, a health.Alert) map[string]error {
	errs := w.notifier.Send(ctx, a)
	for target, err := range errs {
		if err != nil {
			s.log.Error("alert not sent", "target", target, "alert", a.Title, "error", err)
		}
	}
	return errs
}

// watchCert checks the platform's certificate hourly, and alerts once a day
// in its last 14 days.
func (w *watch) watchCert(ctx context.Context, s *server) {
	for {
		end, err := health.CertExpiry(ctx, s.cfg.PlatformURL)
		w.mu.Lock()
		w.certEnd, w.certErr = end, ""
		if err != nil {
			w.certErr = err.Error()
		}
		due := !end.IsZero() && time.Until(end) < 14*24*time.Hour && time.Since(w.certSay) > 23*time.Hour
		if due {
			w.certSay = time.Now()
		}
		w.mu.Unlock()
		if due {
			days := int(time.Until(end).Hours() / 24)
			w.send(ctx, s, health.Alert{Title: fmt.Sprintf("The platform's certificate ends in %d days", days), Severity: "warning",
				Facts: [][2]string{{"Address", s.cfg.PlatformURL}, {"Ends", end.UTC().Format("2006-01-02 15:04 MST")}},
				Link:  w.link, At: time.Now().UTC()})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// handleHealth is the platform's health: in full for administrators; for
// everyone else, what they can't use just now.
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	sess := s.member(w, r)
	if sess == nil {
		return
	}
	if s.health == nil {
		writeJSON(w, http.StatusOK, map[string]any{"checked": false})
		return
	}
	statuses := s.health.checker.Statuses()
	affected := []string{}
	seen := map[string]bool{}
	for _, st := range statuses {
		if st.State == health.Down {
			for _, f := range st.Features {
				if !seen[f] {
					seen[f] = true
					affected = append(affected, f)
				}
			}
		}
	}
	sort.Strings(affected)
	out := map[string]any{"checked": true, "affected": affected}
	if s.isAdmin(sess.User) {
		type service struct {
			health.Status
			MutedUntil *time.Time `json:"mutedUntil,omitempty"`
		}
		var services []service
		for _, st := range statuses {
			sv := service{Status: st}
			if t := s.health.notifier.Muted(st.Name); !t.IsZero() {
				sv.MutedUntil = &t
			}
			services = append(services, sv)
		}
		targets, terr := s.health.notifier.Targets(r.Context())
		s.health.mu.Lock()
		cert := map[string]any{"ends": s.health.certEnd, "error": s.health.certErr}
		backups := s.health.backups
		s.health.mu.Unlock()
		out["backups"] = backups
		out["services"] = services
		out["findings"] = s.health.findings.All()
		out["certificate"] = cert
		out["alerts"] = s.health.notifier.Recent()
		dest := map[string]any{"teams": targets.TeamsWebhook != "", "email": targets.EmailTo}
		if terr != nil {
			dest["error"] = terr.Error()
		}
		out["targets"] = dest
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHealthAction mutes a service's alerts, or sends a test alert:
// administrators only.
func (s *server) handleHealthAction(w http.ResponseWriter, r *http.Request) {
	sess := s.member(w, r)
	if sess == nil {
		return
	}
	if r.Header.Get("X-Platform-Request") != "1" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-Platform-Request"})
		return
	}
	if !s.isAdmin(sess.User) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrators only"})
		return
	}
	if s.health == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "health checks are off"})
		return
	}
	switch r.PathValue("action") {
	case "mute":
		var req struct {
			Service string  `json:"service"`
			Hours   float64 `json:"hours"` // 0: unmute
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Service == "" || req.Hours < 0 || req.Hours > 24*7 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "want {service, hours}: up to a week"})
			return
		}
		var until time.Time
		if req.Hours > 0 {
			until = time.Now().Add(time.Duration(req.Hours * float64(time.Hour)))
		}
		s.health.notifier.Mute(req.Service, until)
		s.log.Info("alerts muted", "service", req.Service, "until", until, "by", sess.User.Username)
		s.record(r, sess.User.Username, "alert.muted", "platform/service:"+req.Service, fmt.Sprintf("for %g hours", req.Hours))
		writeJSON(w, http.StatusOK, map[string]any{"service": req.Service, "mutedUntil": until})
	case "test":
		s.record(r, sess.User.Username, "alert.tested", "platform", "")
		errs := s.health.send(r.Context(), s, health.Alert{Title: "A test alert from the platform", Severity: "test",
			Facts: [][2]string{{"Sent by", sess.User.Username}, {"Address", s.cfg.PlatformURL}}, Link: s.health.link, At: time.Now().UTC()})
		res := map[string]string{}
		for target, err := range errs {
			res[target] = "sent"
			if err != nil {
				res[target] = err.Error()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"targets": res})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such action"})
	}
}

// serveMetrics serves the checks' metrics to Prometheus, on a port of its
// own: never through the gateway.
func (s *server) serveMetrics(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if s.health != nil {
			fmt.Fprint(w, s.health.checker.Metrics())
		}
		if s.maintainer != nil {
			fmt.Fprint(w, s.maintainer.metrics())
		}
	})
	hs := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); hs.Close() }()
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.log.Error("metrics", "error", err)
	}
}

// member is the session of someone allowed in, projects or not, or writes the refusal.
func (s *server) member(w http.ResponseWriter, r *http.Request) *session {
	sess := s.session(r)
	switch {
	case sess == nil:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"login": "/auth/login"})
	case !s.allowed(sess.User.Groups):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no access"})
	default:
		return sess
	}
	return nil
}
