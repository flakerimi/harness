package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEnqueueClaimComplete(t *testing.T) {
	s := NewStore(t.TempDir())

	if _, err := s.Enqueue(Task{}); err == nil {
		t.Error("empty prompt must be rejected")
	}

	a, err := s.Enqueue(Task{Profile: "personal", Prompt: "first", Deliver: "telegram:1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Enqueue(Task{Profile: "personal", Prompt: "second"})
	if err != nil {
		t.Fatal(err)
	}

	// Claim order is FIFO, and claiming marks Running durably.
	got, err := s.NextQueued()
	if err != nil || got == nil || got.ID != a.ID {
		t.Fatalf("NextQueued = %v, %v; want %s", got, err, a.ID)
	}
	if onDisk, _ := s.Get(a.ID); onDisk.Status != Running || onDisk.Started.IsZero() {
		t.Errorf("claimed task should be persisted Running, got %+v", onDisk)
	}

	// Success path.
	if err := s.Complete(got, "the answer", nil); err != nil {
		t.Fatal(err)
	}
	if onDisk, _ := s.Get(a.ID); onDisk.Status != Done || onDisk.Result != "the answer" || onDisk.Finished.IsZero() {
		t.Errorf("completed task = %+v", onDisk)
	}

	// Failure path.
	got, _ = s.NextQueued()
	if got == nil || got.ID != b.ID {
		t.Fatalf("second claim = %v, want %s", got, b.ID)
	}
	if err := s.Complete(got, "", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if onDisk, _ := s.Get(b.ID); onDisk.Status != Failed || onDisk.Error != "boom" {
		t.Errorf("failed task = %+v", onDisk)
	}

	// Queue drained.
	if got, err := s.NextQueued(); err != nil || got != nil {
		t.Errorf("drained queue: got %v, %v", got, err)
	}
}

func TestRecoverRunningRequeues(t *testing.T) {
	s := NewStore(t.TempDir())
	a, _ := s.Enqueue(Task{Prompt: "interrupted work"})
	if _, err := s.NextQueued(); err != nil {
		t.Fatal(err)
	}

	n, err := s.RecoverRunning()
	if err != nil || n != 1 {
		t.Fatalf("RecoverRunning = %d, %v; want 1", n, err)
	}
	if onDisk, _ := s.Get(a.ID); onDisk.Status != Queued || !onDisk.Started.IsZero() {
		t.Errorf("recovered task = %+v, want Queued", onDisk)
	}
}

func TestEnqueueToolQueuesForIdentity(t *testing.T) {
	s := NewStore(t.TempDir())
	tl := NewEnqueueTool(s, "personal", "mock", "telegram:42")

	if tl.Spec().Name != "background_task" || tl.Spec().Writes {
		t.Errorf("spec = %+v", tl.Spec())
	}
	in, _ := json.Marshal(map[string]string{"prompt": "research X and summarize"})
	res, err := tl.Run(context.Background(), in, nil)
	if err != nil || res.IsError {
		t.Fatalf("run: %v %s", err, res.Content)
	}
	if !strings.Contains(res.Content, "queued background task") {
		t.Errorf("result = %q", res.Content)
	}
	all, _ := s.List()
	if len(all) != 1 || all[0].Profile != "personal" || all[0].Deliver != "telegram:42" || all[0].Provider != "mock" {
		t.Errorf("queued = %+v", all)
	}

	empty, _ := json.Marshal(map[string]string{})
	if res, _ := tl.Run(context.Background(), empty, nil); !res.IsError {
		t.Error("empty prompt should error")
	}
}

func TestStatusToolShowsOwnQueueOnly(t *testing.T) {
	s := NewStore(t.TempDir())
	mine, _ := s.Enqueue(Task{Profile: "personal", Prompt: "my research job"})
	s.Enqueue(Task{Profile: "business", Prompt: "someone else's job"})

	// Finish one with a result, fail nothing yet.
	claimed, _ := s.NextQueued()
	s.Complete(claimed, "the findings", nil)
	_ = mine

	tl := NewStatusTool(s, "personal")
	if tl.Spec().Writes {
		t.Error("task_status must be read-only")
	}
	res, err := tl.Run(context.Background(), nil, nil)
	if err != nil || res.IsError {
		t.Fatalf("run: %v %s", err, res.Content)
	}
	if !strings.Contains(res.Content, "my research job") || !strings.Contains(res.Content, "the findings") {
		t.Errorf("should show own task + result: %q", res.Content)
	}
	if strings.Contains(res.Content, "someone else's job") {
		t.Errorf("must not leak other identities' tasks: %q", res.Content)
	}

	// An identity with no jobs gets a plain statement, not an error.
	res, _ = NewStatusTool(s, "developer").Run(context.Background(), nil, nil)
	if res.IsError || !strings.Contains(res.Content, "no background tasks") {
		t.Errorf("empty queue = %+v", res)
	}
}
