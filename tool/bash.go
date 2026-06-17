package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Bash runs a shell command in the workspace root and returns its combined
// stdout+stderr. It exists so skills can execute bundled scripts. It is a
// powerful capability — the harness ships it disabled by default (opt-in), runs
// it in Env.Root, bounds it with a timeout, and the agent's permission gate (if
// set) still runs before every call.
type Bash struct {
	Timeout time.Duration
}

func (Bash) Spec() Spec {
	return Spec{
		Name:        "bash",
		Description: "Run a shell command (sh -c) in the workspace root and return its combined stdout+stderr. Use for running a skill's bundled scripts or quick local commands.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to run.",
				},
			},
			"required": []string{"command"},
		},
	}
}

const bashMaxOutput = 16000

func (b Bash) Run(ctx context.Context, input json.RawMessage, env *Env) (Result, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{Content: "command is required", IsError: true}, nil
	}

	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", args.Command)
	if env != nil && env.Root != "" {
		cmd.Dir = env.Root
	}

	out, err := cmd.CombinedOutput()
	content := string(out)
	if len(content) > bashMaxOutput {
		content = content[:bashMaxOutput] + "\n…(truncated)"
	}

	if runCtx.Err() == context.DeadlineExceeded {
		return Result{Content: fmt.Sprintf("timed out after %s\n%s", timeout, content), IsError: true}, nil
	}
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return Result{Content: fmt.Sprintf("%s\n[exit code %d]", content, exitErr.ExitCode()), IsError: true}, nil
		}
		return Result{Content: content + "\n[error: " + err.Error() + "]", IsError: true}, nil
	}
	if content == "" {
		content = "(no output)"
	}
	return Result{Content: content}, nil
}
