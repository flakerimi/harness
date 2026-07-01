// Package schedule is the proactive pillar: it lets an identity run on a clock
// instead of only when prompted. A task pairs a profile with a prompt and a
// schedule spec ("every 30m", "daily 08:00", "weekly mon 09:00"); a runner
// (driven by `harness schedule run-due`, a system cron, or the built-in daemon)
// fires the prompt when its time comes. Tasks persist as JSON so they survive
// restarts.
package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Task is one scheduled job.
type Task struct {
	ID       string    `json:"id"`
	Profile  string    `json:"profile"`
	Provider string    `json:"provider,omitempty"` // model provider slug; empty → caller default
	Prompt   string    `json:"prompt"`
	Spec     string    `json:"spec"`
	Deliver  string    `json:"deliver,omitempty"` // where to send output, e.g. "telegram:<chatID>"; empty → stdout only
	Enabled  bool      `json:"enabled"`
	Created  time.Time `json:"created"`
	LastRun  time.Time `json:"last_run,omitzero"`
	NextRun  time.Time `json:"next_run,omitzero"`
}

// Due reports whether the task should fire at time now.
func (t *Task) Due(now time.Time) bool {
	return t.Enabled && !t.NextRun.IsZero() && !now.Before(t.NextRun)
}

// NextRun computes the next fire time strictly after the reference time, from a
// schedule spec. Supported forms:
//
//	every <dur>        e.g. "every 30m", "every 2h", "every 90m", "every 1d"
//	daily HH:MM        e.g. "daily 08:00"  (next occurrence of that local time)
//	weekly <day> HH:MM e.g. "weekly mon 09:00"
func NextRun(spec string, after time.Time) (time.Time, error) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(spec)))
	if len(fields) == 0 {
		return time.Time{}, fmt.Errorf("empty schedule spec")
	}
	switch fields[0] {
	case "every":
		if len(fields) != 2 {
			return time.Time{}, fmt.Errorf("usage: every <duration> (e.g. every 30m)")
		}
		d, err := parseEvery(fields[1])
		if err != nil {
			return time.Time{}, err
		}
		return after.Add(d), nil

	case "daily":
		if len(fields) != 2 {
			return time.Time{}, fmt.Errorf("usage: daily HH:MM")
		}
		hh, mm, err := parseClock(fields[1])
		if err != nil {
			return time.Time{}, err
		}
		next := time.Date(after.Year(), after.Month(), after.Day(), hh, mm, 0, 0, after.Location())
		if !next.After(after) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil

	case "weekly":
		if len(fields) != 3 {
			return time.Time{}, fmt.Errorf("usage: weekly <day> HH:MM (e.g. weekly mon 09:00)")
		}
		wd, ok := weekday(fields[1])
		if !ok {
			return time.Time{}, fmt.Errorf("unknown day %q", fields[1])
		}
		hh, mm, err := parseClock(fields[2])
		if err != nil {
			return time.Time{}, err
		}
		next := time.Date(after.Year(), after.Month(), after.Day(), hh, mm, 0, 0, after.Location())
		for next.Weekday() != wd || !next.After(after) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized schedule %q (try: every 30m | daily 08:00 | weekly mon 09:00)", spec)
}

// parseEvery accepts Go durations plus a "<n>d" days form.
func parseEvery(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid day duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return d, nil
}

func parseClock(s string) (hh, mm int, err error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, fmt.Errorf("invalid time %q (want HH:MM)", s)
	}
	hh, err = strconv.Atoi(h)
	if err != nil || hh < 0 || hh > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	mm, err = strconv.Atoi(m)
	if err != nil || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return hh, mm, nil
}

func weekday(s string) (time.Weekday, bool) {
	days := map[string]time.Weekday{
		"sun": time.Sunday, "sunday": time.Sunday,
		"mon": time.Monday, "monday": time.Monday,
		"tue": time.Tuesday, "tuesday": time.Tuesday,
		"wed": time.Wednesday, "wednesday": time.Wednesday,
		"thu": time.Thursday, "thursday": time.Thursday,
		"fri": time.Friday, "friday": time.Friday,
		"sat": time.Saturday, "saturday": time.Saturday,
	}
	wd, ok := days[s]
	return wd, ok
}

// Store persists the task list as a single JSON file under a directory.
type Store struct {
	dir string
}

// NewStore builds a schedule store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) file() string { return filepath.Join(s.dir, "tasks.json") }

// Load returns the stored tasks (sorted by id), or an empty slice if none.
func (s *Store) Load() ([]Task, error) {
	raw, err := os.ReadFile(s.file())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tasks []Task
	if err := json.Unmarshal(raw, &tasks); err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// Save writes the task list, creating the store dir as needed.
func (s *Store) Save(tasks []Task) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.file(), raw, 0o600)
}

// Add appends a task, assigning an id if it has none, and persists. The stored
// task (with id and computed NextRun) is returned.
func (s *Store) Add(t Task, now time.Time) (Task, error) {
	tasks, err := s.Load()
	if err != nil {
		return Task{}, err
	}
	if t.ID == "" {
		t.ID = nextID(tasks)
	} else if _, ok := find(tasks, t.ID); ok {
		return Task{}, fmt.Errorf("task %q already exists", t.ID)
	}
	if t.Created.IsZero() {
		t.Created = now
	}
	t.Enabled = true
	nr, err := NextRun(t.Spec, now)
	if err != nil {
		return Task{}, err
	}
	t.NextRun = nr
	tasks = append(tasks, t)
	if err := s.Save(tasks); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Remove deletes a task by id, reporting whether it existed.
func (s *Store) Remove(id string) (bool, error) {
	tasks, err := s.Load()
	if err != nil {
		return false, err
	}
	out := tasks[:0]
	found := false
	for _, t := range tasks {
		if t.ID == id {
			found = true
			continue
		}
		out = append(out, t)
	}
	if !found {
		return false, nil
	}
	return true, s.Save(out)
}

// Due returns the tasks that should fire at time now.
func (s *Store) Due(now time.Time) ([]Task, error) {
	tasks, err := s.Load()
	if err != nil {
		return nil, err
	}
	var due []Task
	for _, t := range tasks {
		if t.Due(now) {
			due = append(due, t)
		}
	}
	return due, nil
}

// MarkRan records that a task fired at now and recomputes its NextRun.
func (s *Store) MarkRan(id string, now time.Time) error {
	tasks, err := s.Load()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID != id {
			continue
		}
		tasks[i].LastRun = now
		if nr, nerr := NextRun(tasks[i].Spec, now); nerr == nil {
			tasks[i].NextRun = nr
		}
		return s.Save(tasks)
	}
	return fmt.Errorf("task %q not found", id)
}

func find(tasks []Task, id string) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// nextID returns the lowest "task-N" id not already taken.
func nextID(tasks []Task) string {
	used := map[string]bool{}
	for _, t := range tasks {
		used[t.ID] = true
	}
	for i := 1; ; i++ {
		id := "task-" + strconv.Itoa(i)
		if !used[id] {
			return id
		}
	}
}
