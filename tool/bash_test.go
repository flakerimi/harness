package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBashRunsCommand(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"command": "echo hello-bash"})
	res, err := Bash{}.Run(context.Background(), in, &Env{Root: "."})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "hello-bash") {
		t.Fatalf("got %q err=%v", res.Content, res.IsError)
	}
}

func TestBashTimeout(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"command": "sleep 2"})
	res, _ := Bash{Timeout: 100 * time.Millisecond}.Run(context.Background(), in, nil)
	if !res.IsError || !strings.Contains(res.Content, "timed out") {
		t.Errorf("expected timeout, got %q", res.Content)
	}
}

func TestBashRequiresCommand(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"command": "  "})
	res, _ := Bash{}.Run(context.Background(), in, nil)
	if !res.IsError {
		t.Error("empty command should be an error result")
	}
}
