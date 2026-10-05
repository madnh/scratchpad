package relay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/store"
)

// collector is the test's Deliverer: it records nudges and lets a test block until one
// arrives, so nothing here sleeps for a fixed duration hoping the relay kept up.
type collector struct {
	mu   sync.Mutex
	got  []string
	fire chan struct{}
}

func newCollector() *collector { return &collector{fire: make(chan struct{}, 16)} }

func (c *collector) deliver(line string) error {
	c.mu.Lock()
	c.got = append(c.got, line)
	c.mu.Unlock()
	select {
	case c.fire <- struct{}{}:
	default:
	}
	return nil
}

func (c *collector) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.got...)
}

// next waits for one more nudge than the test has already seen.
func (c *collector) next(t *testing.T) string {
	t.Helper()
	select {
	case <-c.fire:
	case <-time.After(5 * time.Second):
		t.Fatal("no nudge arrived")
	}
	got := c.all()
	return got[len(got)-1]
}

// quiet asserts that nothing is delivered within a window. It is a negative test, so the
// window has to be long enough to be meaningful and short enough to keep the suite quick.
func (c *collector) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-c.fire:
		t.Fatalf("expected no nudge, got %v", c.all())
	case <-time.After(d):
	}
}

// testRelay builds a store and a relay over the same temp dir, and starts the relay.
func testRelay(t *testing.T) (*store.Store, *Server, *collector) {
	t.Helper()
	// NOT t.TempDir(): it names the directory after the test, and a unix socket path is
	// capped at ~104 bytes — a long test name pushes the socket over it and the failure
	// reads as `bind: invalid argument`, which says nothing about length.
	dir, err := os.MkdirTemp("", "rly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	projects := filepath.Join(dir, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(config.Config{
		RootDir: dir, ProjectsDir: projects,
		Limits: config.DefaultLimits, Rules: config.RulesPolicy{
			Store: config.RulesWriteAgent, Project: config.RulesWriteAgent, Pad: config.RulesWriteAny,
		},
	})
	c := newCollector()
	srv := NewServer(filepath.Join(dir, "relay.sock"), live, c.deliver)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()
	return store.New(live), srv, c
}

func mustRegister(t *testing.T, srv *Server, ref, author string, since int) {
	t.Helper()
	mustRegisterWake(t, srv, ref, author, since, []string{"me"})
}

func mustRegisterWake(t *testing.T, srv *Server, ref, author string, since int, wake []string) {
	t.Helper()
	if err := Register(srv.Socket(), Registration{
		Ref: ref, Author: author, Since: since, Wake: wake,
	}); err != nil {
		t.Fatal(err)
	}
}

func post(t *testing.T, s *store.Store, ref, author, title, body string) {
	t.Helper()
	if _, err := s.Post(store.PostRequest{
		Ref: ref, Author: author, Title: title, Content: body,
	}); err != nil {
		t.Fatal(err)
	}
}

// A pad the agent takes part in moves, and the agent is handed a POINTER to it — the ref
// and the section it should read from, and nothing that the other agent wrote.
func TestNudgeIsAPointerNotContent(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, srv, p.Ref(), "backend", 1)

	post(t, s, p.Ref(), "frontend", "a question", "SECRET-CONTENT-MUST-NOT-APPEAR\n")

	line := c.next(t)
	if !strings.Contains(line, "pad read "+p.Ref()+" --since 1") {
		t.Fatalf("nudge does not point at the pad: %q", line)
	}
	if strings.Contains(line, "SECRET-CONTENT-MUST-NOT-APPEAR") {
		t.Fatalf("nudge carried another agent's words: %q", line)
	}
}

// The nudge has to survive being executed. `exec` types into whatever is on the other end
// of the pty, and a crashed agent leaves the shell it was launched from — which runs what
// it is given. So the line must be a command that DOES nothing, not a sentence that
// happens to fail parsing (zsh rejects `[scratchpad]` as an empty glob; bash says
// `command not found`; neither is a guarantee).
func TestNudgeIsAHarmlessCommandLine(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, srv, p.Ref(), "backend", 1)
	post(t, s, p.Ref(), "frontend", "q", "x\n")
	line := c.next(t)

	// The binary names itself — under `go test` that is the test binary, which is the
	// point: the nudge tells the receiver to run the tool it was actually delivered by.
	prefix := appinfo.Name() + " notification "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("nudge is not a notification command: %q", line)
	}
	// Exactly one argument, quoted: everything after the subcommand is a single inert
	// string, so the pointer inside it cannot run and print a pad into a dead terminal.
	arg := strings.TrimPrefix(line, prefix)
	if !strings.HasPrefix(arg, "'") || !strings.HasSuffix(arg, "'") {
		t.Fatalf("message is not single-quoted: %q", arg)
	}
	if strings.Contains(strings.Trim(arg, "'"), "'") {
		t.Fatalf("message contains an unescaped quote, which would end the string: %q", arg)
	}
	// Nothing a shell expands, substitutes or redirects may reach it unquoted.
	for _, bad := range []string{"$(", "`", "&&", "||", ";", ">", "|"} {
		if strings.Contains(line[:strings.Index(line, "'")], bad) {
			t.Fatalf("shell metacharacter %q outside the quoted message: %q", bad, line)
		}
	}
}

// Several new sections on one pad are one nudge, not one each: every nudge says the same
// thing, so a later one subsumes the earlier ones.
func TestManySectionsCoalesceIntoOneNudge(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, srv, p.Ref(), "backend", 1)

	// Two other agents talking to each other, back to back — the turn rule forbids the
	// same author twice in a row, which is also what a real burst looks like.
	post(t, s, p.Ref(), "frontend", "one", "a\n")
	post(t, s, p.Ref(), "ops", "two", "b\n")

	line := c.next(t)
	if strings.Count(line, "pad read") != 1 {
		t.Fatalf("expected a single pointer, got %q", line)
	}
	// And the pointer starts where the agent actually left off, not at the newest
	// section: an agent that follows only the latest nudge must not skip what it never read.
	if !strings.Contains(line, "--since 1") {
		t.Fatalf("pointer moved past unread sections: %q", line)
	}
	c.quiet(t, 700*time.Millisecond)
}

// Two pads moving at once are one line, not two.
func TestSeveralPadsAreOneNudge(t *testing.T) {
	s, srv, c := testRelay(t)
	var refs []string
	for _, title := range []string{"first", "second"} {
		p, _, err := s.CreatePad(store.CreateRequest{
			Project: "proj", Author: "backend", Title: title, Content: "hello\n",
		})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, p.Ref())
		mustRegister(t, srv, p.Ref(), "backend", 1)
	}
	for _, ref := range refs {
		post(t, s, ref, "frontend", "ping", "x\n")
	}

	line := c.next(t)
	for _, ref := range refs {
		if !strings.Contains(line, ref) {
			t.Fatalf("nudge %q is missing pad %s", line, ref)
		}
	}
	if strings.Count(line, "pad read") != 2 {
		t.Fatalf("expected two pointers on one line: %q", line)
	}
}

// The agent's own post is not news to it. Without this an always-on relay nudges an agent
// about the very section it just wrote.
func TestOwnPostDoesNotNudge(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, srv, p.Ref(), "backend", 1)

	post(t, s, p.Ref(), "frontend", "q", "x\n")
	c.next(t) // frontend's post does nudge

	// backend answers, and re-registers the way `pad post` does.
	post(t, s, p.Ref(), "backend", "a", "y\n")
	mustRegister(t, srv, p.Ref(), "backend", 3)
	c.quiet(t, 700*time.Millisecond)
}

// A relay registration follows both sides of task work without subscribing to the whole
// task stream. The opener needs the owner's unaddressed progress event; task traffic that
// another coordinator opened remains unrelated and must stay quiet.
func TestTaskOpenerIsNudgedOnlyForTasksItOpened(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "pm", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterWake(t, srv, p.Ref(), "pm", 1, []string{"me", "mine", "opened"})

	opened, err := s.Post(store.PostRequest{
		Ref: p.Ref(), Author: "pm", Title: "ios work", Content: "assigned\n",
		Meta:     pad.Meta{Kind: pad.KindTask, To: []string{"ios"}, Status: pad.StatusOpen},
		OpenTask: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.quiet(t, 300*time.Millisecond) // the opener's own task-open event is not news

	if _, err := s.Post(store.PostRequest{
		Ref: p.Ref(), Author: "ios", Title: "progress", Content: "working\n",
		Meta: pad.Meta{Kind: pad.KindTask, Task: opened.Task, Status: pad.StatusWIP},
	}); err != nil {
		t.Fatal(err)
	}
	if line := c.next(t); !strings.Contains(line, "--since 1") {
		t.Fatalf("opener nudge does not point at its unread task events: %q", line)
	}

	mustRegisterWake(t, srv, p.Ref(), "pm", 3, []string{"me", "mine", "opened"})
	unrelated, err := s.Post(store.PostRequest{
		Ref: p.Ref(), Author: "ops", Title: "android work", Content: "assigned\n",
		Meta:     pad.Meta{Kind: pad.KindTask, To: []string{"android"}, Status: pad.StatusOpen},
		OpenTask: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(store.PostRequest{
		Ref: p.Ref(), Author: "android", Title: "progress", Content: "working\n",
		Meta: pad.Meta{Kind: pad.KindTask, Task: unrelated.Task, Status: pad.StatusWIP},
	}); err != nil {
		t.Fatal(err)
	}
	c.quiet(t, 700*time.Millisecond)
}

// A pad already nudged about is not nudged again while the agent has given no sign of
// having acted on the first one — repeating "you have mail" costs a turn and adds nothing.
// Registering again IS that sign, and re-opens the pad for nudges.
func TestNoSecondNudgeUntilTheAgentActs(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, srv, p.Ref(), "backend", 1)

	post(t, s, p.Ref(), "frontend", "q1", "x\n")
	c.next(t)

	// More traffic while the agent is still busy: no second nudge.
	post(t, s, p.Ref(), "ops", "q2", "y\n")
	c.quiet(t, 700*time.Millisecond)

	// The agent surfaces, catches up and posts — which re-registers it. New traffic is
	// nudged for again.
	post(t, s, p.Ref(), "backend", "a", "z\n")
	mustRegister(t, srv, p.Ref(), "backend", 4)
	post(t, s, p.Ref(), "frontend", "q3", "w\n")
	line := c.next(t)
	if !strings.Contains(line, "--since 4") {
		t.Fatalf("pointer did not follow the agent's own progress: %q", line)
	}
}

// Registering with a `since` that is already behind must nudge immediately: an agent that
// went away for an hour and came back should not have to wait for the next write.
func TestRegistrationFindsABacklog(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	post(t, s, p.Ref(), "frontend", "q", "x\n")

	mustRegister(t, srv, p.Ref(), "backend", 1)
	if line := c.next(t); !strings.Contains(line, p.Ref()) {
		t.Fatalf("backlog not reported: %q", line)
	}
}

// Registering with no mark at all means "from here on": the relay's job is what happens
// next, and replaying a pad's whole history as new activity is a worse first impression
// than saying nothing.
func TestRegistrationWithoutAMarkStartsFromNow(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	post(t, s, p.Ref(), "frontend", "old", "x\n")

	mustRegister(t, srv, p.Ref(), "backend", 0)
	c.quiet(t, 700*time.Millisecond)

	post(t, s, p.Ref(), "ops", "new", "y\n")
	if line := c.next(t); !strings.Contains(line, "--since 2") {
		t.Fatalf("mark was not taken from the pad as it stood: %q", line)
	}
}

// Looking at a pad is not reading it. `pad get --as` registers on every call with the
// agent's last POST as the mark, so an agent that follows a nudge, checks its inbox and
// posts nothing comes back at the very mark it was nudged about. That must not re-nudge:
// it was the loop every agent under exec fell into — nudge, look, nudge, look — with the
// same `--since` each time. Only a mark that has ADVANCED clears the floor.
func TestReregisterAtSameMarkDoesNotRenudge(t *testing.T) {
	s, srv, c := testRelay(t)
	p, _, err := s.CreatePad(store.CreateRequest{
		Project: "proj", Author: "backend", Title: "start", Content: "hello\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, srv, p.Ref(), "backend", 1)
	post(t, s, p.Ref(), "frontend", "a question", "x\n")
	if line := c.next(t); !strings.Contains(line, "--since 1") {
		t.Fatalf("first nudge: %q", line)
	}

	// The agent looks at the pad (pad get --as backend) — same mark, nothing posted.
	mustRegister(t, srv, p.Ref(), "backend", 1)
	mustRegister(t, srv, p.Ref(), "backend", 1)
	c.quiet(t, 1500*time.Millisecond)

	// Registration at the same mark must still not lose the pad: fresh traffic after the
	// floor is the re-nudge's job, and a mark that advances clears it at once.
	post(t, s, p.Ref(), "backend", "an answer", "y\n")
	mustRegister(t, srv, p.Ref(), "backend", 3)
	post(t, s, p.Ref(), "frontend", "thanks", "z\n")
	if line := c.next(t); !strings.Contains(line, "--since 3") {
		t.Fatalf("nudge after the mark advanced: %q", line)
	}
}
