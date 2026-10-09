// Package server wires the pad store into an MCP server and runs it over a transport.
// The default transport is Streamable HTTP over a Unix domain socket (peercred +
// 0600/0700 — no open port, filesystem-gated); --stdio serves a host that spawns the
// process; --tcp is an opt-in loopback listener that REQUIRES bearer tokens. Transport
// choice never changes the tool surface.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/madnh/scratchpad/internal/buildinfo"
	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/mcpsrv"
	"github.com/madnh/scratchpad/internal/store"
)

// shutdownGrace is how long a stop waits for work already in flight. With the streams
// closed under it afterwards, this is the budget for finishing a tool call — not the
// time a clean shutdown takes.
const shutdownGrace = 5 * time.Second

// BuildMCPServer assembles the MCP server (the full tool surface) bound to the store.
// It is transport-agnostic.
func BuildMCPServer(st *store.Store, live *config.Live) *mcp.Server {
	ms := mcp.NewServer(&mcp.Implementation{
		// The server's own name is fixed for the life of the process: it is announced
		// once at initialize, so `instance` is one of the marker's COLD groups.
		Name:    live.Get().Instance,
		Version: buildinfo.Get().Version,
	}, nil)
	mcpsrv.New(st, live).AddTools(ms)
	return ms
}

// ServeStdio serves the MCP server over stdin/stdout for a trusted host that spawned
// this process. stdout belongs to the JSON-RPC stream — every diagnostic goes to
// stderr (the log package's default).
func ServeStdio(ctx context.Context, ms *mcp.Server) error {
	log.Printf("serving MCP over stdio")
	return ms.Run(ctx, &mcp.StdioTransport{})
}

// TCPOptions carries the opt-in loopback TCP listener's settings, resolved by the CLI
// (marker `tcp` group overlaid by flags).
type TCPOptions struct {
	Port int
	// TokenOverride is the credential set from --tcp-token-digest: "sha256:<hex>"
	// entries, raw tokens never stored. Non-nil means the operator pinned credentials on
	// the command line, so the marker is not consulted and these do NOT hot-reload — a
	// flag beats the marker, and a flag changes when the process does. Leave it nil to
	// take credentials from the marker's `auth.clients`, which do reload.
	TokenOverride  []string
	AllowedOrigins []string // exact-match Origin allow-list for browser clients
	Realm          string   // WWW-Authenticate realm (the running binary's name)
}

// ServeHTTP serves the MCP endpoint at /mcp over Streamable HTTP on the Unix socket,
// plus the opt-in loopback TCP listener when tcp is non-nil. The socket needs no
// bearer token — the 0600/0700 permissions and the peercred uid check gate it; the TCP
// listener requires a bearer token whose SHA-256 digest is configured, and guards
// Origin/Host against DNS-rebinding. It blocks until ctx is cancelled, then shuts down
// gracefully and removes the socket file.
func ServeHTTP(ctx context.Context, ms *mcp.Server, live *config.Live, socketPath string, tcp *TCPOptions) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return ms }, nil)

	udsMux := http.NewServeMux()
	udsMux.Handle("/mcp", mcpHandler)
	httpSrv := &http.Server{Handler: udsMux}

	uds, err := listenUnix(socketPath)
	if err != nil {
		return err
	}
	defer os.Remove(socketPath) // best-effort cleanup on exit
	log.Printf("serving MCP over unix socket %s", socketPath)

	errCh := make(chan error, 2)
	go func() { errCh <- httpSrv.Serve(uds) }()

	var tcpSrv *http.Server
	if tcp != nil {
		// Refuse at START when there is nothing to authenticate against. The guard also
		// denies on an empty set, so this is not the thing standing between the port and
		// the world — it is here so an operator who forgot the credentials finds out now,
		// from a message naming where they go, rather than from every client failing.
		if tcp.TokenOverride == nil && len(live.Get().AuthClients()) == 0 {
			_ = uds.Close()
			return fmt.Errorf("refusing to start TCP without credentials: add auth.clients ({\"name\": ..., \"digest\": \"sha256:<hex>\"}) to the marker config or pass --tcp-token-digest")
		}
		guarded, err := tcpGuard(*tcp, live, mcpHandler)
		if err != nil {
			_ = uds.Close()
			return err
		}
		tcpMux := http.NewServeMux()
		tcpMux.Handle("/mcp", guarded)
		tcpSrv = &http.Server{Handler: tcpMux}
		addr := fmt.Sprintf("127.0.0.1:%d", tcp.Port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			_ = uds.Close()
			return fmt.Errorf("listen tcp %s: %w", addr, err)
		}
		log.Printf("serving MCP over loopback tcp %s (opt-in, bearer token required)", addr)
		go func() { errCh <- tcpSrv.Serve(ln) }()
	}

	select {
	case <-ctx.Done():
		// Graceful, then firm. An MCP client parked on a long-lived stream is an ACTIVE
		// request, and Shutdown waits for those without cancelling them — so a plain
		// `return httpSrv.Shutdown(...)` turns Ctrl-C with a client attached into
		// "context deadline exceeded" and a non-zero exit. The grace lets an in-flight
		// tool call finish; anything still there afterwards is closed under it, and a
		// stop the operator asked for is reported as success either way.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if tcpSrv != nil {
			if err := tcpSrv.Shutdown(shutCtx); err != nil {
				_ = tcpSrv.Close()
			}
		}
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			log.Printf("a connection did not close within %s; closing it", shutdownGrace)
			_ = httpSrv.Close()
		}
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// tcpGuard wraps the MCP handler with the TCP transport's checks: an Origin/Host
// guard (browser-driven DNS-rebinding hardening) and mandatory bearer-token auth
// (tokens verified by SHA-256 digest, compared in constant time).
//
// It reads its credentials from `*config.Live` PER REQUEST. This used to take TCPOptions
// by value and bake the digest map into the closure once, which made it the one exception
// to this project's own law that config is read continuously and never frozen at startup.
// The cost of the exception was specific: revoking a token required a restart, and a
// restart severs every in-flight `pad wait` on the deployment — so the emergency operation
// was the most expensive one available.
//
// An explicit `--tcp-token-digest` is the one thing that does NOT reload, and that is the
// config model rather than an oversight: a flag beats the marker, and a flag is a process
// argument that changes when the process does. When the override is set the marker is not
// consulted at all, so an operator who pinned credentials on the command line cannot have
// them widened by a file edit.
//
// An empty credential set denies every request. That direction matters: a marker edited
// down to nothing locks the door rather than opening it.
func tcpGuard(opts TCPOptions, live *config.Live, next http.Handler) (http.Handler, error) {
	var override map[string]bool
	if opts.TokenOverride != nil {
		override = make(map[string]bool, len(opts.TokenOverride))
		for _, d := range opts.TokenOverride {
			hexPart, err := config.ParseTokenDigest(d)
			if err != nil {
				return nil, err
			}
			override[hexPart] = true
		}
	}
	realm := opts.Realm
	if realm == "" {
		realm = "restricted"
	}

	// markerDigests rebuilds the accepted set from the live marker. It is a handful of
	// entries hashed into a map on a transport that is opt-in and loopback-only, so the
	// per-request cost buys correctness at a price nobody can measure.
	markerDigests := func() map[string]bool {
		clients := live.Get().AuthClients()
		out := make(map[string]bool, len(clients))
		for _, cl := range clients {
			// Already validated at load; a malformed entry here would mean the marker
			// never loaded, in which case this is still the last GOOD config.
			if hexPart, err := config.ParseTokenDigest(cl.Digest); err == nil {
				out[hexPart] = true
			}
		}
		return out
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostIsLoopback(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, opts.AllowedOrigins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		digests := override
		if digests == nil {
			digests = markerDigests()
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !tokenMatches(digests, strings.TrimSpace(token)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

// tokenMatches hashes the presented token and compares it against the configured
// digests in constant time.
func tokenMatches(digests map[string]bool, token string) bool {
	if token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	got := hex.EncodeToString(sum[:])
	match := false
	for d := range digests {
		if subtle.ConstantTimeCompare([]byte(d), []byte(got)) == 1 {
			match = true
		}
	}
	return match
}

// hostIsLoopback accepts only loopback Host headers, defeating DNS-rebinding (a
// browser resolving evil.example to 127.0.0.1 still sends Host: evil.example).
func hostIsLoopback(host string) bool {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// originAllowed exact-matches a browser Origin against the allow-list. An empty list
// rejects every cross-origin browser request (non-browser clients send no Origin).
func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if origin == a {
			return true
		}
	}
	return false
}
