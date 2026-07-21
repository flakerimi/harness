package telegram

import (
	"context"
	"fmt"
	"os"
	"strconv"
)

// Deliverer sends scheduled/background output to a Telegram chat; dest is the
// numeric chat id. The bot token is read from $TELEGRAM_BOT_TOKEN at delivery
// time, not construction, so a long-lived daemon picks up rotated credentials
// without a restart.
type Deliverer struct{}

func (Deliverer) Deliver(ctx context.Context, dest, text string) error {
	tok := os.Getenv("TELEGRAM_BOT_TOKEN")
	if tok == "" {
		return fmt.Errorf("telegram delivery needs $TELEGRAM_BOT_TOKEN")
	}
	chatID, err := strconv.ParseInt(dest, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram chat id %q: %w", dest, err)
	}
	return New(tok).Send(ctx, chatID, text)
}
