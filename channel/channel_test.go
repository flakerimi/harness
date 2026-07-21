package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// record is a Deliverer that logs calls and returns a scripted error.
type record struct {
	calls []string
	err   error
}

func (r *record) Deliver(_ context.Context, dest, text string) error {
	r.calls = append(r.calls, dest+"/"+text)
	return r.err
}

func TestRegistryDeliver(t *testing.T) {
	ctx := context.Background()
	tg := &record{}
	r := NewRegistry()
	r.Register("telegram", tg)

	// Empty text is a no-op, never an error — nothing reaches the deliverer.
	if err := r.Deliver(ctx, "telegram:123", ""); err != nil {
		t.Errorf("empty text should be a no-op, got %v", err)
	}
	if len(tg.calls) != 0 {
		t.Errorf("empty text must not reach the deliverer, got %v", tg.calls)
	}

	if err := r.Deliver(ctx, "telegram:123", "hi"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(tg.calls) != 1 || tg.calls[0] != "123/hi" {
		t.Errorf("wrong dispatch: %v", tg.calls)
	}

	// Malformed / unknown targets error without reaching any deliverer.
	for name, target := range map[string]string{
		"no colon":     "telegramchat",
		"empty dest":   "telegram:",
		"unknown kind": "discord:123",
	} {
		if err := r.Deliver(ctx, target, "hello"); err == nil {
			t.Errorf("%s: expected an error for target %q", name, target)
		}
	}

	// The unknown-kind error names what is registered.
	err := r.Deliver(ctx, "discord:123", "hello")
	if err == nil || !strings.Contains(err.Error(), "telegram") {
		t.Errorf("unknown-kind error should list registered kinds, got %v", err)
	}
}

func TestRegistryMultiTarget(t *testing.T) {
	ctx := context.Background()
	a := &record{err: errors.New("a down")}
	b := &record{}
	r := NewRegistry()
	r.Register("a", a)
	r.Register("b", b)

	// Every target is attempted; the first error is the one reported.
	err := r.Deliver(ctx, "a:1|b:2", "hi")
	if err == nil || !strings.Contains(err.Error(), "a down") {
		t.Errorf("expected first error back, got %v", err)
	}
	if len(a.calls) != 1 || len(b.calls) != 1 {
		t.Errorf("both targets should be attempted: a=%v b=%v", a.calls, b.calls)
	}
}

func TestRegistryFallback(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	var got string
	r.Fallback = func(_ context.Context, kind, dest, text string) (bool, error) {
		got = kind + ":" + dest + "/" + text
		return kind == "sms", nil
	}

	if err := r.Deliver(ctx, "sms:555", "hi"); err != nil {
		t.Errorf("fallback-handled kind should succeed, got %v", err)
	}
	if got != "sms:555/hi" {
		t.Errorf("fallback saw %q", got)
	}
	if err := r.Deliver(ctx, "fax:555", "hi"); err == nil {
		t.Error("unrecognized kind should still error after fallback declines")
	}
}

func TestRegistryReplaceAndOrder(t *testing.T) {
	r := NewRegistry()
	first := &record{err: errors.New("old")}
	r.Register("x", first)
	r.Register("y", &record{})
	r.Register("x", &record{}) // replace in place, order unchanged

	if err := r.Deliver(context.Background(), "x:1", "hi"); err != nil {
		t.Errorf("re-registered deliverer should be the live one, got %v", err)
	}
	if kinds := r.Kinds(); len(kinds) != 2 || kinds[0] != "x" || kinds[1] != "y" {
		t.Errorf("registration order not preserved: %v", kinds)
	}
}
