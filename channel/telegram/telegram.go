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
	"unicode/utf8"
)

// Responder turns an inbound message (from a chat) into a reply. text is the
// message text (a media message's caption); images holds any downloaded photo
// or image-file attachment, nil for plain text. Returning an empty string
// sends nothing.
type Responder func(ctx context.Context, chatID int64, user, text string, images []Image) string

// Image is a downloaded inbound photo or image file, ready to hand to a
// multimodal model. MimeType is what Telegram reports — "image/jpeg" for
// photos, which Telegram always recompresses to JPEG.
type Image struct {
	MimeType string
	Data     []byte
}

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

// Update is one Telegram update — a chat message or a tapped inline button.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// Message is an inbound chat message. A media message carries its text in
// Caption, not Text — Telegram never sets both.
type Message struct {
	MessageID int64       `json:"message_id"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	Photo     []PhotoSize `json:"photo"`    // renditions of one photo, varying size
	Document  *Document   `json:"document"` // a file attachment (may be an image)
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	} `json:"from"`
}

// PhotoSize is one rendition of an inbound photo.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// Document is a file attachment. Images sent "as file" arrive here at full
// quality instead of as recompressed Photo renditions.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// text returns the message's textual content — Text for plain messages, the
// caption for media messages.
func (m *Message) text() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

// imageFile picks the message's image content: the largest photo rendition, or
// a document with an image mime type. ok is false when the message carries no
// image (stickers, voice notes, non-image files).
func (m *Message) imageFile() (fileID, mime string, ok bool) {
	if len(m.Photo) > 0 {
		best := m.Photo[0]
		for _, p := range m.Photo[1:] {
			if p.Width*p.Height > best.Width*best.Height {
				best = p
			}
		}
		return best.FileID, "image/jpeg", true
	}
	if m.Document != nil && strings.HasPrefix(m.Document.MimeType, "image/") {
		return m.Document.FileID, m.Document.MimeType, true
	}
	return "", "", false
}

// CallbackQuery is the event fired when a user taps an inline-keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"` // the button's callback_data
	Message *Message `json:"message"`
	From    struct {
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	} `json:"from"`
}

func (c *CallbackQuery) sender() string {
	if c.From.Username != "" {
		return "@" + c.From.Username
	}
	return c.From.FirstName
}

// Button is one inline-keyboard button: a label plus either the opaque data
// sent back when it's tapped (max 64 bytes) or a URL the client opens.
type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
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

// Send delivers a plain-text message to a chat.
func (b *Bot) Send(ctx context.Context, chatID int64, text string) error {
	return b.sendMessage(ctx, chatID, text, nil, "")
}

// SendChatAction shows a transient status in the chat (e.g. "typing"). Telegram
// clears it after ~5s, so callers repeat it during longer operations.
func (b *Bot) SendChatAction(ctx context.Context, chatID int64, action string) error {
	q := url.Values{}
	q.Set("chat_id", strconv.FormatInt(chatID, 10))
	q.Set("action", action)
	_, err := b.call(ctx, "sendChatAction", q)
	return err
}

// SendKeyboard sends a plain-text message with an inline keyboard.
func (b *Bot) SendKeyboard(ctx context.Context, chatID int64, text string, rows [][]Button) error {
	return b.sendMessage(ctx, chatID, text, rows, "")
}

// SendMarkdown renders md (the model's markdown) as Telegram HTML and sends it
// with an inline keyboard. If Telegram rejects the HTML, it retries as plain
// text so a reply is never dropped over a formatting glitch.
func (b *Bot) SendMarkdown(ctx context.Context, chatID int64, md string, rows [][]Button) error {
	if strings.TrimSpace(md) == "" {
		return nil
	}
	if err := b.sendMessage(ctx, chatID, MarkdownToHTML(md), rows, "HTML"); err != nil {
		return b.sendMessage(ctx, chatID, md, rows, "")
	}
	return nil
}

// telegramMaxUnits is our per-message budget under Telegram's hard sendMessage
// limit of 4096 UTF-16 code units — anything longer is rejected with 400
// "message is too long", which silently ate long task deliveries.
const telegramMaxUnits = 4000

// sendMessage is the shared sendMessage call: optional inline keyboard and
// parse mode ("" = plain, "HTML", "MarkdownV2"). Empty text is a no-op. Long
// text is split into chunks (preferring line breaks) so a lengthy reply or a
// delivered task result is never dropped; the keyboard rides the last chunk.
func (b *Bot) sendMessage(ctx context.Context, chatID int64, text string, rows [][]Button, parseMode string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	chunks := splitMessage(text, telegramMaxUnits)
	for i, chunk := range chunks {
		q := url.Values{}
		q.Set("chat_id", strconv.FormatInt(chatID, 10))
		q.Set("text", chunk)
		if parseMode != "" {
			q.Set("parse_mode", parseMode)
		}
		if i == len(chunks)-1 {
			if err := setMarkup(q, rows); err != nil {
				return err
			}
		}
		if _, err := b.call(ctx, "sendMessage", q); err != nil {
			return err
		}
	}
	return nil
}

// splitMessage cuts text into pieces of at most maxUnits UTF-16 code units
// (Telegram's unit of account — astral runes like emoji count as two),
// breaking at the last newline in the window when one exists past its middle,
// else the last space, else a hard cut.
func splitMessage(s string, maxUnits int) []string {
	var chunks []string
	for s != "" {
		units, cut := 0, 0
		lastNL, lastSP := -1, -1
		for i, r := range s {
			u := 1
			if r > 0xFFFF {
				u = 2
			}
			if units+u > maxUnits {
				break
			}
			units += u
			cut = i + utf8.RuneLen(r)
			switch r {
			case '\n':
				lastNL = cut
			case ' ':
				lastSP = cut
			}
		}
		if cut >= len(s) {
			chunks = append(chunks, s)
			break
		}
		end := cut
		if lastNL > cut/2 {
			end = lastNL
		} else if lastSP > cut/2 {
			end = lastSP
		}
		chunks = append(chunks, strings.TrimRight(s[:end], "\n "))
		s = strings.TrimLeft(s[end:], "\n ")
	}
	return chunks
}

// maxFileBytes caps a file download at the Bot API's own getFile limit —
// Telegram refuses to serve anything larger to a bot, so reading past it can
// only mean a truncated file.
const maxFileBytes = 20 << 20

// DownloadFile resolves a file_id via getFile and fetches the file's bytes
// from Telegram's file endpoint.
func (b *Bot) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	q := url.Values{}
	q.Set("file_id", fileID)
	raw, err := b.call(ctx, "getFile", q)
	if err != nil {
		return nil, err
	}
	var out struct {
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("telegram getFile: %w", err)
	}
	if out.Result.FilePath == "" {
		return nil, fmt.Errorf("telegram getFile: no file_path for id %q", fileID)
	}
	u := fmt.Sprintf("%s/file/bot%s/%s", strings.TrimRight(b.apiBase, "/"), b.token, out.Result.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("telegram file download: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFileBytes))
}

// EditMessage replaces a message's text and inline keyboard in place — used to
// drill from the provider list into a provider's models without new messages.
func (b *Bot) EditMessage(ctx context.Context, chatID, messageID int64, text string, rows [][]Button) error {
	q := url.Values{}
	q.Set("chat_id", strconv.FormatInt(chatID, 10))
	q.Set("message_id", strconv.FormatInt(messageID, 10))
	q.Set("text", text)
	if err := setMarkup(q, rows); err != nil {
		return err
	}
	_, err := b.call(ctx, "editMessageText", q)
	return err
}

// AnswerCallback acknowledges a tapped button (stops the client's spinner). An
// optional toast text may be shown.
func (b *Bot) AnswerCallback(ctx context.Context, callbackID, toast string) error {
	q := url.Values{}
	q.Set("callback_query_id", callbackID)
	if toast != "" {
		q.Set("text", toast)
	}
	_, err := b.call(ctx, "answerCallbackQuery", q)
	return err
}

// setMarkup attaches an inline_keyboard reply_markup to a form.
func setMarkup(q url.Values, rows [][]Button) error {
	if rows == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"inline_keyboard": rows})
	if err != nil {
		return err
	}
	q.Set("reply_markup", string(payload))
	return nil
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

// CallbackHandler handles a tapped inline button. It receives the chat and the
// message the button is attached to (so it can EditMessage), the callbackID (to
// AnswerCallback), the button's data, and the sender.
type CallbackHandler func(ctx context.Context, chatID, messageID int64, callbackID, data, user string)

// Run long-polls and dispatches messages to the responder and button taps to
// onCallback (may be nil), sending replies back. A message's photo (largest
// rendition) or image-file attachment is downloaded and handed to the
// responder alongside the text. It returns when ctx is cancelled. Transient
// poll errors are reported and retried after a short back-off so the loop
// survives blips.
func (b *Bot) Run(ctx context.Context, h Responder, onCallback CallbackHandler, onError func(error)) error {
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
			switch {
			case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
				if onCallback != nil {
					cq := u.CallbackQuery
					onCallback(ctx, cq.Message.Chat.ID, cq.Message.MessageID, cq.ID, cq.Data, cq.sender())
				}
			case u.Message != nil:
				m := u.Message
				text := m.text()
				var images []Image
				if fileID, mime, ok := m.imageFile(); ok {
					data, derr := b.DownloadFile(ctx, fileID)
					if derr != nil {
						if onError != nil {
							onError(fmt.Errorf("image download: %w", derr))
						}
						if strings.TrimSpace(text) == "" {
							// Nothing usable survived — say so rather than
							// leaving the message silently unanswered.
							_ = b.Send(ctx, m.Chat.ID, "⚠ I couldn't download that image — please try sending it again.")
							continue
						}
					} else {
						images = append(images, Image{MimeType: mime, Data: data})
					}
				}
				if strings.TrimSpace(text) == "" && len(images) == 0 {
					continue // stickers, voice notes, member joins — nothing to answer
				}
				reply := h(ctx, m.Chat.ID, m.sender(), text, images)
				if err := b.Send(ctx, m.Chat.ID, reply); err != nil && onError != nil {
					onError(err)
				}
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
