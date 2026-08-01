package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cookie names. The session cookie is HttpOnly (script must never be able to read or
// exfiltrate it); it is not Secure because the UI is plain HTTP on loopback, which
// browsers already treat as a trustworthy origin.
const (
	sessionCookie = "scratchpad_ui_session"
	tokenParam    = "t"
)

// maxSessions bounds the session table. One person with a few tabs needs a handful;
// anything beyond that is a client that drops the cookie on every request (a script,
// or a cross-site tab whose cookie SameSite=Strict withholds), and without a bound
// each such request would leak a session for the life of the process. When it is
// reached the oldest session is evicted — a live browser refreshes its own session's
// timestamp on every poll, so the one thrown away is the stale one.
const maxSessions = 64

// authState holds the one-time URL token and the live sessions. Sessions live in
// memory only: restarting the server invalidates them, which is the right default for
// a tool a person starts, uses, and stops.
type authState struct {
	noAuth bool
	token  string // "" when auth is disabled

	mu       sync.Mutex
	sessions map[string]*session
}

// sessionCtxKey carries the session from the auth middleware to the handler. It has to
// travel in the context rather than be looked up again from the request: the request
// that MINTS a session carries no cookie yet (the cookie is only in the response), so
// a second lookup would come back nil and the handler would work with no session.
type sessionCtxKey struct{}

// sessionFrom returns the session the middleware attached, or nil.
func sessionFrom(r *http.Request) *session {
	s, _ := r.Context().Value(sessionCtxKey{}).(*session)
	return s
}

// withSession returns a copy of the request carrying the session.
func withSession(r *http.Request, s *session) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, s))
}

// session is one browser. It remembers the passwords of pads the person unlocked so
// the UI does not have to re-send them (or store them in the browser) on every read;
// they are held in memory, never written to disk, and die with the process.
type session struct {
	mu        sync.Mutex
	passwords map[string]string // ref → password
	touched   time.Time         // last use, for eviction when the table is full
}

// newAuthState mints the URL token unless auth is disabled.
func newAuthState(noAuth bool) (*authState, error) {
	a := &authState{noAuth: noAuth, sessions: make(map[string]*session)}
	if noAuth {
		return a, nil
	}
	tok, err := randomToken()
	if err != nil {
		return nil, err
	}
	a.token = tok
	return a, nil
}

// randomToken returns 32 bytes of entropy, URL-safe.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newSession creates a session and returns it with its id.
func (a *authState) newSession() (string, *session, error) {
	id, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	sess := &session{passwords: make(map[string]string), touched: time.Now()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) >= maxSessions {
		a.evictOldestLocked()
	}
	a.sessions[id] = sess
	return id, sess, nil
}

// evictOldestLocked drops the least recently used session. Callers hold a.mu.
func (a *authState) evictOldestLocked() {
	oldestID, oldest := "", time.Time{}
	for id, s := range a.sessions {
		s.mu.Lock()
		t := s.touched
		s.mu.Unlock()
		if oldestID == "" || t.Before(oldest) {
			oldestID, oldest = id, t
		}
	}
	delete(a.sessions, oldestID)
}

// lookup returns the session behind a request, or nil. With auth disabled every
// request still gets a session (the unlock table needs somewhere to live), minted
// lazily on first use.
func (a *authState) lookup(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	sess := a.sessions[c.Value]
	a.mu.Unlock()
	if sess != nil {
		sess.mu.Lock()
		sess.touched = time.Now()
		sess.mu.Unlock()
	}
	return sess
}

// tokenMatches compares a presented URL token against the real one in constant time.
func (a *authState) tokenMatches(got string) bool {
	if a.token == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a.token), []byte(got)) == 1
}

// unlocked returns a remembered pad password ("" if the pad was never unlocked).
// A nil session is a session that remembers nothing, not a crash: these run on every
// request, and "no session" must degrade to "no unlocked pads".
func (s *session) unlocked(ref string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passwords[ref]
}

// remember stores a verified pad password for the rest of the session.
func (s *session) remember(ref, password string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.passwords[ref] = password
}

// secure wraps every response with the transport-level guards: a loopback Host check,
// a same-origin requirement for state-changing methods, and a strict CSP.
//
// The Host check is what stops DNS rebinding: a page on evil.example that resolves
// its own hostname to 127.0.0.1 reaches this port, but the browser still sends
// `Host: evil.example`, which is not loopback.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostIsLoopback(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// Browsers always attach Origin to a state-changing request, so requiring it
		// to match our own origin blocks cross-site writes without a CSRF token.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "http://"+r.Host {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		h := w.Header()
		// puredashboard compiles templates by cloning <template> nodes (no eval, no
		// new Function), so 'self' is enough for scripts; inline STYLE is needed for
		// the dynamic values components set on elements.
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; base-uri 'none'; "+
				"form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// requireSession gates a handler behind the session cookie, redeeming the one-time
// URL token when it is presented. After redemption it redirects to the same path
// WITHOUT the token, so the secret does not linger in the address bar, in history, or
// in a link the person copies out of it.
func (s *Server) requireSession(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sess := s.auth.lookup(r); sess != nil {
			next.ServeHTTP(w, withSession(r, sess))
			return
		}
		if s.auth.noAuth || s.auth.tokenMatches(r.URL.Query().Get(tokenParam)) {
			id, sess, err := s.auth.newSession()
			if err != nil {
				http.Error(w, "cannot start a session", http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookie,
				Value:    id,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			if r.URL.Query().Has(tokenParam) {
				clean := *r.URL
				q := clean.Query()
				q.Del(tokenParam)
				clean.RawQuery = q.Encode()
				http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
				return
			}
			// The request being forwarded still carries no cookie — the cookie is only
			// in the RESPONSE — so the session travels in the context instead.
			next.ServeHTTP(w, withSession(r, sess))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, "unauthorized",
				"this session is not authenticated; reopen the URL printed at startup")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(unauthorizedPage))
	}
}

// unauthorizedPage tells a person what to do instead of showing a bare 401. It is
// deliberately plain HTML with no assets — the asset routes are behind the same gate.
const unauthorizedPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Scratchpad UI — not authorized</title></head>
<body style="font-family:system-ui,sans-serif;max-width:34rem;margin:4rem auto;padding:0 1rem;line-height:1.6">
<h1>Not authorized</h1>
<p>This UI is opened through a one-time link. Open the URL printed by the
<code>ui</code> command in your terminal — it carries the token that starts the session.</p>
<p>If the server was restarted, the old link is no longer valid: use the new one.</p>
</body></html>
`

// hostIsLoopback accepts only loopback Host headers.
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
