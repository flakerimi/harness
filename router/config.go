package router

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadTable returns the default policy with any overrides from a JSON config
// merged on top. A missing file is not an error. Format:
//
//	{
//	  "providers": {
//	    "anthropic": { "reasoning": "claude-opus-4-8", "fast": "claude-haiku-4-5" },
//	    "deepseek":  { "reasoning": "deepseek-reasoner", "fast": "deepseek-chat" }
//	  }
//	}
func LoadTable(path string) (*Table, error) {
	t := DefaultTable()

	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}

	var file struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for provider, tiers := range file.Providers {
		for tierStr, model := range tiers {
			if model != "" {
				t.Set(provider, ParseTier(tierStr, TierBalanced), model)
			}
		}
	}
	return t, nil
}
