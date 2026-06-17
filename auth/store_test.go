package auth

import (
	"path/filepath"
	"testing"
)

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "auth.json"))

	if _, err := st.Load("claude"); err == nil {
		t.Error("loading from an empty store should error")
	}

	want := &Credentials{Access: "a-tok", Refresh: "r-tok", Expires: 1234567890}
	if err := st.Save("claude", want); err != nil {
		t.Fatal(err)
	}
	// A second provider in the same file must not clobber the first.
	if err := st.Save("google", &Credentials{Access: "g"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.Access != want.Access || got.Refresh != want.Refresh || got.Expires != want.Expires {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if g, _ := st.Load("google"); g == nil || g.Access != "g" {
		t.Errorf("second provider not preserved: %+v", g)
	}
}

func TestStoreOverwrite(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	_ = st.Save("claude", &Credentials{Access: "old"})
	_ = st.Save("claude", &Credentials{Access: "new"})
	got, err := st.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.Access != "new" {
		t.Errorf("overwrite failed: got %q", got.Access)
	}
}
