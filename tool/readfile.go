package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadFile reads a UTF-8 text file from within Env.Root. It is the reference
// built-in: small, schema-described, and confined to the sandbox root.
type ReadFile struct{}

func (ReadFile) Spec() Spec {
	return Spec{
		Name:        "read_file",
		Description: "Read a UTF-8 text file relative to the workspace root.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, relative to the workspace root.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (ReadFile) Run(ctx context.Context, input json.RawMessage, env *Env) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if args.Path == "" {
		return Result{Content: "path is required", IsError: true}, nil
	}

	root := env.Root
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	full := filepath.Join(absRoot, filepath.Clean("/"+args.Path))

	// Confine reads to the root — reject traversal that escapes the sandbox.
	rel, err := filepath.Rel(absRoot, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Result{Content: "path escapes workspace root", IsError: true}, nil
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("%s:\n%s", args.Path, data)}, nil
}
