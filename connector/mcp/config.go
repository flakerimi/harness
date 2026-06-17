package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// LoadConfig reads an MCP server config file and returns one Config per server.
// The format matches the common "mcpServers" map (Claude Desktop / many MCP
// clients):
//
//	{
//	  "mcpServers": {
//	    "google-calendar": {
//	      "command": "npx",
//	      "args": ["-y", "@some/calendar-mcp"],
//	      "env": {"GOOGLE_OAUTH_TOKEN": "…"}
//	    }
//	  }
//	}
//
// A missing file is not an error — it just means no MCP connectors are wired.
func LoadConfig(path string) ([]Config, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var file struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Stable order so introspection output is deterministic.
	names := make([]string, 0, len(file.MCPServers))
	for name := range file.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Config, 0, len(names))
	for _, name := range names {
		s := file.MCPServers[name]
		env := make([]string, 0, len(s.Env))
		for k, v := range s.Env {
			env = append(env, k+"="+v)
		}
		sort.Strings(env)
		out = append(out, Config{Name: name, Command: s.Command, Args: s.Args, Env: env})
	}
	return out, nil
}
