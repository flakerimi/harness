package provider

import (
	"strings"
	"testing"
)

func img(mt string, data string) Block {
	return Block{Type: BlockImage, Image: &ImageBlock{MediaType: mt, Data: []byte(data)}}
}

func text(s string) Block { return Block{Type: BlockText, Text: s} }

func TestVisionCapableIsConservativeForUnknownProviders(t *testing.T) {
	if VisionCapable("deepseek", "deepseek-chat") {
		t.Error("deepseek-chat has no vision; claiming it turns every photo into a 400")
	}
	if VisionCapable("some-new-vendor", "whatever") {
		t.Error("unknown provider must default to no vision")
	}
}

func TestVisionCapableKnownPairs(t *testing.T) {
	yes := []struct{ slug, model string }{
		{"anthropic", "claude-opus-4-8"},
		{"claude", "anything-at-all"},
		{"mock", ""},
		{"openai", "gpt-4o-mini"},
		{"openai", "gpt-5"},
		{"gemini", "gemini-2.5-pro"},
		{"ollama", "llava:13b"},
		{"moonshot", "moonshot-v1-8k-vision-preview"},
		{"kimi", "kimi-latest"},
		{"mistral", "pixtral-large-latest"},
		{"openrouter", "anthropic/claude-sonnet-4.5"},
		{"openrouter", "google/gemini-2.5-flash"},
		{"ANTHROPIC", "claude-x"}, // slug is case-insensitive
	}
	for _, c := range yes {
		if !VisionCapable(c.slug, c.model) {
			t.Errorf("VisionCapable(%q, %q) = false, want true", c.slug, c.model)
		}
	}
	no := []struct{ slug, model string }{
		{"openai", "gpt-3.5-turbo"},
		{"ollama", "llama3.1"},
		{"moonshot", "moonshot-v1-8k"},
		{"mistral", "mistral-large-latest"},
		{"openrouter", "deepseek/deepseek-chat"},
	}
	for _, c := range no {
		if VisionCapable(c.slug, c.model) {
			t.Errorf("VisionCapable(%q, %q) = true, want false", c.slug, c.model)
		}
	}
}

func TestHasImages(t *testing.T) {
	textOnly := []Message{{Role: "user", Content: []Block{text("hi")}}}
	if HasImages(textOnly) {
		t.Error("text-only conversation reported images")
	}
	withImg := []Message{{Role: "user", Content: []Block{text("what is this?"), img("image/png", "PNG")}}}
	if !HasImages(withImg) {
		t.Error("image block not detected")
	}
	// A nil Image with an image Type is malformed, not an image.
	malformed := []Message{{Role: "user", Content: []Block{{Type: BlockImage}}}}
	if HasImages(malformed) {
		t.Error("nil ImageBlock must not count as an image")
	}
}

func TestImagePlaceholderNamesWhatWasWithheld(t *testing.T) {
	got := ImagePlaceholder(&ImageBlock{MediaType: "image/jpeg", Data: []byte("abc")})
	for _, want := range []string{"image/jpeg", "3 bytes", "no vision"} {
		if !strings.Contains(got, want) {
			t.Errorf("placeholder %q missing %q", got, want)
		}
	}
	if ImagePlaceholder(nil) == "" {
		t.Error("nil image should still produce a placeholder")
	}
}

func TestAnthropicMessagesEncodesImageAsBase64Source(t *testing.T) {
	msgs := []Message{{Role: "user", Content: []Block{text("look"), img("image/png", "hi")}}}
	out := anthropicMessages(msgs, true)
	content := out[0]["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(content))
	}
	if content[1]["type"] != "image" {
		t.Fatalf("second block should be an image, got %v", content[1]["type"])
	}
	src := content[1]["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/png" {
		t.Errorf("bad source envelope: %v", src)
	}
	if src["data"] != "aGk=" { // base64("hi")
		t.Errorf("data = %v, want base64 of the raw bytes", src["data"])
	}
}

func TestAnthropicMessagesDegradesImageWithoutVision(t *testing.T) {
	msgs := []Message{{Role: "user", Content: []Block{img("image/png", "hi")}}}
	content := anthropicMessages(msgs, false)[0]["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("image should degrade to a text block, got %v", content)
	}
	if !strings.Contains(content[0]["text"].(string), "no vision") {
		t.Errorf("placeholder should explain itself, got %q", content[0]["text"])
	}
}

// The plain-string form is a compatibility guarantee: deepseek, ollama and
// lmstudio reject the content-parts array, so text-only messages must keep the
// shape the harness has always sent them.
func TestOpenAIUserContentStaysAStringWhenThereAreNoImages(t *testing.T) {
	got := openAIUserContent([]Block{text("one"), text("two")}, true)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("text-only content must serialize as a string, got %T", got)
	}
	if s != "one\ntwo" {
		t.Errorf("got %q", s)
	}
}

func TestOpenAIUserContentUsesPartsArrayForImages(t *testing.T) {
	got := openAIUserContent([]Block{text("look"), img("image/png", "hi")}, true)
	parts, ok := got.([]map[string]any)
	if !ok {
		t.Fatalf("image content must serialize as parts, got %T", got)
	}
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[1]["type"] != "image_url" {
		t.Fatalf("unexpected parts: %v", parts)
	}
	url := parts[1]["image_url"].(map[string]any)["url"].(string)
	if url != "data:image/png;base64,aGk=" {
		t.Errorf("url = %q, want an inline data URL", url)
	}
}

func TestOpenAIUserContentDegradesToStringWithoutVision(t *testing.T) {
	got := openAIUserContent([]Block{text("look"), img("image/png", "hi")}, false)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("a blind model must receive a string, not image parts: got %T", got)
	}
	if !strings.Contains(s, "look") || !strings.Contains(s, "no vision") {
		t.Errorf("want caption plus placeholder, got %q", s)
	}
}

func TestMockReportsImagesItReceived(t *testing.T) {
	var out strings.Builder
	req := Request{Messages: []Message{{Role: "user", Content: []Block{text("what is this?"), img("image/png", "hi")}}}}
	err := (&Mock{}).Stream(t.Context(), req, func(e Event) {
		if e.Type == EventTextDelta {
			out.WriteString(e.TextDelta)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "saw 1 image(s)") {
		t.Errorf("mock should confirm the image reached the provider, got %q", out.String())
	}
}
