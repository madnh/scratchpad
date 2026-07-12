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

// BuildMCPServer assembles the MCP server (the full tool surface) bound to the store.
// It is transport-agnostic.
func BuildMCPServer(st *store.Store, cfg config.Config) *mcp.Server {
	ms := mcp.NewServer(&mcp.Implementation{
		Name:    cfg.Instance,
		Version: buildinfo.Get().Version,
	}, nil)
	mcpsrv.New(st, cfg).AddTools(ms)
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
	Port           int
	TokenDigests   []string // "sha256:<hex>" entries; raw tokens are never stored
	AllowedOrigins []string // exact-match Origin allow-list for browser clients
	Realm          string   // WWW-Authenticate realm (the running binary's name)
}

// ServeHTTP serves the MCP endpoint at /mcp over Streamable HTTP on the Unix socket,
// plus the opt-in loopback TCP listener when tcp is non-nil. The socket needs no
// bearer token — the 0600/0700 permissions and the peercred uid check gate it; the TCP
// listener requires a bearer token whose SHA-256 digest is configured, and guards
// Origin/Host against DNS-rebinding. It blocks until ctx is cancelled, then shuts down
// gracefully and removes the socket file.
func ServeHTTP(ctx context.Context, ms *mcp.Server, socketPath string, tcp *TCPOptions) error {
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
		if len(tcp.TokenDigests) == 0 {
			_ = uds.Close()
			return fmt.Errorf("refusing to start TCP without tokens: add tcp.token_digests (sha256:<hex>) to the marker config or pass --tcp-token-digest")
		}
		guarded, err := tcpGuard(*tcp, mcpHandler)
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
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if tcpSrv != nil {
			_ = tcpSrv.Shutdown(shutCtx)
		}
		return httpSrv.Shutdown(shutCtx)
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
func tcpGuard(opts TCPOptions, next http.Handler) (http.Handler, error) {
	digests := make(map[string]bool, len(opts.TokenDigests))
	for _, d := range opts.TokenDigests {
		hexPart, ok := strings.CutPrefix(strings.TrimSpace(d), "sha256:")
		if !ok || len(hexPart) != 64 {
			return nil, fmt.Errorf("bad token digest %q: want \"sha256:<64 hex chars>\"", d)
		}
		digests[strings.ToLower(hexPart)] = true
	}
	realm := opts.Realm
	if realm == "" {
		realm = "restricted"
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
