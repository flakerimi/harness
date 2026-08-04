package server

import "testing"

func TestJournalAppendSince(t *testing.T) {
	j := newJournal(3)
	for _, e := range []string{"a", "b", "c"} {
		j.Append(e, []byte(`{}`))
	}
	got, evicted := j.Since(1)
	if evicted || len(got) != 2 || got[0].Event != "b" || got[1].Seq != 3 {
		t.Fatalf("Since(1) = %v evicted=%v", got, evicted)
	}
}

func TestJournalEviction(t *testing.T) {
	j := newJournal(2)
	for i := 0; i < 5; i++ {
		j.Append("e", []byte(`{}`))
	}
	if _, evicted := j.Since(1); !evicted {
		t.Fatal("Since before oldest retained frame must report evicted")
	}
	got, evicted := j.Since(3)
	if evicted || len(got) != 2 {
		t.Fatalf("Since(3) = %v evicted=%v", got, evicted)
	}
}

func TestJournalResetKeepsSeqMonotonic(t *testing.T) {
	j := newJournal(8)
	j.Append("a", nil)
	j.Reset()
	f := j.Append("b", nil)
	if f.Seq != 2 {
		t.Fatalf("seq after reset = %d, want 2", f.Seq)
	}
}
