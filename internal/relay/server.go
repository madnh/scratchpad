package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/store"
	"github.com/madnh/scratchpad/internal/watch"
)

// coalesceInterval is how long a burst of pad changes is allowed to settle before one
// nudge is composed from all of it. Two agents answering each other produce several file
// events in quick succession; the reader only ever needs the last word about where to
// start reading.
const coalesceInterval = 400 * time.Millisecond

// renudgeInterval is the floor between two nudges about the SAME pad when the agent has
// given no sign of having acted on the first one.
//
// Without it the relay goes permanently silent on a pad in one real case: a nudge names
// two pads, the agent reads one of them and posts, and the other pad's nudge is never
// consumed. Re-nudging on a slow clock is the escape hatch — it costs one line every few
// minutes and cannot lose a pad forever.
const renudgeInterval = 5 * time.Minute

// Deliverer hands a composed nudge to wherever the agent is listening. It returns an
// error so the server can drop a registration whose destination has gone away; what
// delivery MEANS (a pty write, a test buffer) is deliberately not this package's business.
type Deliverer func(text string) error

// registration is one live agent-to-pad subscription, as the server holds it.
type registration struct {
	ref      string
	author   string
	password string
	wake     pad.Wake
	// since is the last section the agent is known to have seen. It moves only when the
	// agent itself says so by registering again — posting is what proves a read.
	since int
}

// Server is the in-process relay: a control socket agents register with, a watch on the
// store, and one Deliverer to nudge.
type Server struct {
	sock    string
	rootDir string
	ln      net.Listener
	st      *store.Store
	watcher *watch.Watcher
	deliver Deliverer

	// Logf, when set, receives operational notes (a pad that could not be read, a
	// delivery that failed). It is optional because the relay's usual host is a terminal
	// occupied by a full-screen agent UI, where printing anything corrupts the display —
	// which is why `exec --log` writes to a FILE and never to the screen.
	Logf func(format string, args ...any)

	// Command is what this relay was launched around, for `scratchpad relay` to show. A
	// machine with several agents up has several relays, and "which one is this" is
	// otherwise a pid.
	Command string

	mu sync.Mutex
	// regs is keyed by pad ref: one agent, one identity per pad.
	regs map[string]*registration
	// pending holds refs with something new that has not been nudged about yet.
	pending map[string]bool
	// nudged records when each ref was last named in a delivered nudge, and is cleared
	// the moment the agent acts on that pad. It is what stops a talkative pad from
	// re-nudging on every exchange.
	nudged map[string]time.Time

	// What has actually been delivered, for `scratchpad relay`. Without it "nothing
	// arrived" cannot be told apart from "it was delivered and the agent ignored it",
	// and those two have opposite fixes.
	nudges      int
	lastNudge   string
	lastNudgeAt time.Time

	// wake fires the coalescing timer from outside the run loop (a registration can
	// discover a backlog just as an event would).
	wake chan struct{}
}

// NewServer builds a relay bound to sock, reading the store described by live.
func NewServer(sock string, live *config.Live, deliver Deliverer) *Server {
	return &Server{
		sock:    sock,
		rootDir: live.Get().RootDir,
		st:      store.New(live),
		watcher: watch.New(live.Get().ProjectsDir),
		deliver: deliver,
		regs:    make(map[string]*registration),
		pending: make(map[string]bool),
		nudged:  make(map[string]time.Time),
		wake:    make(chan struct{}, 1),
	}
}

// Socket is the path agents should be pointed at via EnvSocket.
func (s *Server) Socket() string { return s.sock }

// maxSocketPath is the shortest `sun_path` any platform we run on gives us (104 on the
// BSDs and macOS, 108 on Linux), minus the terminating NUL.
//
// It matters because the limit is not the filesystem's: a path a store lives at perfectly
// well can still be too long to put a socket at, and the failure is `bind: invalid
// argument` — an error that says nothing about length and sends the reader looking at
// permissions.
const maxSocketPath = 103

// SocketPath picks where this process's relay should listen, given a Scratchpad dir.
//
// It belongs beside the dir like everything else the tool owns. The exception is length:
// when the dir sits deep enough that a socket cannot be bound there, the socket — and
// ONLY the socket — moves to the system temp dir. Nothing is lost by that: the socket is
// runtime, derived, and dies with the process, unlike anything else under the dir.
//
// The name is `<pid>-<random>.sock`, and the random half is not decoration. Pids are
// REUSED: a relay ended with `kill -9` leaves its socket behind, and months later the
// kernel hands that same number to something else. Binding is still safe on its own — the
// OS never gives one pid to two live processes, so any file found at this path is an
// orphan, and Listen removes it. What the entropy rules out is subtler: a grandchild that
// outlived the crashed agent still carries SCRATCHPAD_RELAY in its environment, and if a
// NEW relay had taken over that exact path, its registrations would land in a stranger's
// terminal. Unlikely, but the fix costs six characters, and "unlikely" is not a property
// that survives a year of use.
//
// The pid stays in the name because `relay` prints it and an operator matches it against
// `ps`.
func SocketPath(rootDir string) string {
	name := strconv.Itoa(os.Getpid()) + "-" + randomSuffix() + ".sock"
	if p := filepath.Join(SocketDir(rootDir), name); len(p) <= maxSocketPath {
		return p
	}
	return filepath.Join(os.TempDir(), tempPrefix+name)
}

// randomSuffix returns six hex characters. It falls back to the clock rather than failing:
// a slightly weaker name is better than refusing to start an agent, and this is a
// collision guard, not a secret.
func randomSuffix() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()%0xffffff, 16)
	}
	return hex.EncodeToString(b[:])
}

// Listen creates the control socket. It is separate from Serve so a caller can be sure
// the socket exists BEFORE it launches the agent that will register with it — otherwise
// an agent quick enough to post in its first second registers with nothing.
func (s *Server) Listen() error {
	// A stale socket from a process that died without cleaning up would otherwise make
	// every future exec fail to start. Removing it is safe: the path carries this
	// process's own pid.
	_ = os.Remove(s.sock)
	if err := os.MkdirAll(filepath.Dir(s.sock), 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Run listens (if Listen has not already) and serves until ctx is cancelled, then removes
// the socket file. It returns an error only when the socket could not be created — every
// later failure is survivable and logged, because the relay going quiet must never take
// the agent down with it.
func (s *Server) Run(ctx context.Context) error {
	if s.ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	ln := s.ln
	defer func() {
		ln.Close()
		_ = os.Remove(s.sock)
	}()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go s.accept(ctx, ln)
	go func() {
		if err := s.watcher.Run(ctx); err != nil {
			s.logf("relay: watcher stopped: %v", err)
		}
	}()

	events, unsub := s.watcher.Subscribe()
	defer unsub()

	// One timer, always set to the EARLIEST thing owed. Two very different delays share
	// it — the sub-second coalescing window and the multi-minute re-nudge floor — so it
	// has to be re-armed when a sooner deadline appears. Arming only when idle was wrong
	// in a way no single-pad test could show: a pad waiting out its five-minute floor held
	// the timer, and a brand-new nudge for a DIFFERENT pad waited behind it.
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var armed time.Time

	arm := func(d time.Duration) {
		at := time.Now().Add(d)
		if !armed.IsZero() && !at.Before(armed) {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
		armed = at
	}
	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if s.note(ev.Ref, ev.Removed) {
				arm(coalesceInterval)
			}

		case <-s.wake:
			arm(coalesceInterval)

		case <-timer.C:
			armed = time.Time{}
			if retry := s.flush(); retry > 0 {
				arm(retry)
			}
		}
	}
}

// accept serves the control socket. Each connection carries exactly one request.
func (s *Server) accept(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logf("relay: accept: %v", err)
			return
		}
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{Error: "malformed request"})
		return
	}
	var res response
	switch req.Op {
	case "register":
		if err := s.register(req.Registration); err != nil {
			res.Error = err.Error()
			s.logf("register %s as %s REFUSED: %v", req.Ref, req.Author, err)
		} else {
			res.OK = true
		}
	case "status":
		res.OK, res.Status = true, s.status()
	default:
		res.Error = "unknown op " + req.Op
	}
	_ = json.NewEncoder(conn).Encode(res)
}

// register records (or refreshes) a subscription. Refreshing is also the ONLY signal the
// relay gets that the agent is alive and has caught up on that pad, so it clears the
// pad's outstanding nudge.
func (s *Server) register(r Registration) error {
	if r.Ref == "" || r.Author == "" {
		return errors.New("registration needs a ref and an author")
	}
	if _, _, err := store.ParseRef(r.Ref); err != nil {
		return err
	}
	w, err := pad.ParseWake(r.Wake)
	if err != nil {
		return err
	}
	reg := &registration{ref: r.Ref, author: r.Author, password: r.Password, wake: w, since: r.Since}

	s.mu.Lock()
	// An agent that registers without saying where it is starts from the pad as it
	// stands: the relay's job is what happens NEXT, and replaying a pad's whole history
	// as "new activity" is a worse first impression than saying nothing.
	if reg.since <= 0 {
		if p, err := s.st.Get(reg.ref, reg.password); err == nil && len(p.Sections) > 0 {
			reg.since = p.Last().N
		}
	}
	s.regs[reg.ref] = reg
	delete(s.nudged, reg.ref)
	s.mu.Unlock()

	// Do not answer until the watcher is live. A write that lands while it is still taking
	// its first snapshot is recorded as the starting state and therefore never emitted —
	// and an agent that registers and immediately gets an answer would go on to post,
	// producing exactly that write. The wait is bounded so a relay whose watcher never
	// started still registers rather than hanging the agent's command.
	select {
	case <-s.watcher.Ready():
	case <-time.After(dialTimeout / 2):
	}

	s.logf("watching %s as %s from §%d", reg.ref, reg.author, reg.since)

	// A registration can arrive with a stale `since` — the agent posted, went away for an
	// hour, and came back. Check the pad right now rather than waiting for the next write.
	if s.note(reg.ref, false) {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// note re-reads one pad and records whether it holds anything this agent should hear
// about. It returns true when there is something new to nudge for.
func (s *Server) note(ref string, removed bool) bool {
	s.mu.Lock()
	reg, ok := s.regs[ref]
	s.mu.Unlock()
	if !ok {
		return false
	}
	if removed {
		s.forget(ref)
		return false
	}

	p, err := s.st.Get(ref, reg.password)
	if err != nil {
		// A pad that cannot be read right now (a password we were not given, a partial
		// write) is not an error worth interrupting anyone about; the next event retries.
		// It IS worth logging: a protected pad registered without its password is silent
		// forever, and silence is the one symptom every other cause shares.
		s.logf("cannot read %s: %v", ref, err)
		return false
	}

	// A pad that filled up moved the conversation elsewhere. Following it is not a
	// convenience: the successor is where the answers this agent is waiting for will
	// land, and the old pad will refuse every post from here on.
	if next := p.Header.ContinuedBy; next != "" {
		s.follow(reg, next)
		return true
	}

	for _, sec := range p.Select(pad.Selector{Since: reg.since}).Sections {
		if p.Wakes(sec, reg.author, reg.wake) {
			s.mu.Lock()
			s.pending[ref] = true
			s.mu.Unlock()
			return true
		}
	}
	return false
}

// follow moves a registration onto the pad that continued it. The successor is registered
// from its very beginning, because it opens carrying the house rules and the open tasks
// copied out of the old pad — an agent that skips them posts under rules it never read.
func (s *Server) follow(old *registration, next string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.regs, old.ref)
	delete(s.nudged, old.ref)
	delete(s.pending, old.ref)
	if _, already := s.regs[next]; !already {
		s.regs[next] = &registration{ref: next, author: old.author, password: old.password, wake: old.wake}
	}
	s.pending[next] = true
}

// forget drops a registration entirely — the pad is gone from the store.
func (s *Server) forget(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.regs, ref)
	delete(s.pending, ref)
	delete(s.nudged, ref)
}

// flush composes one nudge out of everything pending and delivers it. It returns how long
// to wait before trying again when something was held back, and 0 when nothing is owed.
func (s *Server) flush() time.Duration {
	now := time.Now()

	s.mu.Lock()
	var refs []string
	var retry time.Duration
	for ref := range s.pending {
		if _, ok := s.regs[ref]; !ok {
			delete(s.pending, ref)
			continue
		}
		if at, held := s.nudged[ref]; held {
			// Already told them about this pad and heard nothing back. Saying it again
			// costs a turn and adds no information — wait out the floor instead.
			if wait := renudgeInterval - now.Sub(at); wait > 0 {
				if retry == 0 || wait < retry {
					retry = wait
				}
				continue
			}
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	lines := make([]string, 0, len(refs))
	for _, ref := range refs {
		lines = append(lines, nudgeFor(ref, s.regs[ref].since))
	}
	s.mu.Unlock()

	if len(refs) == 0 {
		return retry
	}
	line := compose(lines)
	if err := s.deliver(line); err != nil {
		s.logf("nudge FAILED (%v): %s", err, line)
		// Leave everything pending: the destination may come back, and dropping the
		// nudge here would lose the only record that it was owed.
		return renudgeInterval
	}
	s.logf("nudge: %s", line)

	s.mu.Lock()
	for _, ref := range refs {
		delete(s.pending, ref)
		s.nudged[ref] = now
	}
	s.nudges++
	s.lastNudge, s.lastNudgeAt = line, now
	s.mu.Unlock()
	return retry
}

// status reports what this relay is watching, for `scratchpad relay`.
func (s *Server) status() *Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &Status{
		PID: os.Getpid(), Socket: s.sock, Command: s.Command, Dir: s.rootDir,
		Nudges: s.nudges, LastNudge: s.lastNudge,
	}
	if !s.lastNudgeAt.IsZero() {
		st.LastTS = s.lastNudgeAt.Unix()
	}
	for ref, reg := range s.regs {
		w := Watched{Ref: ref, Author: reg.author, Since: reg.since, Pending: s.pending[ref]}
		if at, ok := s.nudged[ref]; ok {
			w.NudgedTS = at.Unix()
		}
		st.Watching = append(st.Watching, w)
	}
	sort.Slice(st.Watching, func(i, j int) bool { return st.Watching[i].Ref < st.Watching[j].Ref })
	return st
}

// logf reports an operational note when the host gave us somewhere to put one.
func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// nudgeFor is the pointer for one pad: what to run, and from where. It never contains a
// byte written by another agent.
func nudgeFor(ref string, since int) string {
	return appinfo.Name() + " pad read " + ref + " --since " + strconv.Itoa(since)
}

// compose folds the per-pad pointers into the single line that reaches the agent.
//
// The line is a VALID command, not a sentence: `exec` types into whatever is on the other
// end of the pty, and that is not always still the agent — a crashed one leaves the shell
// it was launched from, and a shell executes what it is given. `notification` is the
// command that makes executing it a no-op (see cmd/scratchpad/notification.go).
//
// The message is single-quoted so the pointer INSIDE it stays inert: a shell that runs
// this prints the words, it does not go and read a pad into a terminal nobody is watching.
func compose(lines []string) string {
	var msg string
	if len(lines) == 1 {
		msg = "new activity — run: " + lines[0]
	} else {
		msg = "new activity on " + strconv.Itoa(len(lines)) + " pads — run: " + strings.Join(lines, " ; ")
	}
	return appinfo.Name() + " notification " + shellQuote(msg)
}

// shellQuote wraps s in single quotes, the one form in which a POSIX shell interprets
// nothing at all.
//
// Today's messages are built from a ref and a section number and could not contain a
// quote if they tried. The escaping is here anyway, because the day something else ends
// up in a nudge is the day this line stops being inert — and that failure would be
// invisible until it was executed.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
