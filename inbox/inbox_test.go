package inbox

import (
	"fmt"
	"testing"
)

func TestStoreAddListReadCap(t *testing.T) {
	s := NewStore(t.TempDir())

	first, err := s.Add("morning brief")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("task result"); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 2 || got[0].Text != "morning brief" {
		t.Fatalf("list = %+v", got)
	}
	if s.Unread() != 2 {
		t.Fatalf("unread = %d", s.Unread())
	}

	// Mark one read by id, then everything.
	if err := s.MarkRead(first.ID); err != nil {
		t.Fatal(err)
	}
	if s.Unread() != 1 {
		t.Fatalf("after one read: unread = %d", s.Unread())
	}
	if err := s.MarkRead(); err != nil {
		t.Fatal(err)
	}
	if s.Unread() != 0 {
		t.Fatalf("after mark all: unread = %d", s.Unread())
	}

	// The feed is capped — old items fall off the front.
	for i := range keep + 10 {
		if _, err := s.Add(fmt.Sprintf("item %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got := s.List()
	if len(got) != keep {
		t.Fatalf("cap: len = %d, want %d", len(got), keep)
	}
	if got[len(got)-1].Text != fmt.Sprintf("item %d", keep+9) {
		t.Errorf("newest lost: %q", got[len(got)-1].Text)
	}
}
