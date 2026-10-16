package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Targets are where alerts go: the administrators' Teams channel (a
// Workflows webhook's address) and email. They're the platform's own
// Secret (storscale-alerts), so administrators change them without a
// redeploy:
//
//	teamsWebhook  https://...       a Teams channel's Workflows webhook
//	emailTo       a@x.com,b@x.com    who's emailed
//	smtpServer    smtp.x.com:587     and how (STARTTLS when it offers it)
//	smtpFrom, smtpUser, smtpPassword
type Targets struct {
	TeamsWebhook string
	EmailTo      []string
	SMTPServer   string
	SMTPFrom     string
	SMTPUser     string
	SMTPPassword string
}

// TargetsFrom reads them from the Secret's keys.
func TargetsFrom(sec map[string]string) Targets {
	t := Targets{TeamsWebhook: strings.TrimSpace(sec["teamsWebhook"]), SMTPServer: sec["smtpServer"],
		SMTPFrom: sec["smtpFrom"], SMTPUser: sec["smtpUser"], SMTPPassword: sec["smtpPassword"]}
	for _, a := range strings.Split(sec["emailTo"], ",") {
		if a = strings.TrimSpace(a); a != "" {
			t.EmailTo = append(t.EmailTo, a)
		}
	}
	return t
}

// Alert is one notice, as people read it.
type Alert struct {
	Title    string      `json:"title"`
	Severity string      `json:"severity"` // down, up, warning, test
	Facts    [][2]string `json:"facts"`    // label, value
	Link     string      `json:"link"`     // the Health page
	LinkText string      `json:"-"`        // its button; "Open the Health page" when empty
	At       time.Time   `json:"at"`
}

// FromChange is the alert for a service going down or coming back.
func FromChange(ch Change, link string) Alert {
	st := ch.Status
	a := Alert{Link: link, At: time.Now().UTC(), Facts: [][2]string{{"Service", st.Label}}}
	if st.State == Down {
		a.Title, a.Severity = st.Label+" is down", "down"
		a.Facts = append(a.Facts, [2]string{"Why", orUnknown(st.Reason)}, [2]string{"Affects", strings.Join(st.Features, ", ")})
	} else {
		a.Title, a.Severity = st.Label+" is back", "up"
		a.Facts = append(a.Facts, [2]string{"Affected", strings.Join(st.Features, ", ")})
	}
	return a
}

func orUnknown(s string) string {
	if s == "" {
		return "not known"
	}
	return s
}

// Notifier sends alerts to the targets, and keeps which services are muted.
type Notifier struct {
	Targets func(ctx context.Context) (Targets, error)
	HTTP    *http.Client

	mu    sync.Mutex
	muted map[string]time.Time // service -> until
	sent  []Alert              // the latest, newest first
}

// Mute stops a service's alerts until then; a zero time unmutes it.
func (n *Notifier) Mute(service string, until time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.muted == nil {
		n.muted = map[string]time.Time{}
	}
	if until.IsZero() {
		delete(n.muted, service)
	} else {
		n.muted[service] = until
	}
}

// Muted is until when a service's alerts are muted (zero: they aren't).
func (n *Notifier) Muted(service string) time.Time {
	n.mu.Lock()
	defer n.mu.Unlock()
	if t := n.muted[service]; time.Now().Before(t) {
		return t
	}
	return time.Time{}
}

// Recent are the latest alerts, newest first.
func (n *Notifier) Recent() []Alert {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Alert(nil), n.sent...)
}

// Send delivers an alert to every target; what failed, by target.
func (n *Notifier) Send(ctx context.Context, a Alert) map[string]error {
	n.mu.Lock()
	n.sent = append([]Alert{a}, n.sent...)
	if len(n.sent) > 50 {
		n.sent = n.sent[:50]
	}
	n.mu.Unlock()
	t, err := n.Targets(ctx)
	if err != nil {
		return map[string]error{"targets": err}
	}
	out := map[string]error{}
	if t.TeamsWebhook != "" {
		out["teams"] = n.teams(ctx, t.TeamsWebhook, a)
	}
	if len(t.EmailTo) > 0 && t.SMTPServer != "" {
		out["email"] = n.email(t, a)
	}
	return out
}

// Card is the alert as a Teams message: an Adaptive Card, which a Teams
// Workflows webhook posts to its channel.
func Card(a Alert) map[string]any {
	color := map[string]string{"down": "attention", "up": "good", "warning": "warning"}[a.Severity]
	if color == "" {
		color = "default"
	}
	var facts []map[string]string
	for _, f := range a.Facts {
		facts = append(facts, map[string]string{"title": f[0], "value": f[1]})
	}
	body := []any{
		map[string]any{"type": "TextBlock", "text": a.Title, "weight": "bolder", "size": "medium", "color": color, "wrap": true},
		map[string]any{"type": "FactSet", "facts": facts},
		map[string]any{"type": "TextBlock", "text": a.At.Format("2006-01-02 15:04 MST"), "isSubtle": true, "size": "small"},
	}
	card := map[string]any{"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard",
		"version": "1.4", "body": body}
	if a.Link != "" {
		text := a.LinkText
		if text == "" {
			text = "Open the Health page"
		}
		card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": text, "url": a.Link}}
	}
	return map[string]any{"type": "message", "attachments": []any{
		map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "content": card}}}
}

func (n *Notifier) teams(ctx context.Context, webhook string, a Alert) error {
	b, _ := json.Marshal(Card(a))
	req, err := http.NewRequestWithContext(ctx, "POST", webhook, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := n.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		// Never the webhook's address: it holds its signature, a credential.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("teams: %s", plain(err))
	}
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("teams: HTTP %d", res.StatusCode)
	}
	return nil
}

func (n *Notifier) email(t Targets, a Alert) error {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: [platform] %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n",
		t.SMTPFrom, strings.Join(t.EmailTo, ", "), a.Title)
	for _, f := range a.Facts {
		fmt.Fprintf(&b, "%s: %s\r\n", f[0], f[1])
	}
	fmt.Fprintf(&b, "\r\nAt %s.\r\n", a.At.Format("2006-01-02 15:04 MST"))
	if a.Link != "" {
		text := a.LinkText
		if text == "" {
			text = "Open the Health page"
		}
		fmt.Fprintf(&b, "%s: %s\r\n", text, a.Link)
	}
	var auth smtp.Auth
	if t.SMTPUser != "" {
		host, _, _ := net.SplitHostPort(t.SMTPServer)
		auth = smtp.PlainAuth("", t.SMTPUser, t.SMTPPassword, host)
	}
	if err := smtp.SendMail(t.SMTPServer, auth, t.SMTPFrom, t.EmailTo, []byte(b.String())); err != nil {
		return fmt.Errorf("email: %v", err)
	}
	return nil
}
