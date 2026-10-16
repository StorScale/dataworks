// Package health watches the platform's services: each one's own health
// endpoint, checked every few seconds, the same in Compose and on
// Kubernetes. On Kubernetes, a service that's down gets its reason from its
// pods (their state, and their latest warning event). Changes (a service
// down, or back) go to the alerts (Notify).
package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// A Check is one service, as people know it, and how to tell it's up.
type Check struct {
	Name     string   `json:"name"`     // its Service, and its pods' app.kubernetes.io/name
	Label    string   `json:"label"`    // as people know it
	Features []string `json:"features"` // what people lose when it's down
	URL      string   `json:"-"`        // http(s)://...: up when it answers as OK says; tcp://host:port: when it accepts
	// Selector finds its pods on Kubernetes; default app.kubernetes.io/name=<Name>.
	Selector string                                       `json:"-"`
	OK       func(status int, body string) (bool, string) `json:"-"` // nil: 2xx is up
}

// State is a check's last result.
type State string

const (
	Up      State = "up"
	Down    State = "down"
	Unknown State = "unknown" // not checked yet
)

// Status is a service's state, and since when.
type Status struct {
	Check
	State  State     `json:"state"`
	Reason string    `json:"reason,omitempty"` // why it's down, as plainly as known
	Since  time.Time `json:"since"`
	Pods   []Pod     `json:"pods,omitempty"` // on Kubernetes, when it's down
	fails  int       // checks failed in a row
}

// Pod is what Kubernetes says of one of a service's pods.
type Pod struct {
	Name    string `json:"name"`
	Node    string `json:"node,omitempty"`
	Phase   string `json:"phase"`
	Waiting string `json:"waiting,omitempty"` // a container's reason: ImagePullBackOff, CrashLoopBackOff...
	Event   string `json:"event,omitempty"`   // its latest warning: FailedMount: ... can't read superblock
}

// Pods looks up a service's pods (Kubernetes); nil in Compose.
type Pods interface {
	Pods(ctx context.Context, service string) ([]Pod, error)
}

// A Change is a service going down (after Confirm failed checks in a row),
// or coming back.
type Change struct {
	Status Status
	Was    State
}

// Checker checks its services every Every, and sends changes to Notify.
type Checker struct {
	Checks  []Check
	Every   time.Duration // 15s
	Timeout time.Duration // 5s
	Confirm int           // failed checks in a row before a service is down (2)
	Pods    Pods          // nil: no reasons from Kubernetes
	Notify  func(Change)  // nil: none
	// Quiet is how long after starting no change is sent: the platform's
	// services start too, and aren't down.
	Quiet time.Duration
	HTTP  *http.Client

	mu      sync.Mutex
	status  map[string]*Status
	started time.Time
}

func (c *Checker) init() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status != nil {
		return
	}
	c.status = map[string]*Status{}
	c.started = time.Now()
	for _, ch := range c.Checks {
		c.status[ch.Name] = &Status{Check: ch, State: Unknown, Since: c.started}
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if c.Confirm <= 0 {
		c.Confirm = 2
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
}

// Run checks until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	c.init()
	every := c.Every
	if every <= 0 {
		every = 15 * time.Second
	}
	for {
		c.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Once checks every service once, at the same time.
func (c *Checker) Once(ctx context.Context) {
	c.init()
	type result struct {
		name   string
		ok     bool
		reason string
	}
	results := make(chan result, len(c.Checks))
	for _, ch := range c.Checks {
		go func(ch Check) {
			ok, reason := c.probe(ctx, ch)
			results <- result{ch.Name, ok, reason}
		}(ch)
	}
	var changes []Change
	for range c.Checks {
		r := <-results
		if ch := c.record(ctx, r.name, r.ok, r.reason); ch != nil {
			changes = append(changes, *ch)
		}
	}
	if c.Notify != nil && time.Since(c.started) >= c.Quiet {
		for _, ch := range changes {
			c.Notify(ch)
		}
	}
}

func (c *Checker) probe(ctx context.Context, ch Check) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	if addr, ok := strings.CutPrefix(ch.URL, "tcp://"); ok {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return false, plain(err)
		}
		conn.Close()
		return true, ""
	}
	req, err := http.NewRequestWithContext(ctx, "GET", ch.URL, nil)
	if err != nil {
		return false, err.Error()
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return false, plain(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if ch.OK != nil {
		return ch.OK(res.StatusCode, string(body))
	}
	if res.StatusCode/100 != 2 {
		return false, fmt.Sprintf("its health check answers HTTP %d", res.StatusCode)
	}
	return true, ""
}

// plain is a connection error in a few words.
func plain(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "no such host"):
		return "it isn't running (its name doesn't resolve)"
	case strings.Contains(s, "connection refused"):
		return "it isn't accepting connections (starting, or stopped)"
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "timeout"):
		return "its health check didn't answer in time"
	}
	return s
}

// record keeps a check's result; a Change when the service went down
// (Confirm failures in a row) or came back.
func (c *Checker) record(ctx context.Context, name string, ok bool, reason string) *Change {
	c.mu.Lock()
	st := c.status[name]
	was := st.State
	now := time.Now()
	if ok {
		st.fails, st.Reason, st.Pods = 0, "", nil
		if st.State != Up {
			st.State, st.Since = Up, now
		}
		c.mu.Unlock()
		if was == Down {
			return &Change{Status: *st, Was: was}
		}
		return nil
	}
	st.fails++
	st.Reason = reason
	confirmed := st.fails >= c.Confirm
	c.mu.Unlock()
	var pods []Pod
	if c.Pods != nil && confirmed {
		pods, _ = c.Pods.Pods(ctx, name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st.Pods = pods
	if why := podReason(pods); why != "" {
		st.Reason = why
	}
	if confirmed && st.State != Down {
		st.State, st.Since = Down, now
		return &Change{Status: *st, Was: was}
	}
	return nil
}

// podReason is the plainest reason a service's pods give.
func podReason(pods []Pod) string {
	for _, p := range pods {
		switch {
		case p.Event != "":
			return fmt.Sprintf("pod %s on %s: %s", p.Name, p.Node, p.Event)
		case p.Waiting != "":
			return fmt.Sprintf("pod %s: %s", p.Name, p.Waiting)
		}
	}
	return ""
}

// Statuses are every service's, sorted by label.
func (c *Checker) Statuses() []Status {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Status, 0, len(c.status))
	for _, st := range c.status {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// Metrics are the statuses in Prometheus' text format: platform_service_up
// 1 or 0 for each service (none while it's unknown).
func (c *Checker) Metrics() string {
	var b strings.Builder
	b.WriteString("# HELP platform_service_up Whether a platform service answers its health check (1) or not (0).\n")
	b.WriteString("# TYPE platform_service_up gauge\n")
	for _, st := range c.Statuses() {
		if st.State == Unknown {
			continue
		}
		v := 0
		if st.State == Up {
			v = 1
		}
		fmt.Fprintf(&b, "platform_service_up{service=%q} %d\n", st.Name, v)
	}
	return b.String()
}
