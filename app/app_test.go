package app

import (
	"context"
	"testing"
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
