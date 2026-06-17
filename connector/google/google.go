// Package google is a Google connector backed by an OAuth credential. It mirrors
// how Construct's backend integrates Google (OAuth2 + the Calendar/Gmail REST
// APIs), exposed to the harness as tools. v1 ships Calendar (read); the auth and
// HTTP plumbing generalize to Gmail next.
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/connector"
	"github.com/flakerimi/harness/tool"
)

// Connector accesses Google APIs with a stored OAuth credential.
type Connector struct {
	store   *auth.Store
	tokens  *auth.GoogleTokenSource
	http    *http.Client
	apiBase string // default https://www.googleapis.com (overridable for tests)
}

// New builds a Google connector over the credential store + OAuth client.
func New(store *auth.Store, clientID, clientSecret string) *Connector {
	return &Connector{
		store:   store,
		tokens:  auth.NewGoogleTokenSource(store, clientID, clientSecret),
		http:    &http.Client{Timeout: 30 * time.Second},
		apiBase: "https://www.googleapis.com",
	}
}

func (c *Connector) Name() string { return "google" }

func (c *Connector) Status(context.Context) connector.Status {
	if _, err := c.store.Load("google"); err != nil {
		return connector.Status{Connected: false, Detail: "not logged in — run: harness login -provider google"}
	}
	return connector.Status{Connected: true, Detail: "calendar (read)"}
}

func (c *Connector) Tools(context.Context) ([]tool.Tool, error) {
	// Only advertise tools once connected — otherwise the agent would see
	// calendar tools that error on every call.
	if _, err := c.store.Load("google"); err != nil {
		return nil, nil
	}
	return []tool.Tool{
		&calendarListTool{c: c},
		&calendarGetTool{c: c},
	}, nil
}

func (c *Connector) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(c.apiBase, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

type calEvent struct {
	ID          string  `json:"id"`
	Summary     string  `json:"summary"`
	Description string  `json:"description"`
	Location    string  `json:"location"`
	HangoutLink string  `json:"hangoutLink"`
	Start       calTime `json:"start"`
	End         calTime `json:"end"`
	Organizer   struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"organizer"`
	Attendees []struct {
		Email          string `json:"email"`
		DisplayName    string `json:"displayName"`
		ResponseStatus string `json:"responseStatus"`
	} `json:"attendees"`
}

type calTime struct {
	DateTime string `json:"dateTime"`
	Date     string `json:"date"`
}

func (t calTime) val() string {
	if t.DateTime != "" {
		return t.DateTime
	}
	return t.Date
}

func formatEvent(e calEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "• %s\n", strings.TrimSpace(e.Summary))
	fmt.Fprintf(&b, "  when: %s → %s\n", e.Start.val(), e.End.val())
	if e.Location != "" {
		fmt.Fprintf(&b, "  where: %s\n", e.Location)
	}
	if e.HangoutLink != "" {
		fmt.Fprintf(&b, "  meet: %s\n", e.HangoutLink)
	}
	if e.Organizer.Email != "" {
		fmt.Fprintf(&b, "  organizer: %s\n", contact(e.Organizer.DisplayName, e.Organizer.Email))
	}
	if len(e.Attendees) > 0 {
		parts := make([]string, 0, len(e.Attendees))
		for _, a := range e.Attendees {
			parts = append(parts, contact(a.DisplayName, a.Email))
		}
		fmt.Fprintf(&b, "  attendees: %s\n", strings.Join(parts, ", "))
	}
	if e.ID != "" {
		fmt.Fprintf(&b, "  id: %s\n", e.ID)
	}
	if e.Description != "" {
		d := strings.ReplaceAll(strings.TrimSpace(e.Description), "\n", " ")
		if len(d) > 500 {
			d = d[:500] + "…"
		}
		fmt.Fprintf(&b, "  notes: %s\n", d)
	}
	return b.String()
}

func contact(name, email string) string {
	switch {
	case name != "" && email != "":
		return name + " <" + email + ">"
	case email != "":
		return email
	default:
		return name
	}
}

type calendarListTool struct{ c *Connector }

func (calendarListTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "calendar_list_events",
		Description: "List Google Calendar events in a time range (defaults to the next 7 days). Returns title, time, location, organizer, and attendees.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"time_min":    map[string]any{"type": "string", "description": "RFC3339 start (default: now)."},
				"time_max":    map[string]any{"type": "string", "description": "RFC3339 end (default: now + 7 days)."},
				"max_results": map[string]any{"type": "integer", "description": "Max events (default 10, cap 50)."},
				"calendar_id": map[string]any{"type": "string", "description": "Calendar id (default 'primary')."},
				"query":       map[string]any{"type": "string", "description": "Free-text search filter."},
			},
		},
	}
}

func (t *calendarListTool) Run(ctx context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		TimeMin    string `json:"time_min"`
		TimeMax    string `json:"time_max"`
		MaxResults int    `json:"max_results"`
		CalendarID string `json:"calendar_id"`
		Query      string `json:"query"`
	}
	_ = json.Unmarshal(input, &args)

	cal := args.CalendarID
	if cal == "" {
		cal = "primary"
	}
	now := time.Now()
	q := url.Values{}
	q.Set("timeMin", orDefault(args.TimeMin, now.Format(time.RFC3339)))
	q.Set("timeMax", orDefault(args.TimeMax, now.Add(7*24*time.Hour).Format(time.RFC3339)))
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	maxResults := args.MaxResults
	if maxResults <= 0 || maxResults > 50 {
		maxResults = 10
	}
	q.Set("maxResults", strconv.Itoa(maxResults))
	if args.Query != "" {
		q.Set("q", args.Query)
	}

	body, err := t.c.get(ctx, "/calendar/v3/calendars/"+url.PathEscape(cal)+"/events", q)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var out struct {
		Items []calEvent `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return tool.Result{Content: "parse: " + err.Error(), IsError: true}, nil
	}
	if len(out.Items) == 0 {
		return tool.Result{Content: "no events in range"}, nil
	}
	var b strings.Builder
	for _, e := range out.Items {
		b.WriteString(formatEvent(e))
		b.WriteByte('\n')
	}
	return tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

type calendarGetTool struct{ c *Connector }

func (calendarGetTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "calendar_get_event",
		Description: "Get one Google Calendar event by id, with full details and attendees.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"event_id":    map[string]any{"type": "string", "description": "The event id."},
				"calendar_id": map[string]any{"type": "string", "description": "Calendar id (default 'primary')."},
			},
			"required": []string{"event_id"},
		},
	}
}

func (t *calendarGetTool) Run(ctx context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		EventID    string `json:"event_id"`
		CalendarID string `json:"calendar_id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if args.EventID == "" {
		return tool.Result{Content: "event_id is required", IsError: true}, nil
	}
	cal := args.CalendarID
	if cal == "" {
		cal = "primary"
	}
	body, err := t.c.get(ctx, "/calendar/v3/calendars/"+url.PathEscape(cal)+"/events/"+url.PathEscape(args.EventID), nil)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var e calEvent
	if err := json.Unmarshal(body, &e); err != nil {
		return tool.Result{Content: "parse: " + err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: formatEvent(e)}, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
