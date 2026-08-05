// Package webui serves the human-facing Web UI: a read-only view of the pad store
// with live updates, plus deleting a pad. It exists because `pad wait` is an AGENT
// ergonomic — a blocking command whose exit code wakes a program — and gives a person
// nothing to look at.
//
// It is a separate listener from `serve`, not another MCP transport:
//
//   - A browser cannot speak to a Unix socket, so this needs TCP; it binds loopback
//     only, guards the Host header (a page on the internet resolving its own name to
//     127.0.0.1 still sends its own Host), and rejects cross-origin writes.
//   - Auth is browser-shaped: a one-time token in the URL printed at startup, swapped
//     for an HttpOnly session cookie, rather than MCP's bearer tokens.
//   - The surface WRITES NOTHING INTO a pad. A person watching a conversation is not a
//     participant in it: posting requires an author identity and would have to obey the
//     turn rule, which belongs to the agents' surfaces. The one mutating action is
//     deleting a whole pad, one at a time — and, exactly as in the CLI, deletion is not
//     gated by the pad password, which protects CONTENT rather than existence. Bulk
//     cleanup by age stays in the CLI's `pad purge`, where the victim list is printed
//     and confirmed first.
//
// Everything goes through internal/store, so the UI observes the same flock discipline
// and password gate as the CLI and the MCP server — there is no second disk path.
package webui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
	"github.com/madnh/scratchpad/internal/watch"
)

// shutdownGrace bounds a stop. With the streams closed first there is normally nothing
// left to wait for, so this is a backstop against one wedged handler, not the budget a
// clean shutdown spends.
const shutdownGrace = 3 * time.Second

// Options configures a UI server. Port and NoAuth come from the resolved config
// (flag > env > marker > default).
type Options struct {
	Port   int
	NoAuth bool
}

// Server is the assembled UI: the store it reads, the watcher that pushes changes,
// the session table, and the SSE hub.
type Server struct {
	store   *store.Store
	live    *config.Live
	opts    Options
	watcher *watch.Watcher
	// marker keeps live in step with the config file. The UI needs it even more than the
	// other surfaces do: it is the one that WRITES the marker, and a save that changed
	// nothing until a restart would be a lie told by its own Save button.
	marker *watch.Marker
	auth   *authState
	hub    *hub
}

// New assembles a UI server over a store. It touches no sockets and no filesystem —
// Run does that.
func New(st *store.Store, live *config.Live, opts Options) (*Server, error) {
	if opts.Port <= 0 {
		opts.Port = config.DefaultUIPort
	}
	auth, err := newAuthState(opts.NoAuth)
	if err != nil {
		return nil, err
	}
	cfg := live.Get()
	w := watch.New(cfg.ProjectsDir)
	return &Server{
		store:   st,
		live:    live,
		opts:    opts,
		watcher: w,
		marker:  watch.ReloadConfig(cfg.RootDir, live),
		auth:    auth,
		hub:     newHub(st, w),
	}, nil
}

// URL is the address a person should open: the loopback origin plus, unless auth is
// disabled, the one-time token that establishes the session.
func (s *Server) URL() string {
	u := fmt.Sprintf("http://127.0.0.1:%d/", s.opts.Port)
	if s.auth.token != "" {
		u += "?t=" + s.auth.token
	}
	return u
}

// Run listens and serves until ctx is cancelled, then shuts down gracefully. The
// watcher and the SSE hub run alongside for the lifetime of the server.
func (s *Server) Run(ctx context.Context) error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.opts.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w (another instance may already be running; pass --port)", addr, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		if err := s.watcher.Run(ctx); err != nil {
			log.Printf("ui: watcher stopped: %v", err)
		}
	}()
	go func() {
		if err := s.marker.Run(ctx); err != nil {
			log.Printf("ui: config watcher stopped: %v", err)
		}
	}()
	go s.hub.run(ctx)

	srv := &http.Server{
		Handler: s.handler(),
		// No write timeout: /api/events is a long-lived stream. Reads are bounded
		// because every request body this server accepts is small.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		// Stop the hub FIRST. It closes every open SSE stream, so their handlers return
		// and Shutdown has nothing left to wait for. Without this, one browser tab is
		// enough to hold the shutdown open until the deadline.
		cancel()
		shutCtx, stop := context.WithTimeout(context.Background(), shutdownGrace)
		defer stop()
		if err := srv.Shutdown(shutCtx); err != nil {
			// Graceful, then firm. The stop was asked for and the process is leaving
			// either way, so a connection that will not drain gets closed under it —
			// and Ctrl-C still exits 0, because a stop that happened is not a failure.
			log.Printf("ui: a connection did not close within %s; closing it", shutdownGrace)
			_ = srv.Close()
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handler builds the route table. Static assets are served from the embedded FS at
// the root; everything under /api is JSON (or, for /api/events, an SSE stream).
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", s.api(s.handleStatus))
	mux.HandleFunc("GET /api/projects", s.api(s.handleProjects))
	mux.HandleFunc("GET /api/pads", s.api(s.handlePads))
	mux.HandleFunc("GET /api/pads/{ref}", s.api(s.handlePad))
	mux.HandleFunc("GET /api/pads/{ref}/sections", s.api(s.handleSections))
	mux.HandleFunc("GET /api/pads/{ref}/sections/{n}/preview", s.api(s.handleSectionPreview))
	mux.HandleFunc("GET /api/pads/{ref}/tasks", s.api(s.handleTasks))
	mux.HandleFunc("GET /api/stuck", s.api(s.handleStuck))
	mux.HandleFunc("POST /api/pads/{ref}/unlock", s.api(s.handleUnlock))
	mux.HandleFunc("DELETE /api/pads/{ref}", s.api(s.handleDelete))

	// The deployment settings — the one thing a person edits here that is not a pad at
	// all. See config_api.go for the groups it deliberately leaves alone.
	mux.HandleFunc("GET /api/config", s.api(s.handleConfig))
	mux.HandleFunc("PUT /api/config", s.api(s.handleSetConfig))

	// Rules are the only pad content this UI writes, and the only reason it can: a rules
	// section does not take the turn, and it is authored by pad.SystemAuthor rather than
	// impersonating an agent. Every write here goes through `secure`, which requires a
	// same-origin Origin on any non-GET.
	mux.HandleFunc("GET /api/rules", s.api(s.handleStoreRules))
	mux.HandleFunc("PUT /api/rules", s.api(s.handleSetStoreRules))
	mux.HandleFunc("GET /api/projects/{name}/rules", s.api(s.handleProjectRules))
	mux.HandleFunc("PUT /api/projects/{name}/rules", s.api(s.handleSetProjectRules))
	// How far an announcement would reach, for the checkbox that sends one. Read-only and
	// GET: it counts pads, it does not touch them.
	mux.HandleFunc("GET /api/rules/notify-targets", s.api(s.handleRulesNotifyTargets))
	mux.HandleFunc("GET /api/projects/{name}/rules/notify-targets", s.api(s.handleRulesNotifyTargets))
	mux.HandleFunc("PUT /api/pads/{ref}/rules", s.api(s.handleSetPadRules))
	mux.HandleFunc("GET /api/events", s.requireSession(http.HandlerFunc(s.handleEvents)))

	mux.Handle("GET /", s.requireSession(s.assetHandler()))

	return s.secure(mux)
}
