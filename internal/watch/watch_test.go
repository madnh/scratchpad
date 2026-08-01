package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitEvent drains until an event arrives or the deadline passes.
func waitEvent(t *testing.T, ch <-chan Event, within time.Duration) (Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(within):
		return Event{}, false
	}
}

// newRunning starts a watcher over a temp projects dir and returns it plus a
// subscription, tearing both down at the end of the test.
func newRunning(t *testing.T, projectsDir string) <-chan Event {
	t.Helper()
	w := New(projectsDir)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	ch, unsub := w.Subscribe()
	t.Cleanup(unsub)
	// Run seeds its snapshot before it starts watching; give it that moment so the
	// test's first write is genuinely observed as a change.
	time.Sleep(100 * time.Millisecond)
	return ch
}

func TestWatcherSeesAppend(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "demo")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	pad := filepath.Join(proj, "ab3k9x.md")
	if err := os.WriteFile(pad, []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ch := newRunning(t, root)

	f, err := os.OpenFile(pad, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("# 2 - bob - reply\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	ev, ok := waitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("no event for an append")
	}
	if ev.Ref != "demo-ab3k9x" || ev.Removed {
		t.Fatalf("got %+v, want a change on demo-ab3k9x", ev)
	}
}

func TestWatcherSeesNewPadInNewProject(t *testing.T) {
	root := t.TempDir()
	ch := newRunning(t, root)

	proj := filepath.Join(root, "fresh")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	// The pad may land before the new directory's watch is registered; the rescan
	// covers that in production, but the test must not depend on a 30s tick, so
	// give the Create event a moment to be processed first.
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(proj, "zz9p2q.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ev, ok := waitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("no event for a pad created in a new project")
	}
	if ev.Ref != "fresh-zz9p2q" || ev.Removed {
		t.Fatalf("got %+v, want a change on fresh-zz9p2q", ev)
	}
}

func TestWatcherSeesDelete(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "demo")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	pad := filepath.Join(proj, "ab3k9x.md")
	if err := os.WriteFile(pad, []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ch := newRunning(t, root)

	if err := os.Remove(pad); err != nil {
		t.Fatal(err)
	}
	ev, ok := waitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("no event for a removed pad")
	}
	if ev.Ref != "demo-ab3k9x" || !ev.Removed {
		t.Fatalf("got %+v, want a removal of demo-ab3k9x", ev)
	}
}

// An unchanged store must stay silent: emission is state-based, so a rescan over
// files nobody touched publishes nothing.
func TestRescanIsIdempotent(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "demo")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "ab3k9x.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	w := New(root)
	w.seed()
	ch, unsub := w.Subscribe()
	defer unsub()

	w.rescan()
	w.rescan()

	if ev, ok := waitEvent(t, ch, 200*time.Millisecond); ok {
		t.Fatalf("rescan of an untouched store emitted %+v", ev)
	}
}

// Non-pad files (editor swap files, dotfiles, stray junk) must never surface.
func TestNonPadFilesIgnored(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "demo")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}

	ch := newRunning(t, root)

	for _, name := range []string{".ab3k9x.md.swp", "notes.txt", ".hidden.md"} {
		if err := os.WriteFile(filepath.Join(proj, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if ev, ok := waitEvent(t, ch, 500*time.Millisecond); ok {
		t.Fatalf("emitted %+v for a non-pad file", ev)
	}
}
