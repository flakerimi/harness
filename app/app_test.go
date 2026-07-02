package app

import (
	"context"
	"os"
	"testing"

	"github.com/flakerimi/harness/profile"
)

func TestBuildMockNoProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())            // isolate config/connectors
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // (linux)
	ag, err := Build(context.Background(), Spec{Provider: "mock", System: "hi", MaxTokens: 256, Root: "."})
	if err != nil {
		t.Fatalf("Build(mock): %v", err)
	}
	if ag == nil {
		t.Fatal("Build returned a nil agent")
	}
}

func TestBuildProfileRootsEnvInWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ag, err := Build(context.Background(), Spec{Provider: "mock", Profile: "personal", MaxTokens: 256})
	if err != nil {
		t.Fatalf("Build(mock, personal): %v", err)
	}
	ws := profile.WorkspaceDir("personal")
	if got := ag.Env().Root; got != ws {
		t.Errorf("Env.Root = %q, want the profile workspace %q", got, ws)
	}
	if got := ag.Env().Workspace; got != ws {
		t.Errorf("Env.Workspace = %q, want %q", got, ws)
	}
	if fi, err := os.Stat(ws); err != nil || !fi.IsDir() {
		t.Errorf("workspace dir should exist after Build: %v", err)
	}

	// An explicit root wins; the workspace stays available alongside it.
	ag, err = Build(context.Background(), Spec{Provider: "mock", Profile: "personal", MaxTokens: 256, Root: "."})
	if err != nil {
		t.Fatalf("Build(mock, personal, root=.): %v", err)
	}
	if got := ag.Env().Root; got != "." {
		t.Errorf("explicit Root = %q, want %q", got, ".")
	}
	if got := ag.Env().Workspace; got != ws {
		t.Errorf("Env.Workspace with explicit root = %q, want %q", got, ws)
	}
}

func TestBuildUnknownProfileErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Build(context.Background(), Spec{Provider: "mock", Profile: "does-not-exist", MaxTokens: 256, Root: "."})
	if err == nil {
		t.Error("building with an unknown profile should error")
	}
}

func TestBuildUnknownProviderErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Build(context.Background(), Spec{Provider: "nope", MaxTokens: 256, Root: "."})
	if err == nil {
		t.Error("building with an unknown provider should error")
	}
}
