package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

func TestWithVisionFollowsResolvedModel(t *testing.T) {
	base := []string{provider.CapTools, provider.CapCaching}
	withFlag := append(slices.Clone(base), provider.CapVision)

	cases := []struct {
		name  string
		caps  []string
		slug  string
		model string
		want  bool // CapVision present in result
	}{
		{"vision model gains the flag", base, "anthropic", "claude-opus-4-8", true},
		{"blind model never gets it", base, "deepseek", "deepseek-v4-pro", false},
		{"stale flag is stripped for a blind model", withFlag, "deepseek", "deepseek-v4-pro", false},
		{"flag kept when already present and model sees", withFlag, "claude", "claude-opus-4-8", true},
		{"model-dependent provider, vision variant", base, "ollama", "llava:13b", true},
		{"model-dependent provider, text variant", base, "ollama", "llama3.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withVision(tc.caps, tc.slug, tc.model)
			if has := slices.Contains(got, provider.CapVision); has != tc.want {
				t.Errorf("withVision(%v, %s, %s) vision=%v, want %v", tc.caps, tc.slug, tc.model, has, tc.want)
			}
			// The other flags must survive untouched.
			for _, c := range []string{provider.CapTools, provider.CapCaching} {
				if !slices.Contains(got, c) {
					t.Errorf("lost %s", c)
				}
			}
		})
	}

	// The input slice must never be mutated — it's shared agent state.
	if slices.Contains(base, provider.CapVision) {
		t.Error("withVision mutated its input")
	}
}

// TestContinueWithCarriesImagesThroughTheLoop drives the full multimodal turn
// offline: an image + caption user message reaches the provider intact, and
// the reply lands in the history. This is the path a chat channel (Telegram
// photos) rides.
func TestContinueWithCarriesImagesThroughTheLoop(t *testing.T) {
	ag := New(provider.NewMock(), tool.NewRegistry(), Options{Model: "mock-1"})
	content := []provider.Block{
		{Type: provider.BlockImage, Image: &provider.ImageBlock{MediaType: "image/jpeg", Data: []byte("JPEG")}},
		{Type: provider.BlockText, Text: "what is this"},
	}
	var c Collector
	history, err := ag.ContinueWith(context.Background(), nil, content, &c)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Text(); !strings.Contains(got, "saw 1 image(s)") || !strings.Contains(got, "what is this") {
		t.Errorf("reply = %q — image or caption did not reach the provider", got)
	}
	if len(history) != 2 || len(history[0].Content) != 2 {
		t.Fatalf("history should hold the multimodal user turn + reply, got %d messages", len(history))
	}
}
