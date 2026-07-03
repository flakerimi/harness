// Package task is the background-work pillar: a persistent queue of one-shot
// jobs — "work on X, report when done". Where schedule/ fires prompts on a
// clock, task/ fires them now, off the caller's critical path: a surface (or
// the agent itself, via the background_task tool) enqueues a prompt, the
// daemon's worker executes it sequentially, and the result is delivered to a
// channel target ("telegram:<chatID>") or kept for `harness task show`. Tasks
// persist as JSON so a restart loses nothing; jobs caught mid-run are re-queued.
package task

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Status is a task's lifecycle state.
type Status string

const (
	Queued  Status = "queued"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
)

// Task is one background job.
type Task struct {
	ID       string    `json:"id"`
	Profile  string    `json:"profile,omitempty"`
	Provider string    `json:"provider,omitempty"` // model provider slug; empty → runner default
	Prompt   string    `json:"prompt"`
	Deliver  string    `json:"deliver,omitempty"` // where the result goes, e.g. "telegram:<chatID>"; empty → stored only
	Status   Status    `json:"status"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Result   string    `json:"result,omitempty"`
	Error    string    `json:"error,omitempty"`

	// Retry state: transient failures (network, provider blips) re-queue the
	// job instead of failing it — Attempts counts runs, NotBefore delays the
	// next pickup so retries back off.
	Attempts  int       `json:"attempts,omitempty"`
	NotBefore time.Time `json:"not_before,omitzero"`
}

// Store persists tasks as one JSON file each under dir.
type Store struct{ dir string }

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Dir() string { return s.dir }

// Enqueue files a new job: it assigns the id, stamps creation, and persists it
// as Queued. The passed task's Prompt is required.
func (s *Store) Enqueue(t Task) (*Task, error) {
	if strings.TrimSpace(t.Prompt) == "" {
		return nil, fmt.Errorf("task prompt is required")
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	t.ID = fmt.Sprintf("t%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
	t.Status = Queued
	t.Created = time.Now()
	t.Started, t.Finished = time.Time{}, time.Time{}
	t.Result, t.Error = "", ""
	if err := s.Save(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Save persists a task (atomic rename, like the other stores).
func (s *Store) Save(t *Task) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(t.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(t.ID))
}

// Get loads one task by id.
func (s *Store) Get(id string) (*Task, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// List returns all tasks, oldest first.
func (s *Store) List() ([]Task, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Task
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := s.Get(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // skip a corrupt file rather than failing the listing
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// NextQueued claims the oldest runnable queued task: it marks it Running and
// persists before returning, so a single worker processes each job once. Jobs
// whose NotBefore is in the future (retry backoff) are skipped. Returns nil
// with no error when nothing is runnable.
func (s *Store) NextQueued() (*Task, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range all {
		if all[i].Status != Queued || now.Before(all[i].NotBefore) {
			continue
		}
		t := all[i]
		t.Status = Running
		t.Started = time.Now()
		if err := s.Save(&t); err != nil {
			return nil, err
		}
		return &t, nil
	}
	return nil, nil
}

// Requeue puts a transiently-failed job back in the queue with backoff, so a
// network blip doesn't kill work the user is waiting on.
func (s *Store) Requeue(t *Task, delay time.Duration) error {
	t.Attempts++
	t.Status = Queued
	t.Started = time.Time{}
	t.NotBefore = time.Now().Add(delay)
	return s.Save(t)
}

// Complete finalizes a run: result text on success, the error on failure.
func (s *Store) Complete(t *Task, result string, runErr error) error {
	t.Finished = time.Now()
	if runErr != nil {
		t.Status = Failed
		t.Error = runErr.Error()
	} else {
		t.Status = Done
		t.Result = result
	}
	return s.Save(t)
}

// RecoverRunning re-queues jobs caught mid-run (a daemon restart), so nothing
// is silently lost. Returns how many were recovered.
func (s *Store) RecoverRunning() (int, error) {
	all, err := s.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range all {
		if all[i].Status != Running {
			continue
		}
		t := all[i]
		t.Status = Queued
		t.Started = time.Time{}
		if err := s.Save(&t); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}
