package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// confirmWriteTTY asks on the terminal before a mutating tool call (write_file,
// edit_file, bash, …) — the CLI works in the user's own directory, so writes
// need consent. Non-interactive runs (piped stdin) can't consent and are
// denied; -yes skips the gate entirely.
func confirmWriteTTY() func(name, detail string) bool {
	return confirmWriteReader(bufio.NewReader(os.Stdin))
}

// confirmWriteReader is the TTY confirm over a caller-owned stdin reader — the
// chat REPL shares its reader so the gate and the prompt loop don't fight over
// buffered bytes.
func confirmWriteReader(reader *bufio.Reader) func(name, detail string) bool {
	return func(name, detail string) bool {
		if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintf(os.Stderr, "✗ %s blocked (non-interactive; run with -yes to allow writes)\n", name)
			return false
		}
		fmt.Fprintf(os.Stderr, "⚠ %s: %s\nallow? [y/N] ", name, detail)
		line, _ := reader.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		}
		fmt.Fprintln(os.Stderr, "✗ denied")
		return false
	}
}
