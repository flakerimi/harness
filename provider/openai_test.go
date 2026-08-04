package provider

import "testing"

// Strict OpenAI-compatible validators (LM Studio) reject object schemas that
// lack a properties key; buildBody must normalize without mutating the tool's
// own spec map.
func TestBuildBodyToolSchemaGainsProperties(t *testing.T) {
	o := NewOpenAI("openai", "http://unused", "k")
	req := Request{
		Model:    "gpt-4o",
		Tools:    []Tool{{Name: "ping", Description: "d", InputSchema: map[string]any{"type": "object"}}},
		CapFlags: []string{CapTools},
	}
	body := o.buildBody(req)
	tools, ok := body["tools"].([]map[string]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	fn := tools[0]["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if _, ok := params["properties"]; !ok {
		t.Fatal("object schema without properties was not normalized")
	}
	if _, ok := req.Tools[0].InputSchema["properties"]; ok {
		t.Fatal("tool's own InputSchema map was mutated")
	}
}
