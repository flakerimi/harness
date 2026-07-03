package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, tl Tool, env *Env, input map[string]any) Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tl.Run(context.Background(), raw, env)
	if err != nil {
		t.Fatalf("%s: %v", tl.Spec().Name, err)
	}
	return res
}

func TestWriteReadEditListRoundtrip(t *testing.T) {
	env := &Env{Root: t.TempDir()}

	if res := run(t, WriteFile{}, env, map[string]any{"path": "notes/draft.md", "content": "hello world"}); res.IsError {
		t.Fatalf("write: %s", res.Content)
	}
	if res := run(t, ReadFile{}, env, map[string]any{"path": "notes/draft.md"}); res.IsError || !strings.Contains(res.Content, "hello world") {
		t.Fatalf("read after write: %s", res.Content)
	}
	if res := run(t, EditFile{}, env, map[string]any{"path": "notes/draft.md", "old_string": "world", "new_string": "harness"}); res.IsError {
		t.Fatalf("edit: %s", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(env.Root, "notes", "draft.md"))
	if string(data) != "hello harness" {
		t.Errorf("after edit = %q, want %q", data, "hello harness")
	}
	res := run(t, ListDir{}, env, map[string]any{"path": "notes"})
	if res.IsError || !strings.Contains(res.Content, "draft.md") {
		t.Errorf("list: %s", res.Content)
	}
}

func TestEditRequiresUniqueMatch(t *testing.T) {
	env := &Env{Root: t.TempDir()}
	run(t, WriteFile{}, env, map[string]any{"path": "f.txt", "content": "aa aa"})

	if res := run(t, EditFile{}, env, map[string]any{"path": "f.txt", "old_string": "aa", "new_string": "b"}); !res.IsError {
		t.Error("ambiguous old_string should error without replace_all")
	}
	if res := run(t, EditFile{}, env, map[string]any{"path": "f.txt", "old_string": "aa", "new_string": "b", "replace_all": true}); res.IsError {
		t.Errorf("replace_all: %s", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(env.Root, "f.txt"))
	if string(data) != "b b" {
		t.Errorf("after replace_all = %q, want %q", data, "b b")
	}
	if res := run(t, EditFile{}, env, map[string]any{"path": "f.txt", "old_string": "zz", "new_string": "y"}); !res.IsError {
		t.Error("missing old_string should error")
	}
}

func TestWriteConfinedToRoot(t *testing.T) {
	outer := t.TempDir()
	env := &Env{Root: filepath.Join(outer, "sandbox")}
	os.MkdirAll(env.Root, 0o755)

	if res := run(t, WriteFile{}, env, map[string]any{"path": "../escape.txt", "content": "x"}); !res.IsError {
		t.Error("traversal write should be rejected")
	}
	if _, err := os.Stat(filepath.Join(outer, "escape.txt")); err == nil {
		t.Error("escaped file must not exist")
	}
}

func TestWorkspacePrefixAddressesWorkspace(t *testing.T) {
	env := &Env{Root: t.TempDir(), Workspace: t.TempDir()}

	if res := run(t, WriteFile{}, env, map[string]any{"path": "workspace:home.md", "content": "home sweet home"}); res.IsError {
		t.Fatalf("workspace write: %s", res.Content)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "home.md")); err != nil {
		t.Errorf("file should land in the workspace: %v", err)
	}
	if res := run(t, ReadFile{}, env, map[string]any{"path": "workspace:home.md"}); res.IsError || !strings.Contains(res.Content, "home sweet home") {
		t.Errorf("workspace read: %s", res.Content)
	}
	// Traversal out of the workspace is confined too.
	if res := run(t, WriteFile{}, env, map[string]any{"path": "workspace:../escape.txt", "content": "x"}); !res.IsError {
		t.Error("workspace traversal should be rejected")
	}
	// Without a workspace the prefix is an error, not a silent fallback.
	if res := run(t, ReadFile{}, &Env{Root: env.Root}, map[string]any{"path": "workspace:home.md"}); !res.IsError {
		t.Error("workspace: path without a workspace should error")
	}
}

func TestWriteToolsAreMarkedAsWrites(t *testing.T) {
	for _, tl := range []Tool{WriteFile{}, EditFile{}, Bash{}} {
		if !tl.Spec().Writes {
			t.Errorf("%s must be marked Writes", tl.Spec().Name)
		}
	}
	for _, tl := range []Tool{ReadFile{}, ListDir{}} {
		if tl.Spec().Writes {
			t.Errorf("%s must not be marked Writes", tl.Spec().Name)
		}
	}
}

func TestWriteFileAppendBuildsInSections(t *testing.T) {
	env := &Env{Root: t.TempDir()}
	run(t, WriteFile{}, env, map[string]any{"path": "pub/big.html", "content": "<html><body>"})
	run(t, WriteFile{}, env, map[string]any{"path": "pub/big.html", "content": "<h1>part two</h1>", "append": true})
	res := run(t, WriteFile{}, env, map[string]any{"path": "pub/big.html", "content": "</body></html>", "append": true})
	if res.IsError || !strings.Contains(res.Content, "appended") {
		t.Fatalf("append: %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(env.Root, "pub", "big.html"))
	if string(data) != "<html><body><h1>part two</h1></body></html>" {
		t.Errorf("assembled = %q", data)
	}
	// append to a fresh path creates it
	if res := run(t, WriteFile{}, env, map[string]any{"path": "new.txt", "content": "x", "append": true}); res.IsError {
		t.Errorf("append-create: %+v", res)
	}
}
