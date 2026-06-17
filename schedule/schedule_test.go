package schedule

import (
	"testing"
	"time"
)

func TestNextRunEvery(t *testing.T) {
	base := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"every 30m": base.Add(30 * time.Minute),
		"every 2h":  base.Add(2 * time.Hour),
		"every 90m": base.Add(90 * time.Minute),
		"every 1d":  base.AddDate(0, 0, 1),
	}
	for spec, want := range cases {
		got, err := NextRun(spec, base)
		if err != nil {
			t.Errorf("%s: %v", spec, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("%s: next = %v, want %v", spec, got, want)
		}
	}
}

func TestNextRunDaily(t *testing.T) {
	// 10:00 now, daily 08:00 → tomorrow 08:00.
	base := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	got, err := NextRun("daily 08:00", base)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 18, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("daily 08:00 = %v, want %v", got, want)
	}

	// 06:00 now, daily 08:00 → today 08:00.
	early := time.Date(2026, 6, 17, 6, 0, 0, 0, time.UTC)
	got2, _ := NextRun("daily 08:00", early)
	want2 := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Errorf("daily 08:00 (early) = %v, want %v", got2, want2)
	}
}

func TestNextRunWeekly(t *testing.T) {
	// 2026-06-17 is a Wednesday. Next Monday 09:00.
	base := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	got, err := NextRun("weekly mon 09:00", base)
	if err != nil {
		t.Fatal(err)
	}
	if got.Weekday() != time.Monday || got.Hour() != 9 {
		t.Errorf("weekly mon 09:00 = %v (%s)", got, got.Weekday())
	}
	if !got.After(base) {
		t.Errorf("next weekly run must be in the future: %v", got)
	}
}

func TestNextRunErrors(t *testing.T) {
	base := time.Now()
	for _, spec := range []string{"", "every", "every 0m", "daily 25:00", "weekly xx 09:00", "nonsense"} {
		if _, err := NextRun(spec, base); err == nil {
			t.Errorf("spec %q should error", spec)
		}
	}
}

func TestStoreAddListRemove(t *testing.T) {
	st := NewStore(t.TempDir())
	now := time.Date(2026, 6, 17, 6, 0, 0, 0, time.UTC)

	a, err := st.Add(Task{Profile: "work", Prompt: "brief me", Spec: "daily 08:00"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "task-1" || !a.Enabled || a.NextRun.IsZero() {
		t.Fatalf("unexpected added task: %+v", a)
	}
	b, _ := st.Add(Task{Profile: "personal", Prompt: "standup", Spec: "every 1h"}, now)
	if b.ID != "task-2" {
		t.Errorf("second id = %q, want task-2", b.ID)
	}

	tasks, _ := st.Load()
	if len(tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(tasks))
	}

	ok, _ := st.Remove("task-1")
	if !ok {
		t.Error("remove task-1 should report found")
	}
	gone, _ := st.Remove("task-1")
	if gone {
		t.Error("removing a missing task should report not-found")
	}
	tasks, _ = st.Load()
	if len(tasks) != 1 || tasks[0].ID != "task-2" {
		t.Errorf("after remove: %+v", tasks)
	}
}

func TestDueAndMarkRan(t *testing.T) {
	st := NewStore(t.TempDir())
	create := time.Date(2026, 6, 17, 6, 0, 0, 0, time.UTC)
	_, _ = st.Add(Task{Profile: "work", Prompt: "x", Spec: "daily 08:00"}, create)

	// Before 08:00 → not due.
	due, _ := st.Due(time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC))
	if len(due) != 0 {
		t.Errorf("should not be due yet: %+v", due)
	}
	// At 08:30 → due.
	fire := time.Date(2026, 6, 17, 8, 30, 0, 0, time.UTC)
	due, _ = st.Due(fire)
	if len(due) != 1 {
		t.Fatalf("should be due at 08:30, got %d", len(due))
	}

	// After running, NextRun advances to the next day and it's no longer due.
	if err := st.MarkRan("task-1", fire); err != nil {
		t.Fatal(err)
	}
	due, _ = st.Due(fire)
	if len(due) != 0 {
		t.Errorf("should not be due right after running: %+v", due)
	}
	tasks, _ := st.Load()
	if tasks[0].LastRun.IsZero() || !tasks[0].NextRun.After(fire) {
		t.Errorf("MarkRan did not advance schedule: %+v", tasks[0])
	}
}
