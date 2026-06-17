package main

import (
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/session"
)

func TestTelegramCommandSwitchModel(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := &session.Session{ID: "tg-1"}

	// A normal message is not a command.
	if _, isCmd := telegramCommand(store, sess, "hello there", "claude"); isCmd {
		t.Fatal("plain text should not be treated as a command")
	}

	// /model with a provider + explicit model switches and persists.
	reply, isCmd := telegramCommand(store, sess, "/model deepseek deepseek-reasoner", "claude")
	if !isCmd {
		t.Fatal("/model should be a command")
	}
	if !strings.Contains(reply, "deepseek") {
		t.Errorf("reply should confirm switch: %q", reply)
	}
	if sess.Provider != "deepseek" || sess.Model != "deepseek-reasoner" {
		t.Errorf("session not updated: provider=%q model=%q", sess.Provider, sess.Model)
	}
	reloaded, _ := store.Load("tg-1")
	if reloaded.Provider != "deepseek" {
		t.Errorf("switch not persisted: %+v", reloaded)
	}

	// Switching with no explicit model clears the model override.
	telegramCommand(store, sess, "/model kimi", "claude")
	if sess.Provider != "kimi" || sess.Model != "" {
		t.Errorf("expected kimi with no model, got %q/%q", sess.Provider, sess.Model)
	}
}

func TestTelegramCommandValidationAndOthers(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := &session.Session{ID: "tg-2", Provider: "claude"}

	if reply, _ := telegramCommand(store, sess, "/model nope", "claude"); !strings.Contains(reply, "unknown provider") {
		t.Errorf("unknown provider should be rejected: %q", reply)
	}
	if reply, _ := telegramCommand(store, sess, "/model", "claude"); !strings.Contains(reply, "usage:") {
		t.Errorf("/model with no arg should show usage: %q", reply)
	}
	if reply, _ := telegramCommand(store, sess, "/models", "claude"); !strings.Contains(reply, "kimi") {
		t.Errorf("/models should list providers: %q", reply)
	}
	if reply, _ := telegramCommand(store, sess, "/help", "claude"); !strings.Contains(reply, "/model") {
		t.Errorf("/help should document commands: %q", reply)
	}
	// Group-mention form /model@bot is normalized.
	if reply, isCmd := telegramCommand(store, sess, "/models@flaksbitch_bot", "claude"); !isCmd || !strings.Contains(reply, "Providers") {
		t.Errorf("/models@bot should work: %q", reply)
	}

	// /reset clears history but keeps the provider.
	sess.History = []provider.Message{{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: "hi"}}}}
	if reply, _ := telegramCommand(store, sess, "/reset", "claude"); !strings.Contains(reply, "reset") {
		t.Errorf("/reset reply: %q", reply)
	}
	if len(sess.History) != 0 {
		t.Errorf("/reset should clear history, got %d msgs", len(sess.History))
	}
	if sess.Provider != "claude" {
		t.Errorf("/reset should keep provider, got %q", sess.Provider)
	}
}
