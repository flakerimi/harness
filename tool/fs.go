package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// resolvePath maps a tool-supplied path into the mediated filesystem. Paths
// resolve inside Env.Root; a "workspace:" prefix resolves inside Env.Workspace
// (the identity's persistent home) for when the working root is elsewhere —
// e.g. a CLI run in a project dir saving a draft home. Traversal that escapes
// the chosen base is rejected, so tools never widen their own blast radius.
func resolvePath(env *Env, p string) (string, error) {
	base := env.Root
	if base == "" {
		base = "."
	}
	if rest, ok := strings.CutPrefix(p, "workspace:"); ok {
		if env.Workspace == "" {
			return "", errors.New("no workspace is configured (workspace: paths need an identity profile)")
		}
		base, p = env.Workspace, rest
		if p == "" {
			p = "."
		}
	}
	// Reject escape attempts outright (Clean-then-Join below would silently
	// confine them, which hides the mistake from the model).
	if c := filepath.Clean(p); c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the sandbox root")
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	full := filepath.Join(absBase, filepath.Clean("/"+p))
	rel, err := filepath.Rel(absBase, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the sandbox root")
	}
	return full, nil
}

// WriteFile creates or overwrites a file under the sandbox root (parent
// directories are created). It is a mutating action: Spec.Writes is set, so a
// permission gate (when wired) confirms it before it runs.
type WriteFile struct{}

func (WriteFile) Spec() Spec {
	return Spec{
		Name:        "write_file",
		Description: "Create or overwrite a UTF-8 text file (parent dirs are created); set append to add to the end instead — write large files in sections across several calls. Paths are relative to the working root; prefix with \"workspace:\" to write into your persistent workspace instead.",
		Writes:      true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path relative to the working root (or workspace: prefixed).",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The content to write (or append).",
				},
				"append": map[string]any{
					"type":        "boolean",
					"description": "Append to the file instead of overwriting — for writing big files in pieces.",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (WriteFile) Run(ctx context.Context, input json.RawMessage, env *Env) (Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if args.Path == "" {
		return Result{Content: "path is required", IsError: true}, nil
	}
	full, err := resolvePath(env, args.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if args.Append {
		f, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		defer f.Close()
		if _, err := f.WriteString(args.Content); err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		fi, _ := f.Stat()
		return Result{Content: fmt.Sprintf("appended %d bytes to %s (now %d bytes)", len(args.Content), args.Path, fi.Size())}, nil
	}
	if err := os.WriteFile(full, []byte(args.Content), 0o644); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("wrote %s (%d bytes)", args.Path, len(args.Content))}, nil
}

// EditFile performs an exact string replacement in an existing file — the
// surgical alternative to rewriting a whole file with write_file. The target
// string must match exactly once unless replace_all is set, which keeps the
// model honest about what it is changing.
type EditFile struct{}

func (EditFile) Spec() Spec {
	return Spec{
		Name:        "edit_file",
		Description: "Replace an exact string in an existing text file. old_string must occur exactly once (set replace_all to change every occurrence). Paths are relative to the working root; prefix with \"workspace:\" for your persistent workspace.",
		Writes:      true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path relative to the working root (or workspace: prefixed).",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "The exact text to replace.",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The replacement text.",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace every occurrence (default: old_string must be unique).",
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func (EditFile) Run(ctx context.Context, input json.RawMessage, env *Env) (Result, error) {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if args.Path == "" || args.OldString == "" {
		return Result{Content: "path and old_string are required", IsError: true}, nil
	}
	if args.OldString == args.NewString {
		return Result{Content: "old_string and new_string are identical", IsError: true}, nil
	}
	full, err := resolvePath(env, args.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	text := string(data)
	n := strings.Count(text, args.OldString)
	switch {
	case n == 0:
		return Result{Content: "old_string not found in " + args.Path, IsError: true}, nil
	case n > 1 && !args.ReplaceAll:
		return Result{Content: fmt.Sprintf("old_string occurs %d times in %s — make it unique or set replace_all", n, args.Path), IsError: true}, nil
	}
	text = strings.ReplaceAll(text, args.OldString, args.NewString)
	if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("edited %s (%d replacement(s))", args.Path, n)}, nil
}

// listDirCap bounds a listing so a huge directory can't flood the context.
const listDirCap = 300

// ListDir lists a directory under the sandbox root — how the agent orients
// itself in its workspace before reading or writing.
type ListDir struct{}

func (ListDir) Spec() Spec {
	return Spec{
		Name:        "list_dir",
		Description: "List a directory's entries (dirs end with /). Paths are relative to the working root; prefix with \"workspace:\" to list your persistent workspace. Empty path lists the root.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path relative to the working root (or workspace: prefixed); empty for the root itself.",
				},
			},
		},
	}
}

func (ListDir) Run(ctx context.Context, input json.RawMessage, env *Env) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
		}
	}
	p := args.Path
	if p == "" {
		p = "."
	}
	full, err := resolvePath(env, p)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d entries\n", p, len(entries))
	for i, e := range entries {
		if i == listDirCap {
			fmt.Fprintf(&b, "… %d more (truncated)\n", len(entries)-listDirCap)
			break
		}
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
			continue
		}
		size := int64(0)
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		fmt.Fprintf(&b, "%s (%d bytes)\n", e.Name(), size)
	}
	return Result{Content: strings.TrimSuffix(b.String(), "\n")}, nil
}
