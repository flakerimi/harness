// Package telegram is a chat channel: it bridges a Telegram bot to the harness
// engine, so an identity is reachable from your phone. It is transport-only —
// the per-message reply is produced by an injected Responder (the CLI wires in
// a profile/session-backed agent), keeping this package free of agent wiring.
//
// It speaks the Telegram Bot API over plain HTTP long-polling (getUpdates) and
// sends replies with sendMessage. No SDK, stdlib only.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Responder turns an inbound message (from a chat) into a reply. Returning an
// empty string sends nothing.
type Responder func(ctx context.Context, chatID int64, user, text string) string

// Bot is a Telegram bot client + long-poll loop.
type Bot struct {
	token   string
	apiBase string // default https://api.telegram.org (overridable for tests)
	http    *http.Client
	offset  int64 // last processed update_id + 1
}

// New builds a bot for the given token.
func New(token string) *Bot {
	return &Bot{
		token:   token,
		apiBase: "https://api.telegram.org",
		http:    &http.Client{Timeout: 70 * time.Second},
	}
}

// Update is one Telegram update.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is an inbound chat message.
type Message struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	} `json:"from"`
}

func (m *Message) sender() string {
	if m.From.Username != "" {
		return "@" + m.From.Username
	}
	return m.From.FirstName
}

// GetUpdates long-polls for new updates starting at the current offset.
func (b *Bot) GetUpdates(ctx context.Context, timeoutSecs int) ([]Update, error) {
	q := url.Values{}
	q.Set("offset", strconv.FormatInt(b.offset, 10))
	q.Set("timeout", strconv.Itoa(timeoutSecs))
	raw, err := b.call(ctx, "getUpdates", q)
	if err != nil {
		return nil, err
	}
	var out struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("telegram getUpdates: %w", err)
	}
	for _, u := range out.Result {
		if u.UpdateID >= b.offset {
			b.offset = u.UpdateID + 1
		}
	}
	return out.Result, nil
}

// Command is a bot command advertised in Telegram's UI (the "/" menu and
// autocomplete). Command is the name without a leading slash, lowercase.
type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands registers the command menu (Telegram setMyCommands), so the
// commands surface in the client's command list instead of having to be known
// in advance. Safe to call on every startup — it just overwrites the list.
func (b *Bot) SetCommands(ctx context.Context, cmds []Command) error {
	payload, err := json.Marshal(cmds)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("commands", string(payload))
	_, err = b.call(ctx, "setMyCommands", q)
	return err
}

// Send delivers a text message to a chat.
func (b *Bot) Send(ctx context.Context, chatID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	q := url.Values{}
	q.Set("chat_id", strconv.FormatInt(chatID, 10))
	q.Set("text", text)
	_, err := b.call(ctx, "sendMessage", q)
	return err
}

func (b *Bot) call(ctx context.Context, method string, form url.Values) ([]byte, error) {
	u := fmt.Sprintf("%s/bot%s/%s", strings.TrimRight(b.apiBase, "/"), b.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram %s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// Run long-polls and dispatches each text message to the responder, sending the
// reply back. It returns when ctx is cancelled. Transient poll errors are
// reported and retried after a short back-off so the loop survives blips.
func (b *Bot) Run(ctx context.Context, h Responder, onError func(error)) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updates, err := b.GetUpdates(ctx, 50)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if onError != nil {
				onError(err)
			}
			if !sleep(ctx, 3*time.Second) {
				return ctx.Err()
			}
			continue
		}
		for _, u := range updates {
			if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
				continue
			}
			reply := h(ctx, u.Message.Chat.ID, u.Message.sender(), u.Message.Text)
			if err := b.Send(ctx, u.Message.Chat.ID, reply); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

// sleep waits d or until ctx is done; it reports false if ctx was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
