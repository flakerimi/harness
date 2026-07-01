package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotenvLine(t *testing.T) {
	cases := []struct {
		line          string
		wantKey, want string
		ok            bool
	}{
		{`FOO=bar`, "FOO", "bar", true},
		{`export TOKEN=123:abc`, "TOKEN", "123:abc", true},
		{`  QUOTED = "hello world" `, "QUOTED", "hello world", true},
		{`SINGLE='x y'`, "SINGLE", "x y", true},
		{`# a comment`, "", "", false},
		{``, "", "", false},
		{`no_equals_here`, "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parseDotenvLine(c.line)
		if ok != c.ok || k != c.wantKey || v != c.want {
			t.Errorf("parse(%q) = (%q,%q,%v), want (%q,%q,%v)", c.line, k, v, ok, c.wantKey, c.want, c.ok)
		}
	}
}

func TestLoadDotenvSetsAndRespectsEnv(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	body := "# secrets\nTELEGRAM_BOT_TOKEN=111:aaa\nexport KIMI_API_KEY=\"sk-kimi\"\nALREADY_SET=fromfile\n"
	if err := os.WriteFile(env, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// A var already in the environment must NOT be overridden by the file.
	t.Setenv("ALREADY_SET", "fromenv")
	// Ensure the file-only vars start unset in this test process.
	os.Unsetenv("TELEGRAM_BOT_TOKEN")
	os.Unsetenv("KIMI_API_KEY")
	t.Cleanup(func() { os.Unsetenv("TELEGRAM_BOT_TOKEN"); os.Unsetenv("KIMI_API_KEY") })

	n := LoadDotenv(env)
	if n != 2 {
		t.Errorf("set %d vars, want 2 (the two unset ones)", n)
	}
	if got := os.Getenv("TELEGRAM_BOT_TOKEN"); got != "111:aaa" {
		t.Errorf("TELEGRAM_BOT_TOKEN = %q", got)
	}
	if got := os.Getenv("KIMI_API_KEY"); got != "sk-kimi" {
		t.Errorf("KIMI_API_KEY = %q (quotes should be stripped)", got)
	}
	if got := os.Getenv("ALREADY_SET"); got != "fromenv" {
		t.Errorf("real env should win, got %q", got)
	}
}

func TestLoadDotenvMissingFileIsFine(t *testing.T) {
	if n := LoadDotenv(filepath.Join(t.TempDir(), "nope.env")); n != 0 {
		t.Errorf("missing file should set nothing, got %d", n)
	}
}
