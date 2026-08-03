package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/madnh/scratchpad/internal/store"
	"github.com/madnh/scratchpad/internal/watch"
)

// heartbeatInterval keeps an idle SSE stream alive and lets the browser notice a dead
// server promptly. EventSource reconnects on its own, so a missed beat is not fatal.
const heartbeatInterval = 20 * time.Second

// clientBuffer is the per-browser queue depth for pushed events.
const clientBuffer = 64

// padEvent is what a browser receives when a pad changes. It carries METADATA ONLY —
// exactly the level `pad list` already publishes — so a notification about a protected
// pad reveals nothing its listing entry does not. Section bodies are fetched
// separately, through the password gate.
type padEvent struct {
	Type         string `json:"type"` // "changed" | "removed"
	Ref          string `json:"ref"`
	Project      string `json:"project"`
	Title        string `json:"title,omitempty"`
	SectionCount int    `json:"section_count,omitempty"`
	LastAuthor   string `json:"last_author,omitempty"`
	LastTitle    string `json:"last_title,omitempty"` // empty for a protected pad
	LastTS       int64  `json:"last_ts,omitempty"`
	Protected    bool   `json:"protected,omitempty"`

	// Routing and task fields, so a notification can say "T3 → done" rather than "the
	// pad changed", and so the task panel stays live without refetching.
	//
	// They say MORE than a listing entry does, so they follow exactly the rule the last
	// section's title already follows: omitted entirely for a protected pad. The
	// boundary is the level `pad list` publishes — not whatever the UI finds useful.
	LastKind   string `json:"last_kind,omitempty"`
	LastTask   int    `json:"last_task,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
	OpenTasks  int    `json:"open_tasks,omitempty"`
}

// hub turns the watcher's single event stream into a broadcast to every open browser,
// reading each changed pad's metadata ONCE rather than once per connected client.
type hub struct {
	store   *store.Store
	watcher *watch.Watcher

	mu      sync.Mutex
	clients map[int]chan padEvent
	next    int
	closed  bool // set once run() stops; a late subscriber gets a closed channel
}

func newHub(st *store.Store, w *watch.Watcher) *hub {
	return &hub{store: st, watcher: w, clients: make(map[int]chan padEvent)}
}

// subscribe registers a browser and returns its queue plus an unsubscribe function.
//
// After the hub has stopped it hands back an already-closed channel rather than a live
// one: a request that arrives during shutdown must end, not wait for an event that can
// no longer come.
func (h *hub) subscribe() (<-chan padEvent, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		ch := make(chan padEvent)
		close(ch)
		return ch, func() {}
	}
	id := h.next
	h.next++
	ch := make(chan padEvent, clientBuffer)
	h.clients[id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.clients[id]; ok {
			delete(h.clients, id)
			close(c)
		}
	}
}

// run pumps watcher events into browser queues until ctx is cancelled, then closes
// every queue on the way out.
//
// Closing them is what makes Ctrl-C prompt. `http.Server.Shutdown` waits for handlers
// to return and does NOT cancel their request contexts, so an open browser tab parked
// in /api/events would sit there until the shutdown deadline expired — which is exactly
// how a clean stop came to print "context deadline exceeded".
func (h *hub) run(ctx context.Context) {
	events, unsub := h.watcher.Subscribe()
	defer unsub()
	defer h.close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			h.broadcast(h.enrich(ev))
		}
	}
}

// close ends every open stream. The handlers see their channel close and return; the
// `closed` flag stops a racing subscribe() from being handed a channel nobody will
// ever write to or close again.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.clients {
		delete(h.clients, id)
		close(ch)
	}
}

// enrich turns a bare file event into the metadata a browser can render. A pad that
// cannot be read (deleted between the event and this read, or mid-write) degrades to
// the identity alone rather than dropping the notification.
func (h *hub) enrich(ev watch.Event) padEvent {
	if ev.Removed {
		return padEvent{Type: "removed", Ref: ev.Ref, Project: ev.Project}
	}
	out := padEvent{Type: "changed", Ref: ev.Ref, Project: ev.Project}
	m, lastTitle, err := h.store.Meta(ev.Ref)
	if err != nil {
		return out
	}
	out.Title = m.Title
	out.SectionCount = m.SectionCount
	out.LastAuthor = m.LastAuthor
	out.LastTitle = lastTitle
	out.LastTS = m.LastTS
	out.Protected = m.Protected
	if m.Protected {
		return out // nothing beyond the listing level leaves here
	}
	out.OpenTasks = m.OpenTasks
	if last, err := h.store.LastSection(ev.Ref); err == nil {
		out.LastKind = string(last.Kind)
		out.LastTask = last.Task
		out.LastStatus = string(last.Status)
	}
	return out
}

// broadcast fans an event out without blocking on a browser that stopped reading.
func (h *hub) broadcast(ev padEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.clients {
		select {
		case ch <- ev:
		default:
		}
	}
}

// handleEvents streams pad changes to one browser as Server-Sent Events.
//
// SSE rather than WebSocket: the traffic is one-way (server → browser), it rides
// ordinary HTTP with no upgrade handshake, and EventSource reconnects by itself — a
// restart of this server heals without any client-side retry logic.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Tell the browser to wait a beat before reconnecting, so a server restart does
	// not turn into a reconnect storm.
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	events, unsub := s.hub.subscribe()
	defer unsub()

	beat := time.NewTicker(heartbeatInterval)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: pad\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-beat.C:
			// A comment line: valid SSE, ignored by EventSource, keeps the
			// connection (and any proxy in between) from going idle.
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
