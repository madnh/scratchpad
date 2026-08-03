// Package watch turns pad-file writes into a PUSH stream of events.
//
// The pad file is the single source of truth (turn state is derived from it, nothing
// lives outside it), so this package watches the STORE, not the writers: whoever
// appends — the CLI, the MCP server, a future binary, or a human with an editor or
// `rm` — is noticed the same way. No writer has to cooperate, and internal/store
// keeps knowing nothing about who is listening.
//
// The mechanism is the kernel's filesystem-notification facility (inotify on Linux,
// kqueue/FSEvents on the BSDs and macOS) via fsnotify: the watcher blocks until the
// kernel wakes it, so it is push with millisecond latency, not a polling loop. Two
// safeguards sit around it, because filesystem notification is best-effort:
//
//   - a slow full rescan (rescanInterval) catches anything the kernel dropped, and is
//     the ONLY mechanism on filesystems where notification silently does not work
//     (some network and container filesystems);
//   - if fsnotify cannot start at all the watcher degrades to that rescan and reports
//     Degraded() so a caller can say so out loud instead of going quietly blind.
//
// Emission is state-based, not event-based: every candidate path is stat-ed and
// compared against the last known (mtime, size). That collapses the several write
// events one append produces into one Event, and makes the rescan idempotent — it
// re-emits nothing that the kernel already reported.
package watch

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/madnh/scratchpad/internal/pad"
)

// debounceInterval is how long the watcher waits for a burst of kernel events to
// settle before emitting. One append can produce several write events; a reader also
// wants the writer's exclusive flock to be released before it tries a shared one.
const debounceInterval = 50 * time.Millisecond

// rescanInterval is the safety net's period — deliberately slow: it is a backstop for
// dropped or unsupported kernel notifications, never the primary mechanism.
const rescanInterval = 30 * time.Second

// subscriberBuffer is the per-subscriber queue depth. Events carry no payload beyond
// an identifier, so a subscriber that falls this far behind has bigger problems than
// a coalesced notification.
const subscriberBuffer = 256

// Event reports that one pad's file changed on disk (an append, or a create) or
// vanished (deleted). It deliberately carries no pad content: a subscriber re-reads
// through internal/store, which applies the shared flock and the password gate.
type Event struct {
	Project string `json:"project"`
	ID      string `json:"pad_id"`
	Ref     string `json:"ref"`
	Removed bool   `json:"removed"`
}

// fileState is the stat-level fingerprint used to decide whether a pad really changed.
type fileState struct {
	mod  time.Time
	size int64
}

// Watcher streams pad-file changes under a projects directory to any number of
// subscribers. The zero value is not usable — call New.
type Watcher struct {
	projectsDir string

	mu       sync.Mutex
	subs     map[int]chan Event
	nextSub  int
	snapshot map[string]fileState
	degraded bool
}

// New builds a Watcher over a store's projects directory. It does not touch the
// filesystem — Run does.
func New(projectsDir string) *Watcher {
	return &Watcher{
		projectsDir: projectsDir,
		subs:        make(map[int]chan Event),
		snapshot:    make(map[string]fileState),
	}
}

// Degraded reports whether kernel notification is unavailable and the watcher is
// running on the rescan safety net alone (changes are still seen, just later).
func (w *Watcher) Degraded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.degraded
}

// Subscribe returns a channel of events plus a function that unsubscribes and closes
// it. Sends are non-blocking: a subscriber that stops draining loses events rather
// than stalling the watcher.
func (w *Watcher) Subscribe() (<-chan Event, func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.nextSub
	w.nextSub++
	ch := make(chan Event, subscriberBuffer)
	w.subs[id] = ch
	return ch, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if c, ok := w.subs[id]; ok {
			delete(w.subs, id)
			close(c)
		}
	}
}

// Run watches until ctx is cancelled. It seeds its state from the current contents of
// the store WITHOUT emitting, so subscribers only hear about what happens from now on.
// It returns nil on a clean shutdown; a failure to start kernel notification is not an
// error — it degrades to the rescan safety net and says so in the log.
func (w *Watcher) Run(ctx context.Context) error {
	w.seed()

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		w.mu.Lock()
		w.degraded = true
		w.mu.Unlock()
		log.Printf("watch: filesystem notification unavailable (%v); falling back to a %s rescan", err, rescanInterval)
		return w.runRescanOnly(ctx)
	}
	defer fsw.Close()

	// Watch the projects root (so a new project directory is noticed) and every
	// project directory that exists now. Directories, not files: one watch per
	// project keeps the count at 1+N however many pads the store holds.
	w.addWatch(fsw, w.projectsDir)
	for _, dir := range w.projectDirs() {
		w.addWatch(fsw, dir)
	}

	rescan := time.NewTicker(rescanInterval)
	defer rescan.Stop()

	pending := make(map[string]bool)
	var debounce <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			// A newly created project directory needs its own watch, and may
			// already contain a pad by the time we get here.
			if ev.Has(fsnotify.Create) && filepath.Dir(ev.Name) == w.projectsDir && isDir(ev.Name) {
				w.addWatch(fsw, ev.Name)
				for _, p := range padFilesIn(ev.Name) {
					pending[p] = true
				}
			}
			if isPadPath(ev.Name) {
				pending[ev.Name] = true
			}
			if len(pending) > 0 && debounce == nil {
				debounce = time.After(debounceInterval)
			}

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			// Never fatal: the rescan still covers us.
			log.Printf("watch: %v", err)

		case <-debounce:
			debounce = nil
			paths := make([]string, 0, len(pending))
			for p := range pending {
				paths = append(paths, p)
			}
			pending = make(map[string]bool)
			w.emitChanged(paths)

		case <-rescan.C:
			w.rescan()
		}
	}
}

// runRescanOnly is the degraded loop: no kernel notification, just the safety net.
func (w *Watcher) runRescanOnly(ctx context.Context) error {
	t := time.NewTicker(rescanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			w.rescan()
		}
	}
}

// addWatch registers a directory, tolerating a race where it vanished meanwhile.
func (w *Watcher) addWatch(fsw *fsnotify.Watcher, dir string) {
	if err := fsw.Add(dir); err != nil && !os.IsNotExist(err) {
		log.Printf("watch: cannot watch %s: %v", dir, err)
	}
}

// seed records the current state of every pad without emitting anything.
func (w *Watcher) seed() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, path := range w.allPadFiles() {
		if st, err := stat(path); err == nil {
			w.snapshot[path] = st
		}
	}
}

// rescan diffs the whole store against the snapshot — the backstop for kernel events
// that were dropped or never arrive. It emits exactly what the kernel path would have.
func (w *Watcher) rescan() {
	paths := w.allPadFiles()

	w.mu.Lock()
	for path := range w.snapshot {
		paths = append(paths, path) // include vanished files so they emit a removal
	}
	w.mu.Unlock()

	w.emitChanged(paths)
}

// emitChanged stats each path, compares it with the snapshot, and publishes an Event
// only where the state actually differs — which is what makes duplicate kernel events
// and the periodic rescan both harmless.
func (w *Watcher) emitChanged(paths []string) {
	var events []Event

	w.mu.Lock()
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] || !isPadPath(path) {
			continue
		}
		seen[path] = true

		st, err := stat(path)
		if err != nil {
			if _, known := w.snapshot[path]; known {
				delete(w.snapshot, path)
				events = append(events, w.eventFor(path, true))
			}
			continue
		}
		if prev, known := w.snapshot[path]; known && prev == st {
			continue
		}
		w.snapshot[path] = st
		events = append(events, w.eventFor(path, false))
	}
	subs := make([]chan Event, 0, len(w.subs))
	for _, ch := range w.subs {
		subs = append(subs, ch)
	}
	w.mu.Unlock()

	for _, ev := range events {
		for _, ch := range subs {
			select {
			case ch <- ev:
			default: // a subscriber that stopped draining must not stall the watcher
			}
		}
	}
}

// eventFor derives the pad identity from the file's location: <projects>/<p>/<id>.md.
func (w *Watcher) eventFor(path string, removed bool) Event {
	project := filepath.Base(filepath.Dir(path))
	id := strings.TrimSuffix(filepath.Base(path), ".md")
	return Event{Project: project, ID: id, Ref: project + "-" + id, Removed: removed}
}

// projectDirs lists the immediate subdirectories of the projects root.
func (w *Watcher) projectDirs() []string {
	entries, err := os.ReadDir(w.projectsDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(w.projectsDir, e.Name()))
		}
	}
	return dirs
}

// allPadFiles lists every pad file currently in the store.
func (w *Watcher) allPadFiles() []string {
	var out []string
	for _, dir := range w.projectDirs() {
		out = append(out, padFilesIn(dir)...)
	}
	return out
}

// padFilesIn lists the pad files directly inside one project directory.
func padFilesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && pad.IsPadFileName(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// isPadPath filters out editor swap files, dotfiles, the store's own files and anything
// else that is not a pad. It uses the same naming law as the store, so a change to
// `_rules.md` never announces itself as a pad that does not exist — a subscriber would
// then fetch a ref nobody can resolve.
func isPadPath(path string) bool {
	return pad.IsPadFileName(filepath.Base(path))
}

// isDir reports whether path is a directory right now.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// stat reduces a file to the fingerprint the watcher compares.
func stat(path string) (fileState, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileState{}, err
	}
	return fileState{mod: fi.ModTime(), size: fi.Size()}, nil
}
