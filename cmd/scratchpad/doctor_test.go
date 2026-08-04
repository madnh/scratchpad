package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countOversized is the only thing that can now tell a person why a pad lists fine and
// refuses to open. It replaced a counter that could never fire — T1 built it on top of a
// read failure during List, and T3's streaming removed that failure — so this test exists
// mainly to stop the same thing happening a second time silently.
func TestCountOversized(t *testing.T) {
	projects := filepath.Join(t.TempDir(), "projects")
	demo := filepath.Join(projects, "demo")
	if err := os.MkdirAll(demo, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, size int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(demo, name), []byte(strings.Repeat("x", size)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("small1.md", 100)
	write("big123.md", 5000)
	write("big456.md", 5000)
	// Not pads: the store's own file, and something a person dropped in. Counting either
	// would report a problem that does not exist.
	write("_rules.md", 9000)
	write("notes.txt", 9000)

	if got := countOversized(projects, 1000); got != 2 {
		t.Errorf("countOversized = %d, want 2", got)
	}
	if got := countOversized(projects, 100000); got != 0 {
		t.Errorf("with a ceiling above everything: got %d, want 0", got)
	}
	// A missing store is a different diagnosis, reported elsewhere; this must not panic
	// or invent a count.
	if got := countOversized(filepath.Join(projects, "nope"), 1000); got != 0 {
		t.Errorf("missing dir: got %d, want 0", got)
	}
}
