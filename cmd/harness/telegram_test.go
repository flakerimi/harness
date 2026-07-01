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
	if _, isCmd := telegramCommand(store, sess, "hello there", "claude", "personal"); isCmd {
		t.Fatal("plain text should not be treated as a command")
	}

	// /model with a provider + explicit model switches and persists.
	reply, isCmd := telegramCommand(store, sess, "/model deepseek deepseek-v4-pro", "claude", "personal")
	if !isCmd {
		t.Fatal("/model should be a command")
	}
	if !strings.Contains(reply, "deepseek") {
		t.Errorf("reply should confirm switch: %q", reply)
	}
	if sess.Provider != "deepseek" || sess.Model != "deepseek-v4-pro" {
		t.Errorf("session not updated: provider=%q model=%q", sess.Provider, sess.Model)
	}
	reloaded, _ := store.Load("tg-1")
	if reloaded.Provider != "deepseek" {
		t.Errorf("switch not persisted: %+v", reloaded)
	}

	// Switching with no explicit model clears the model override.
	telegramCommand(store, sess, "/model kimi", "claude", "personal")
	if sess.Provider != "kimi" || sess.Model != "" {
		t.Errorf("expected kimi with no model, got %q/%q", sess.Provider, sess.Model)
	}
}

func TestIdentityCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the chat-identity store
	ids := newChatIdentities()

	// A normal message isn't an identity command.
	if _, handled := identityCommand(ids, 1, "hello", "personal"); handled {
		t.Fatal("plain text should not be an identity command")
	}
	// /profiles lists identities.
	if reply, handled := identityCommand(ids, 1, "/profiles", "personal"); !handled || !strings.Contains(reply, "personal") {
		t.Errorf("/profiles reply: %q", reply)
	}
	// Switching to an unknown identity is rejected.
	if reply, _ := identityCommand(ids, 1, "/profile nope", "personal"); !strings.Contains(reply, "unknown identity") {
		t.Errorf("unknown identity should be rejected: %q", reply)
	}
	// Switching to a real identity persists per chat.
	if reply, _ := identityCommand(ids, 1, "/profile work", "personal"); !strings.Contains(reply, "work") {
		t.Errorf("/profile work reply: %q", reply)
	}
	if got := ids.get(1, "personal"); got != "work" {
		t.Errorf("identity not persisted for chat: got %q", got)
	}
	// A different chat is unaffected.
	if got := ids.get(2, "personal"); got != "personal" {
		t.Errorf("other chat should keep default, got %q", got)
	}
}

func TestTelegramCommandValidationAndOthers(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := &session.Session{ID: "tg-2", Provider: "claude"}

	if reply, _ := telegramCommand(store, sess, "/model nope", "claude", "personal"); !strings.Contains(reply, "unknown provider") {
		t.Errorf("unknown provider should be rejected: %q", reply)
	}
	if reply, _ := telegramCommand(store, sess, "/model", "claude", "personal"); !strings.Contains(reply, "usage:") {
		t.Errorf("/model with no arg should show usage: %q", reply)
	}
	if reply, _ := telegramCommand(store, sess, "/models", "claude", "personal"); !strings.Contains(reply, "kimi") {
		t.Errorf("/models should list providers: %q", reply)
	}
	if reply, _ := telegramCommand(store, sess, "/help", "claude", "personal"); !strings.Contains(reply, "/model") {
		t.Errorf("/help should document commands: %q", reply)
	}
	// Group-mention form /model@bot is normalized.
	if reply, isCmd := telegramCommand(store, sess, "/models@flaksbitch_bot", "claude", "personal"); !isCmd || !strings.Contains(reply, "Providers") {
		t.Errorf("/models@bot should work: %q", reply)
	}

	// /reset clears history but keeps the provider.
	sess.History = []provider.Message{{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: "hi"}}}}
	if reply, _ := telegramCommand(store, sess, "/reset", "claude", "personal"); !strings.Contains(reply, "reset") {
		t.Errorf("/reset reply: %q", reply)
	}
	if len(sess.History) != 0 {
		t.Errorf("/reset should clear history, got %d msgs", len(sess.History))
	}
	if sess.Provider != "claude" {
		t.Errorf("/reset should keep provider, got %q", sess.Provider)
	}
}
