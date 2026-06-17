package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMissingIsNoOp(t *testing.T) {
	cfgs, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if cfgs != nil {
		t.Fatalf("missing file should yield nil configs, got %+v", cfgs)
	}
}

func TestLoadConfigParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	body := `{"mcpServers":{"cal":{"command":"echo","args":["hi"],"env":{"K":"V"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("want 1 config, got %d", len(cfgs))
	}
	c := cfgs[0]
	if c.Name != "cal" || c.Command != "echo" || len(c.Args) != 1 || c.Args[0] != "hi" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if len(c.Env) != 1 || c.Env[0] != "K=V" {
		t.Fatalf("unexpected env: %+v", c.Env)
	}
}
