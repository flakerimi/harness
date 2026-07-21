// Package webhook delivers text to incoming-webhook URLs. The payload carries
// both "text" (Slack, Mattermost, Teams) and "content" (Discord) keys — each
// service reads its own and ignores the other, so one kind covers them all.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Deliverer POSTs text to an incoming-webhook URL as JSON; dest is the URL.
type Deliverer struct{}

func (Deliverer) Deliver(ctx context.Context, dest, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text, "content": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dest, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("webhook %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
