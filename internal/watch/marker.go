package watch

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/madnh/scratchpad/internal/config"
)

// Marker watches the deployment's marker file and calls back when it changes, so a
// long-lived process picks up an edited config without being restarted.
//
// It is the same idea as Watcher one directory up, and for the same reason: watch the
// FILE, never the writers. The Web UI writing through the API, an operator in an editor,
// and a config management tool dropping a new marker in are all noticed identically, and
// none of them has to know a server is listening.
//
// It is a separate type rather than another source of Event because a marker is not a
// pad: Event carries a pad ref, is filtered by the store's naming law
// (pad.IsPadFileName), and flows straight out to browsers over SSE. Announcing a config
// change on that stream would hand every subscriber a ref that resolves to nothing.
type Marker struct {
	dir      string
	path     string
	onChange func()

	mu       sync.Mutex
	state    fileState
	known    bool
	degraded bool
}

// NewMarker builds a watcher over the marker inside a Scratchpad dir. onChange runs on
// the watcher's own goroutine, so it should be short — reloading is one small read.
// It touches no filesystem; Run does.
func NewMarker(dir string, onChange func()) *Marker {
	return &Marker{dir: dir, path: config.MarkerPath(dir), onChange: onChange}
}

// ReloadConfig is the callback every long-lived surface wants: re-read the marker and
// install it into live. It lives here rather than in each command so the two decisions
// below are made once, not once per surface.
//
// A marker that fails to load leaves the running config ALONE. Falling back to defaults
// would read a half-written file as "the operator wants the built-in settings", and the
// setting that would silently change is rules — the one where a wrong guess grants a
// permission nobody granted.
//
// Groups that cannot change under a running process are reported, never applied: a
// listener is already bound and a socket already named, so a config that claimed the new
// port would be lying about what the process is doing. See config.MergeHot.
func ReloadConfig(dir string, live *config.Live) *Marker {
	path := config.MarkerPath(dir)
	return NewMarker(dir, func() {
		fresh, err := config.LoadDir(dir)
		if err != nil {
			log.Printf("config: %s changed but could not be loaded (%v); keeping the configuration in use", path, err)
			return
		}
		if cold := config.ColdChanges(live.Get(), fresh); len(cold) > 0 {
			log.Printf("config: reloaded %s — %s changed too, and %s only on restart",
				path, strings.Join(cold, ", "), verb(len(cold), "applies", "apply"))
		} else {
			log.Printf("config: reloaded %s", path)
		}
		live.Apply(fresh)
	})
}

// verb picks the form for a count, so the reload line reads as English either way.
func verb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Degraded reports whether kernel notification is unavailable and this watcher is running
// on the rescan safety net alone (an edit is still seen, just up to rescanInterval later).
func (m *Marker) Degraded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.degraded
}

// Run watches until ctx is cancelled. It seeds from the marker's current state WITHOUT
// firing, so startup does not look like an edit.
//
// The watch is on the DIRECTORY, not on the marker itself: the file is replaced by
// rename (that is how a config write stays atomic), which leaves a file-level watch
// pointing at an inode nobody will ever write again — the first save would be reported
// and every one after it lost.
func (m *Marker) Run(ctx context.Context) error {
	m.seed()

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		m.mu.Lock()
		m.degraded = true
		m.mu.Unlock()
		log.Printf("watch: filesystem notification unavailable (%v); config reload falls back to a %s rescan", err, rescanInterval)
		return m.runRescanOnly(ctx)
	}
	defer fsw.Close()

	if err := fsw.Add(m.dir); err != nil {
		m.mu.Lock()
		m.degraded = true
		m.mu.Unlock()
		log.Printf("watch: cannot watch %s (%v); config reload falls back to a %s rescan", m.dir, err, rescanInterval)
		return m.runRescanOnly(ctx)
	}

	rescan := time.NewTicker(rescanInterval)
	defer rescan.Stop()

	var debounce <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if filepath.Base(ev.Name) == config.MarkerFilename && debounce == nil {
				debounce = time.After(debounceInterval)
			}

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			log.Printf("watch: %v", err) // never fatal: the rescan still covers us

		case <-debounce:
			debounce = nil
			m.fireIfChanged()

		case <-rescan.C:
			m.fireIfChanged()
		}
	}
}

// runRescanOnly is the degraded loop: no kernel notification, just the safety net.
func (m *Marker) runRescanOnly(ctx context.Context) error {
	t := time.NewTicker(rescanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			m.fireIfChanged()
		}
	}
}

// seed records the marker's current state without firing.
func (m *Marker) seed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, err := stat(m.path); err == nil {
		m.state, m.known = st, true
	}
}

// fireIfChanged compares the marker against the last known (mtime, size) and calls back
// only on a real difference. That is what collapses the burst of events one save
// produces into one reload, and what makes the periodic rescan idempotent.
//
// A marker that has VANISHED is deliberately not a change: an editor writing through a
// temp file removes the old one for an instant, and reacting to that would reload from a
// file that is not there. The next write is a difference again and gets reported.
func (m *Marker) fireIfChanged() {
	st, err := stat(m.path)
	if err != nil {
		return
	}

	m.mu.Lock()
	same := m.known && m.state == st
	m.state, m.known = st, true
	m.mu.Unlock()

	if !same && m.onChange != nil {
		m.onChange()
	}
}
