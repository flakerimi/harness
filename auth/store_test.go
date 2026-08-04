package auth

import (
	"os"
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

func TestStoreSavePerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	st := NewStore(p)
	if err := st.Save("claude", &Credentials{Access: "a"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth file perms = %o, want 600", perm)
	}
}

// Connecting a connector under a fresh identity is often the first write to
// that profile's dir — Save must create it, not fail with ENOENT.
func TestStoreSaveCreatesMissingDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "profiles", "work", "auth.json")
	if err := NewStore(p).Save("google", &Credentials{Access: "a", Refresh: "r"}); err != nil {
		t.Fatalf("Save into missing dir: %v", err)
	}
	got, err := NewStore(p).Load("google")
	if err != nil || got.Access != "a" {
		t.Fatalf("reload = %+v err=%v", got, err)
	}
}
