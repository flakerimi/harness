package main

import (
	"context"
	"strings"
	"testing"
)

func TestDeliverValidation(t *testing.T) {
	ctx := context.Background()

	// Empty text is a no-op, never an error (nothing to send).
	if err := deliver(ctx, "telegram:123", ""); err != nil {
		t.Errorf("empty text should be a no-op, got %v", err)
	}

	// Malformed / unsupported targets error before any network call.
	cases := map[string]string{
		"no colon":     "telegramchat",
		"empty dest":   "telegram:",
		"unknown kind": "discord:123",
	}
	for name, target := range cases {
		if err := deliver(ctx, target, "hello"); err == nil {
			t.Errorf("%s: expected an error for target %q", name, target)
		}
	}
}

func TestDeliverTelegramNeedsToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	err := deliver(context.Background(), "telegram:123", "hello")
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("expected a missing-token error, got %v", err)
	}
}
