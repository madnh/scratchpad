package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/config"
)

// runMarker starts a Marker over dir and returns a channel that receives once per
// detected change.
func runMarker(t *testing.T, dir string) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{}, 16)
	m := NewMarker(dir, func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	// Run seeds before it watches; give it that moment so the first write below is
	// genuinely observed as a change rather than swallowed by the seed.
	time.Sleep(100 * time.Millisecond)
	return ch
}

func fired(t *testing.T, ch <-chan struct{}, within time.Duration) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(within):
		return false
	}
}

func writeMarkerFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(config.MarkerPath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMarkerSeesAnInPlaceWrite(t *testing.T) {
	dir := t.TempDir()
	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1}`)

	ch := runMarker(t, dir)
	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1,"display_name":"edited"}`)

	if !fired(t, ch, 3*time.Second) {
		t.Fatal("an edit to the marker was not noticed")
	}
}

// The write path this feature ships uses temp file + rename. A watch on the FILE would
// survive exactly one of these and then point at an inode nobody writes again, so this is
// the case that matters most.
func TestMarkerSeesARenameOverIt(t *testing.T) {
	dir := t.TempDir()
	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1}`)

	ch := runMarker(t, dir)

	for i, body := range []string{
		`{"type":"scratchpad","version":1,"display_name":"one"}`,
		`{"type":"scratchpad","version":1,"display_name":"two"}`,
	} {
		tmp := filepath.Join(dir, "marker.tmp")
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, config.MarkerPath(dir)); err != nil {
			t.Fatal(err)
		}
		if !fired(t, ch, 3*time.Second) {
			t.Fatalf("rename %d was not noticed", i+1)
		}
	}
}

// A pad changing must not wake the config reload, and vice versa: they are separate
// streams because they mean different things.
func TestMarkerIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1}`)

	ch := runMarker(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "_rules.md"), []byte("be nice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fired(t, ch, 500*time.Millisecond) {
		t.Fatal("a neighbouring file was reported as a config change")
	}
}

// ReloadConfig is the callback every long-lived surface installs: hot groups adopted,
// cold groups reported but left alone.
func TestReloadConfigAppliesHotOnly(t *testing.T) {
	dir := t.TempDir()
	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1,"instance":"prod"}`)
	running, err := config.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(running)

	m := ReloadConfig(dir, live)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(100 * time.Millisecond)

	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1,"instance":"renamed",
		"limits":{"max_sections_per_pad":4242}}`)

	deadline := time.After(3 * time.Second)
	for {
		got := live.Get()
		if got.Limits.MaxSectionsPerPad == 4242 {
			if got.Instance != "prod" {
				t.Fatalf("a cold group was adopted: instance = %q", got.Instance)
			}
			if got.SocketPath != running.SocketPath {
				t.Fatalf("socket path moved under the process: %q", got.SocketPath)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the new limit never arrived (still %d)", got.Limits.MaxSectionsPerPad)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A marker that does not parse must leave the running config alone. Falling back to the
// defaults would quietly widen the rules policy — the one setting where a wrong guess
// grants a permission nobody granted.
func TestReloadConfigKeepsRunningConfigOnBadMarker(t *testing.T) {
	dir := t.TempDir()
	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1,
		"rules":{"store":"ui","project":"ui","pad":"opener"},
		"limits":{"max_sections_per_pad":10}}`)
	running, err := config.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(running)

	m := ReloadConfig(dir, live)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(100 * time.Millisecond)

	writeMarkerFile(t, dir, `{"type":"scratchpad","version":1, oh no`)
	time.Sleep(500 * time.Millisecond)

	got := live.Get()
	if got.Rules != running.Rules || !config.SameLimits(got.Limits, running.Limits) {
		t.Fatalf("a broken marker changed the running config: %+v", got)
	}
}
