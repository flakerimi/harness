package provider

import "strings"

// visionModels lists, per provider slug, the model-name substrings that accept
// image input. An empty substring matches every model of that provider.
//
// Slugs absent from this map answer false. That is deliberate: the capability
// system's contract is "unknown ⇒ conservative default", and here the cost of
// guessing wrong is asymmetric — claiming vision a model lacks turns every
// photo into an API error, while missing vision a model has merely degrades to
// a text placeholder.
var visionModels = map[string][]string{
	"mock":      {""}, // always: keeps the image path exercisable offline
	"anthropic": {""}, // every current Claude model reads images
	"claude":    {""},
	"gemini":    {"gemini"},
	"openai":    {"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-4-vision", "gpt-5", "chatgpt-4o", "o1", "o3", "o4"},
	// kimi-k2.5/k2.6 verified empirically 2026-07-11 (red-square test, api.moonshot.ai);
	// k2.7-code untested, so deliberately unlisted.
	"kimi":     {"vision", "kimi-latest", "kimi-k2.5", "kimi-k2.6"},
	"moonshot": {"vision", "kimi-latest", "kimi-k2.5", "kimi-k2.6"},
	// fireworks' kimi-k2p6 verified same day; k2p5 500s on image input there — unlisted.
	"fireworks": {"vision", "-vl", "kimi-k2p6"},
	"mimo":      {"omni", "-vl"},
	"mistral":   {"pixtral"},
	// OpenRouter ids are vendor-prefixed ("anthropic/claude-…"), so the vendor
	// substrings identify the vision families it fronts.
	"openrouter": {"claude", "gpt-4o", "gpt-4.1", "gpt-5", "gemini", "pixtral", "vision", "-vl", "o1", "o3", "o4"},
	"ollama":     {"llava", "vision", "-vl"},
	"lmstudio":   {"llava", "vision", "-vl"},
}

// VisionCapable reports whether (slug, model) accepts image blocks. Callers use
// it to decide whether to add CapVision to a Request; the adapters then trust
// the flag rather than re-deriving it, keeping vendor knowledge at this layer.
func VisionCapable(slug, model string) bool {
	subs, ok := visionModels[strings.ToLower(slug)]
	if !ok {
		return false
	}
	m := strings.ToLower(model)
	for _, s := range subs {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// HasImages reports whether any message carries an image block. Adapters and
// callers use it to avoid paying for the multimodal request shape when the
// conversation is plain text.
func HasImages(msgs []Message) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == BlockImage && b.Image != nil {
				return true
			}
		}
	}
	return false
}
