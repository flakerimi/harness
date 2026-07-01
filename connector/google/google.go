// Package google is a Google connector backed by an OAuth credential. It mirrors
// how Construct's backend integrates Google (OAuth2 + the Calendar/Gmail REST
// APIs), exposed to the harness as tools. v1 ships Calendar (read); the auth and
// HTTP plumbing generalize to Gmail next.
package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
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
	return connector.Status{Connected: true, Detail: "calendar + gmail (read + draft)"}
}

func (c *Connector) Tools(context.Context) ([]tool.Tool, error) {
	// Only advertise tools once connected — otherwise the agent would see
	// calendar/gmail tools that error on every call.
	if _, err := c.store.Load("google"); err != nil {
		return nil, nil
	}
	return []tool.Tool{
		&calendarListTool{c: c},
		&calendarGetTool{c: c},
		&gmailListTool{c: c},
		&gmailGetTool{c: c},
		&gmailDraftTool{c: c},
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

// post sends a JSON body to a Google API path with the bearer token — the write
// counterpart to get, used for creating Gmail drafts.
func (c *Connector) post(ctx context.Context, path string, jsonBody []byte) ([]byte, error) {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(c.apiBase, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
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

// --- Gmail (read) ---------------------------------------------------------

type gmailMessage struct {
	ID       string    `json:"id"`
	ThreadID string    `json:"threadId"`
	Snippet  string    `json:"snippet"`
	LabelIDs []string  `json:"labelIds"`
	Payload  gmailPart `json:"payload"`
}

type gmailPart struct {
	MimeType string        `json:"mimeType"`
	Headers  []gmailHeader `json:"headers"`
	Body     gmailBody     `json:"body"`
	Parts    []gmailPart   `json:"parts"`
}

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailBody struct {
	Data string `json:"data"`
}

func header(headers []gmailHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// findPlainText walks the MIME tree for the first text/plain body.
func findPlainText(p gmailPart) string {
	if strings.HasPrefix(p.MimeType, "text/plain") && p.Body.Data != "" {
		return decodeB64(p.Body.Data)
	}
	for _, sub := range p.Parts {
		if txt := findPlainText(sub); txt != "" {
			return txt
		}
	}
	return ""
}

// decodeB64 decodes Gmail's URL-safe base64 (with or without padding).
func decodeB64(s string) string {
	s = strings.TrimSpace(s)
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return string(b)
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return string(b)
	}
	return ""
}

type gmailListTool struct{ c *Connector }

func (gmailListTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "gmail_list_messages",
		Description: "Search Gmail and list matching messages (sender, subject, date, snippet). Use Gmail search syntax in `query`, e.g. \"is:unread\", \"from:boss@x.com newer_than:7d\", \"has:attachment\".",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "Gmail search query (default: in:inbox)."},
				"max_results": map[string]any{"type": "integer", "description": "Max messages (default 10, cap 25)."},
			},
		},
	}
}

func (t *gmailListTool) Run(ctx context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	_ = json.Unmarshal(input, &args)

	max := args.MaxResults
	if max <= 0 || max > 25 {
		max = 10
	}
	q := url.Values{}
	q.Set("q", orDefault(args.Query, "in:inbox"))
	q.Set("maxResults", strconv.Itoa(max))

	body, err := t.c.get(ctx, "/gmail/v1/users/me/messages", q)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return tool.Result{Content: "parse: " + err.Error(), IsError: true}, nil
	}
	if len(list.Messages) == 0 {
		return tool.Result{Content: "no messages match"}, nil
	}

	var b strings.Builder
	for _, m := range list.Messages {
		mq := url.Values{}
		mq.Set("format", "metadata")
		mq.Add("metadataHeaders", "From")
		mq.Add("metadataHeaders", "Subject")
		mq.Add("metadataHeaders", "Date")
		raw, err := t.c.get(ctx, "/gmail/v1/users/me/messages/"+url.PathEscape(m.ID), mq)
		if err != nil {
			fmt.Fprintf(&b, "• (could not load %s: %v)\n\n", m.ID, err)
			continue
		}
		var msg gmailMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		fmt.Fprintf(&b, "• %s\n", orDefault(strings.TrimSpace(header(msg.Payload.Headers, "Subject")), "(no subject)"))
		fmt.Fprintf(&b, "  from: %s · %s\n", header(msg.Payload.Headers, "From"), header(msg.Payload.Headers, "Date"))
		if msg.Snippet != "" {
			fmt.Fprintf(&b, "  %s\n", clipText(msg.Snippet, 200))
		}
		fmt.Fprintf(&b, "  id: %s\n\n", msg.ID)
	}
	return tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

type gmailGetTool struct{ c *Connector }

func (gmailGetTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "gmail_get_message",
		Description: "Get one Gmail message by id, with headers and the plain-text body.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message_id": map[string]any{"type": "string", "description": "The message id (from gmail_list_messages)."},
			},
			"required": []string{"message_id"},
		},
	}
}

func (t *gmailGetTool) Run(ctx context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if args.MessageID == "" {
		return tool.Result{Content: "message_id is required", IsError: true}, nil
	}
	q := url.Values{}
	q.Set("format", "full")
	body, err := t.c.get(ctx, "/gmail/v1/users/me/messages/"+url.PathEscape(args.MessageID), q)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var msg gmailMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return tool.Result{Content: "parse: " + err.Error(), IsError: true}, nil
	}
	h := msg.Payload.Headers
	var b strings.Builder
	fmt.Fprintf(&b, "Subject: %s\n", orDefault(header(h, "Subject"), "(no subject)"))
	fmt.Fprintf(&b, "From: %s\n", header(h, "From"))
	if to := header(h, "To"); to != "" {
		fmt.Fprintf(&b, "To: %s\n", to)
	}
	fmt.Fprintf(&b, "Date: %s\n", header(h, "Date"))
	fmt.Fprintf(&b, "id: %s\n", msg.ID)
	// Surface threading handles so a reply draft can attach to this thread.
	fmt.Fprintf(&b, "thread_id: %s\n", msg.ThreadID)
	if mid := header(h, "Message-ID"); mid != "" {
		fmt.Fprintf(&b, "message_id: %s\n", mid)
	}
	b.WriteString("\n")

	if text := findPlainText(msg.Payload); text != "" {
		fmt.Fprint(&b, clipText(strings.TrimSpace(text), 4000))
	} else {
		fmt.Fprintf(&b, "(no plain-text body — snippet: %s)", msg.Snippet)
	}
	return tool.Result{Content: b.String()}, nil
}

// clipText shortens s to n bytes with an ellipsis.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- Gmail (draft) --------------------------------------------------------

type gmailDraftTool struct{ c *Connector }

func (gmailDraftTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "gmail_create_draft",
		Description: "Create a Gmail draft. It is NOT sent — it waits in Drafts for the user to review and send. For a reply, pass thread_id and in_reply_to from gmail_get_message so it threads correctly (and keep the original subject with a 'Re: ' prefix).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":          map[string]any{"type": "string", "description": "Recipient email address."},
				"subject":     map[string]any{"type": "string", "description": "Subject line."},
				"body":        map[string]any{"type": "string", "description": "Plain-text body of the draft."},
				"thread_id":   map[string]any{"type": "string", "description": "Optional Gmail thread id (from gmail_get_message) to attach a reply to its thread."},
				"in_reply_to": map[string]any{"type": "string", "description": "Optional Message-ID of the message being replied to, for correct threading."},
			},
			"required": []string{"to", "subject", "body"},
		},
	}
}

func (t *gmailDraftTool) Run(ctx context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		To        string `json:"to"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
		ThreadID  string `json:"thread_id"`
		InReplyTo string `json:"in_reply_to"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.To) == "" || strings.TrimSpace(args.Subject) == "" || strings.TrimSpace(args.Body) == "" {
		return tool.Result{Content: "to, subject, and body are all required", IsError: true}, nil
	}

	raw := base64.URLEncoding.EncodeToString([]byte(buildRawMessage(args.To, args.Subject, args.Body, args.InReplyTo)))
	msg := map[string]any{"raw": raw}
	if args.ThreadID != "" {
		msg["threadId"] = args.ThreadID
	}
	payload, err := json.Marshal(map[string]any{"message": msg})
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}

	body, err := t.c.post(ctx, "/gmail/v1/users/me/drafts", payload)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var draft struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &draft)
	return tool.Result{Content: fmt.Sprintf("created draft %s to %s — review and send it in Gmail", orDefault(draft.ID, "(saved)"), args.To)}, nil
}

// buildRawMessage assembles an RFC 2822 message for the Gmail drafts API. The
// subject is MIME-encoded only when it isn't plain ASCII, so unicode subjects
// survive; reply headers are added when in_reply_to is given.
func buildRawMessage(to, subject, body, inReplyTo string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", inReplyTo)
		fmt.Fprintf(&b, "References: %s\r\n", inReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String()
}
