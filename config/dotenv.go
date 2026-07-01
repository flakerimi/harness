package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// DotenvPaths are the .env files loaded at startup, most specific first: a
// project-local ./.env, then a global <user-config>/harness/.env. A value
// already present in the real environment always wins, so .env is a convenience,
// never an override — consistent with how the rest of the harness treats env.
func DotenvPaths() []string {
	paths := []string{".env"}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "harness", ".env"))
	}
	return paths
}

// LoadDotenv reads KEY=VALUE lines from the given .env files and sets each var
// that isn't already in the environment (so real env vars, and earlier files,
// win). Missing files are skipped silently. Returns how many vars it set.
func LoadDotenv(paths ...string) int {
	n := 0
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := parseDotenvLine(sc.Text())
			if !ok {
				continue
			}
			if _, exists := os.LookupEnv(k); exists {
				continue // real env and earlier .env files take precedence
			}
			if os.Setenv(k, v) == nil {
				n++
			}
		}
		f.Close()
	}
	return n
}

// parseDotenvLine parses a single KEY=VALUE line, tolerating a leading `export`
// and surrounding single/double quotes. Blank and comment (#) lines return
// ok=false. Values are not variable-expanded.
func parseDotenvLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	k, v, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	if k == "" {
		return "", "", false
	}
	return k, v, true
}
