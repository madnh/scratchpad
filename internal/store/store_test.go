package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	limits := config.DefaultLimits
	limits.MaxSectionsPerPad = 5
	limits.MaxPadsPerProject = 3
	// The permissive WRITE policy is the default for these tests, so the ones that predate
	// the policy keep testing what they were written to test. The tests that care about the
	// policy build their own store — see testStorePolicy.
	//
	// The READ gate is the opposite: it is left at the deployment default, so every test
	// here runs under the reacking a real store does. A test fixture that quietly picks the
	// laxer setting is how a default ends up untested everywhere except the one file that
	// names it.
	return testStorePolicy(t, limits, config.RulesPolicy{
		Store:            config.RulesWriteAgent,
		Project:          config.RulesWriteAgent,
		Pad:              config.RulesWriteAny,
		Reack:            config.DefaultRulesPolicy.Reack,
		NotifyActiveDays: config.DefaultRulesPolicy.NotifyActiveDays,
	})
}

// testStorePolicy is testStore with the rules policy spelled out, for the tests that are
// about the policy itself.
// testRejectingStore is testStore with limits.on_full set back to "reject". The tests that
// predate continuation are about the LIMIT — that it binds, and that raising it unblocks a
// pad — so they keep testing that rather than being rewritten around the new default.
func testRejectingStore(t *testing.T) *Store {
	t.Helper()
	limits := config.DefaultLimits
	limits.MaxSectionsPerPad = 5
	limits.MaxPadsPerProject = 3
	limits.OnFull = config.OnFullReject
	return testStorePolicy(t, limits, config.RulesPolicy{
		Store:   config.RulesWriteAgent,
		Project: config.RulesWriteAgent,
		Pad:     config.RulesWriteAny,
	})
}

func testStorePolicy(t *testing.T, limits config.Limits, rules config.RulesPolicy) *Store {
	t.Helper()
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	return New(config.NewLive(config.Config{
		RootDir: dir, ProjectsDir: projects, Limits: limits, Rules: rules,
	}))
}

// create is the old positional CreatePad, kept for the tests that predate rules and do
// not care about them. A test that DOES care builds the request itself.
func create(s *Store, project, author, title, content string, protect bool) (*Pad, string, error) {
	return s.CreatePad(CreateRequest{
		Project: project, Author: author, Title: title, Content: content, Protect: protect,
	})
}

func TestCreatePostReadRoundtrip(t *testing.T) {
	s := testStore(t)
	pad, pw, err := create(s, "projectx", "frontend", "How does API X work", "The question\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "" {
		t.Fatalf("unprotected pad returned a password %q", pw)
	}
	if pad.Project != "projectx" || len(pad.Sections) != 1 || pad.Sections[0].N != 1 {
		t.Fatalf("bad created pad: %+v", pad)
	}
	if !strings.HasPrefix(pad.Ref(), "projectx-") {
		t.Fatalf("bad ref %q", pad.Ref())
	}

	got, err := s.Get(pad.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Sections[0].Content != "The question\n" {
		t.Fatalf("content roundtrip failed: %q", got.Sections[0].Content)
	}
	if got.Sections[0].Author != "frontend" || got.Sections[0].Title != "How does API X work" {
		t.Fatalf("header roundtrip failed: %+v", got.Sections[0])
	}
	if got.Sections[0].TS == 0 {
		t.Fatal("section timestamp missing")
	}

	after, err := s.Post(PostRequest{Ref: pad.Ref(), Author: "backend", Title: "Answer", Content: "The answer", Password: ""})
	if err != nil {
		t.Fatal(err)
	}
	if after.Section != 2 || after.Pad.Last().Author != "backend" {
		t.Fatalf("bad post: %+v", after.Pad.Last())
	}
}

func TestTurnRule(t *testing.T) {
	s := testStore(t)
	pad, _, err := create(s, "default", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: pad.Ref(), Author: "a", Title: "again", Content: "c", Password: ""}); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("want not_your_turn, got %v", err)
	}
	if _, err := s.Post(PostRequest{Ref: pad.Ref(), Author: "b", Title: "reply", Content: "c", Password: ""}); err != nil {
		t.Fatalf("other author must be allowed: %v", err)
	}
	// And now a may post again, but b may not.
	if _, err := s.Post(PostRequest{Ref: pad.Ref(), Author: "b", Title: "again", Content: "c", Password: ""}); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("want not_your_turn for b, got %v", err)
	}
	if _, err := s.Post(PostRequest{Ref: pad.Ref(), Author: "a", Title: "back", Content: "c", Password: ""}); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordProtection(t *testing.T) {
	s := testStore(t)
	pad, pw, err := create(s, "default", "a", "t", "c", true)
	if err != nil {
		t.Fatal(err)
	}
	if pw == "" {
		t.Fatal("protected pad returned no password")
	}
	if !pad.Protected() {
		t.Fatal("pad not marked protected")
	}
	if _, err := s.Get(pad.Ref(), ""); !HasCode(err, CodeUnauthorized) {
		t.Fatalf("missing password: want unauthorized, got %v", err)
	}
	if _, err := s.Get(pad.Ref(), "wrong"); !HasCode(err, CodeUnauthorized) {
		t.Fatalf("wrong password: want unauthorized, got %v", err)
	}
	if _, err := s.Get(pad.Ref(), pw); err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if _, err := s.Post(PostRequest{Ref: pad.Ref(), Author: "b", Title: "t", Content: "c", Password: pw}); err != nil {
		t.Fatalf("correct password rejected on post: %v", err)
	}
	// The password must never appear in the pad file (only its bcrypt hash).
	got, err := s.Get(pad.Ref(), pw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.PasswordHash(), pw) {
		t.Fatal("plaintext password leaked into the pad header")
	}
	// Listing shows the pad without a password.
	pads, _, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(pads) != 1 || !pads[0].Protected {
		t.Fatalf("protected pad should appear in listings: %+v", pads)
	}
}

func TestValidation(t *testing.T) {
	s := testStore(t)
	cases := []struct {
		name string
		fn   func() error
		code string
	}{
		{"bad project", func() error { _, _, err := create(s, "Bad-Name", "a", "t", "c", false); return err }, CodeInvalidProjectName},
		{"empty author", func() error { _, _, err := create(s, "p1", "", "t", "c", false); return err }, CodeInvalidInput},
		{"separator in author", func() error { _, _, err := create(s, "p1", "a - b", "t", "c", false); return err }, CodeInvalidInput},
		{"empty title", func() error { _, _, err := create(s, "p1", "a", "", "c", false); return err }, CodeInvalidInput},
		{"multiline title", func() error { _, _, err := create(s, "p1", "a", "x\ny", "c", false); return err }, CodeInvalidInput},
		{"empty content", func() error { _, _, err := create(s, "p1", "a", "t", "", false); return err }, CodeInvalidInput},
		{"huge content", func() error {
			_, _, err := create(s, "p1", "a", "t", strings.Repeat("x", 65*1024), false)
			return err
		}, CodeContentTooLarge},
		{"bad ref", func() error { _, err := s.Get("not_a_ref", ""); return err }, CodeInvalidRef},
		{"missing pad", func() error { _, err := s.Get("default-zzzzzz", ""); return err }, CodePadNotFound},
	}
	for _, tc := range cases {
		if err := tc.fn(); !HasCode(err, tc.code) {
			t.Errorf("%s: want code %s, got %v", tc.name, tc.code, err)
		}
	}
}

func TestLimits(t *testing.T) {
	s := testRejectingStore(t) // 5 sections/pad, 3 pads/project
	pad, _, err := create(s, "p1", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	authors := []string{"b", "a", "b", "a", "b", "a"}
	var lastErr error
	for _, who := range authors {
		_, lastErr = s.Post(PostRequest{Ref: pad.Ref(), Author: who, Title: "t", Content: "c", Password: ""})
		if lastErr != nil {
			break
		}
	}
	if !HasCode(lastErr, CodeLimitExceeded) {
		t.Fatalf("want limit_exceeded on section overflow, got %v", lastErr)
	}

	for i := 0; i < 2; i++ {
		if _, _, err := create(s, "p1", "a", "t", "c", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := create(s, "p1", "a", "t", "c", false); !HasCode(err, CodeLimitExceeded) {
		t.Fatalf("want limit_exceeded on pad overflow, got %v", err)
	}
}

// A limit raised under a running store applies to the very next post. This is the whole
// point of reading the limits from config.Live instead of copying them in at startup: a
// pad that has hit its ceiling is unblocked by editing the marker, not by a restart.
func TestLimitsFollowTheLiveConfig(t *testing.T) {
	s := testRejectingStore(t) // 5 sections/pad
	p, _, err := create(s, "p1", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	authors := []string{"b", "a", "b", "a", "b"}
	var lastErr error
	for _, who := range authors {
		_, lastErr = s.Post(PostRequest{Ref: p.Ref(), Author: who, Title: "t", Content: "c"})
		if lastErr != nil {
			break
		}
	}
	if !HasCode(lastErr, CodeLimitExceeded) {
		t.Fatalf("want the pad full first, got %v", lastErr)
	}

	raised := s.live.Get()
	raised.Limits.MaxSectionsPerPad = 50
	s.live.Set(raised)

	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "b", Title: "t", Content: "c"}); err != nil {
		t.Fatalf("post after raising the limit: %v", err)
	}

	// And the other direction: lowering it below what the pad already holds closes it to
	// new posts rather than doing anything to what is already written.
	lowered := s.live.Get()
	lowered.Limits.MaxSectionsPerPad = 2
	s.live.Set(lowered)
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "a", Title: "t", Content: "c"}); !HasCode(err, CodeLimitExceeded) {
		t.Fatalf("want limit_exceeded after lowering the limit, got %v", err)
	}
	if got, err := s.Get(p.Ref(), ""); err != nil || len(got.Sections) != 6 {
		t.Fatalf("the transcript changed under a lowered limit: %v (err %v)", got, err)
	}
}

func TestContentWithHashLines(t *testing.T) {
	s := testStore(t)
	content := "intro\n\n# heading inside content\nmore\n# 5 - fake - but no trailing pattern match?\n"
	pad, _, err := create(s, "default", "a", "t", content, false)
	if err != nil {
		t.Fatal(err)
	}
	// "# 5 - fake - …" DOES match the section pattern — that residual risk is accepted
	// by design. "# heading inside content" must NOT split the section.
	got, err := s.Get(pad.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Sections[0].N != 1 || !strings.Contains(got.Sections[0].Content, "# heading inside content") {
		t.Fatalf("plain markdown heading corrupted parsing: %+v", got.Sections)
	}
}

func TestWait(t *testing.T) {
	s := testStore(t)
	pad, _, err := create(s, "default", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}

	// Timeout path: no new section → changed=false, no error.
	start := time.Now()
	res, err := s.Wait(context.Background(), WaitRequest{Ref: pad.Ref(), Since: 1, Timeout: 50 * time.Millisecond})
	if err != nil || res.Changed {
		t.Fatalf("want quiet timeout, got changed=%v err=%v", res.Changed, err)
	}
	if res.Pad.Last().N != 1 {
		t.Fatalf("timeout should still return the pad: %+v", res.Pad.Last())
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not honor the deadline")
	}

	// Change path: a concurrent post wakes the waiter.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(100 * time.Millisecond)
		if _, err := s.Post(PostRequest{Ref: pad.Ref(), Author: "b", Title: "reply", Content: "answer", Password: ""}); err != nil {
			t.Error(err)
		}
	}()
	res, err = s.Wait(context.Background(), WaitRequest{Ref: pad.Ref(), Since: 1, Timeout: 10 * time.Second})
	wg.Wait()
	if err != nil || !res.Changed {
		t.Fatalf("want changed=true, got changed=%v err=%v", res.Changed, err)
	}
	if res.Pad.Last().N != 2 || res.Pad.Last().Author != "b" {
		t.Fatalf("waiter saw wrong state: %+v", res.Pad.Last())
	}
}

func TestConcurrentPosts(t *testing.T) {
	s := testStore(t)
	// Plenty of room — and raised the way a running deployment raises it, through the
	// live config rather than by reaching into the store.
	roomy := s.live.Get()
	roomy.Limits = config.DefaultLimits
	s.live.Set(roomy)
	pad, _, err := create(s, "default", "seed", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	// Many goroutines race to post; flock + turn rule must keep numbering strictly
	// sequential and alternation intact.
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			who := "even"
			if i%2 == 1 {
				who = "odd"
			}
			_, _ = s.Post(PostRequest{Ref: pad.Ref(), Author: who, Title: "t", Content: "c", Password: ""}) // not_your_turn errors are expected
		}(i)
	}
	wg.Wait()
	got, err := s.Get(pad.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	for i, sec := range got.Sections {
		if sec.N != i+1 {
			t.Fatalf("non-sequential section numbering: %+v", got.Sections)
		}
		if i > 0 && sec.Author == got.Sections[i-1].Author {
			t.Fatalf("turn rule violated between %d and %d", i, i+1)
		}
	}
}

func TestAuthorsRoster(t *testing.T) {
	s := testStore(t)
	pad, _, err := create(s, "default", "frontend", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := pad.Authors(); len(got) != 1 || got[0] != "frontend" {
		t.Fatalf("fresh pad: want [frontend], got %v", got)
	}
	for _, who := range []string{"backend", "frontend", "qa"} {
		if _, err := s.Post(PostRequest{Ref: pad.Ref(), Author: who, Title: "t", Content: "c"}); err != nil {
			t.Fatal(err)
		}
	}

	// First-appearance order, each author once — frontend posted twice and stays first.
	got, err := s.Get(pad.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"frontend", "backend", "qa"}; !equalStrings(got.Authors(), want) {
		t.Fatalf("want %v, got %v", want, got.Authors())
	}

	// The roster survives the listing path, which parses without section bodies.
	m, _, err := s.Meta(pad.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"frontend", "backend", "qa"}; !equalStrings(m.Authors, want) {
		t.Fatalf("meta: want %v, got %v", want, m.Authors)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseRef(t *testing.T) {
	if p, id, err := ParseRef("projectx-abc123"); err != nil || p != "projectx" || id != "abc123" {
		t.Fatalf("got %q %q %v", p, id, err)
	}
	for _, bad := range []string{"", "noseparator", "Bad-abc", "p1-", "-abc", "p_x-abc"} {
		if _, _, err := ParseRef(bad); !HasCode(err, CodeInvalidRef) {
			t.Errorf("%q: want invalid_ref, got %v", bad, err)
		}
	}
}

func TestDeleteAndProjects(t *testing.T) {
	s := testStore(t)
	pad, _, err := create(s, "p1", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := create(s, "p2", "a", "t", "c", false); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects()
	if err != nil || len(projects) != 2 {
		t.Fatalf("want 2 projects, got %+v (%v)", projects, err)
	}
	if err := s.Delete(pad.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(pad.Ref()); !HasCode(err, CodePadNotFound) {
		t.Fatalf("double delete: want pad_not_found, got %v", err)
	}
	if _, err := s.Get(pad.Ref(), ""); !HasCode(err, CodePadNotFound) {
		t.Fatalf("deleted pad still readable: %v", err)
	}
}

// post is the shorthand these tests use for an ordinary append.
func post(t *testing.T, s *Store, ref, author, title string, meta Meta, openTask bool) *PostResult {
	t.Helper()
	res, err := s.Post(PostRequest{
		Ref: ref, Author: author, Title: title, Content: "body", Meta: meta, OpenTask: openTask,
	})
	if err != nil {
		t.Fatalf("post as %s: %v", author, err)
	}
	return res
}

// TestTaskWritePathEnforcesItsRules covers what only the WRITE path can enforce: the
// task number is allocated under the same lock as the append, ownership is checked
// before anything is written, and a task without an owner is refused.
func TestTaskWritePathEnforcesItsRules(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "default", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := p.Ref()

	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "pm", Title: "no owner", Content: "x", OpenTask: true,
	}); !HasCode(err, CodeTaskNeedsOwner) {
		t.Fatalf("a task without an owner must be refused, got %v", err)
	}

	first := post(t, s, ref, "pm", "Crash on resume", Meta{To: []string{"ios", "android"}}, true)
	if first.Task != 1 {
		t.Fatalf("first task should be T1, got T%d", first.Task)
	}
	// Opening a second task immediately: task events do not take the turn, so the same
	// author is not blocked. This is the coordinator pattern the rule exists to allow.
	second := post(t, s, ref, "pm", "Payment screen", Meta{To: []string{"ios"}}, true)
	if second.Task != 2 {
		t.Fatalf("second task should be T2, got T%d", second.Task)
	}

	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "erp", Title: "hijack", Content: "x",
		Meta: Meta{Kind: "task", Task: 1, Status: "done"},
	}); !HasCode(err, CodeNotTaskOwner) {
		t.Fatalf("a stranger must not move a task, got %v", err)
	}

	// An owner reports on their own slice; the task stays open because the other owner
	// has not.
	post(t, s, ref, "ios", "iOS done", Meta{Kind: "task", Task: 1, Status: "done"}, false)
	got, err := s.Get(ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if task, _ := got.Task(1); task.Status == "done" {
		t.Fatal("T1 must not read as done while android is outstanding")
	}
}

// TestCrossReferenceIsNotATaskEvent pins the two-layer split at the write path, which
// is the layer both surfaces build their `kind` on.
//
// A section that merely POINTS at a task is a message: it takes the turn like any other
// remark, anyone may write it, and it is not the owner's answer. Collapsing the two
// would quietly undo three rules at once — a question would stop taking the turn, a
// stranger could write into a task's record, and an idle remark would clear the debt
// that `--unacked` and `pad who` report.
func TestCrossReferenceIsNotATaskEvent(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "default", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := p.Ref()
	post(t, s, ref, "pm", "Crash on resume", Meta{To: []string{"ios"}}, true)

	// A stranger may ask about T1 — the ownership rule governs the record, not the
	// conversation around it.
	res := post(t, s, ref, "backend", "Anything I can help with?", Meta{Task: 1, To: []string{"ios"}}, false)
	if last := res.Pad.Last(); last.IsTask() {
		t.Fatalf("a bare task reference must stay a message: %#v", last.Meta)
	}

	// It is conversation, so it holds the turn.
	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "backend", Title: "again", Content: "x", Meta: Meta{Task: 1},
	}); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("a cross-reference must take the turn, got %v", err)
	}

	// And it is not the owner's answer: T1 is still owed by ios.
	post(t, s, ref, "ios", "still reading", Meta{Task: 1}, false)
	got, err := s.Get(ref, "")
	if err != nil {
		t.Fatal(err)
	}
	owed := got.Owed()
	if len(owed) != 1 || owed[0].To != "ios" || owed[0].Task != 1 {
		t.Fatalf("ios still owes an event on T1, got %#v", owed)
	}

	// A number this pad never opened is a typo, whichever layer it is used on.
	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "pm", Title: "typo", Content: "x", Meta: Meta{Task: 99},
	}); !HasCode(err, CodeNoSuchTask) {
		t.Fatalf("a dangling task reference must be refused, got %v", err)
	}
}

// TestReplyImpliesAddressingAndValidates pins two write-path behaviours: replying to a
// section addresses its author without the caller repeating it, and a dangling `re` is
// refused rather than silently stored.
func TestReplyImpliesAddressingAndValidates(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "default", "frontend", "question", "how?", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := p.Ref()

	res := post(t, s, ref, "backend", "answer", Meta{Re: 1}, false)
	last := res.Pad.Last()
	if !last.AddressedTo("frontend") {
		t.Fatalf("a reply should address the parent's author: %#v", last.Meta)
	}

	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "frontend", Title: "t", Content: "x", Meta: Meta{Re: 99},
	}); !HasCode(err, CodeInvalidInput) {
		t.Fatalf("a dangling re must be refused, got %v", err)
	}
}

// TestWaitWakesSelectivelyButCatchesUpFully is the promise the selectors depend on:
// filtering decides what INTERRUPTS you, never what you are told about.
func TestWaitWakesSelectivelyButCatchesUpFully(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "default", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := p.Ref()
	post(t, s, ref, "backend", "chat with erp", Meta{To: []string{"erp"}}, false) // §2
	post(t, s, ref, "erp", "reply to backend", Meta{To: []string{"backend"}}, false)
	post(t, s, ref, "pm", "over to you", Meta{To: []string{"ios"}}, false) // §4

	res, err := s.Wait(context.Background(), WaitRequest{
		Ref: ref, Since: 1, Author: "ios",
		Wake: Wake{Me: true}, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || len(res.Matched) != 1 || res.Matched[0].N != 4 {
		t.Fatalf("ios should be woken by §4 alone: %#v", res.Matched)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("the two sections it slept through must still be reported: %#v", res.Skipped)
	}
	for _, sec := range res.Skipped {
		if sec.Content != "" {
			t.Error("skipped sections are a table of contents, not bodies")
		}
	}
}

// TestWaitReturnsOnUnacked is what stops a wait from hanging forever on an agent that
// was never listening.
func TestWaitReturnsOnUnacked(t *testing.T) {
	s := testStore(t)
	// Opened by someone else, so pm is free to post the message that goes unanswered.
	p, _, err := create(s, "default", "ios", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := p.Ref()
	post(t, s, ref, "pm", "please look", Meta{To: []string{"android"}}, false)

	// Unacked: 1ns — the assignment is already older than that, so this returns at once
	// rather than after the (never arriving) reply.
	res, err := s.Wait(context.Background(), WaitRequest{
		Ref: ref, Since: 2, Author: "pm",
		Wake: Wake{Me: true}, Timeout: 2 * time.Second, Unacked: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Reason != "unacked" {
		t.Fatalf("want an unacked wake, got changed=%v reason=%q", res.Changed, res.Reason)
	}
	if len(res.Unacked) != 1 || res.Unacked[0].To != "android" {
		t.Fatalf("want the android assignment named: %#v", res.Unacked)
	}
}

// TestPostWarnsAboutASilentAddressee is the immediacy that presence was wanted for: the
// sender is told at the moment they can still act, and the post still succeeds.
func TestPostWarnsAboutASilentAddressee(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "default", "ios", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	res := post(t, s, p.Ref(), "pm", "over to you", Meta{To: []string{"nobody"}}, false)
	if len(res.Warnings) != 1 {
		t.Fatalf("want a warning about an addressee never seen here, got %#v", res.Warnings)
	}
	if res.Section != 2 {
		t.Fatal("a warning must not stop the post from succeeding")
	}
}

// A pad that was valid when written stays readable however far the operator lowers the
// limits afterwards. This is the whole point of the read ceiling not following policy:
// limits govern WRITES, and an append-only store must never retroactively withdraw reads.
func TestLoweringLimitsKeepsExistingPadsReadable(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "p1", "a", "t", strings.Repeat("x", 3000), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "b", Title: "second", Content: strings.Repeat("y", 3000),
	}); err != nil {
		t.Fatal(err)
	}

	// The exact configuration that used to make this pad unreadable.
	lowered := s.live.Get()
	lowered.Limits = config.Limits{MaxTitleKB: 1, MaxContentKB: 1, MaxSectionsPerPad: 1}
	s.live.Set(lowered)

	got, err := s.Get(p.Ref(), "")
	if err != nil {
		t.Fatalf("a pad written under the old limits became unreadable: %v", err)
	}
	if len(got.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(got.Sections))
	}
	// WRITES still take the new limit — this is the half that must not change.
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "a", Title: "third", Content: strings.Repeat("z", 3000),
	}); !HasCode(err, CodeContentTooLarge) {
		t.Fatalf("a write over the new content limit: want content_too_large, got %v", err)
	}
}

// The sequence this tool actually recommends: an agent hits a bound, the operator raises
// the limit, the pad grows, the limit goes back. A ceiling derived from the DEFAULT limits
// would pass the test above and still fail this one.
func TestRaiseGrowLowerKeepsPadsReadable(t *testing.T) {
	s := testStore(t)

	raised := s.live.Get()
	raised.Limits = config.Limits{MaxTitleKB: 4, MaxContentKB: 512, MaxSectionsPerPad: 50, MaxPadsPerProject: 10}
	s.live.Set(raised)

	big := strings.Repeat("x", 400*1024) // legal under the raised limit, not under the default
	p, _, err := create(s, "p1", "a", "t", big, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "b", Title: "second", Content: big}); err != nil {
		t.Fatal(err)
	}

	back := s.live.Get()
	back.Limits = config.DefaultLimits
	s.live.Set(back)

	if _, err := s.Get(p.Ref(), ""); err != nil {
		t.Fatalf("a pad grown under a raised limit became unreadable when it was lowered: %v", err)
	}
}

// An unreadable pad keeps its row in List, carrying the reason. It used to be dropped,
// which made a pad the store still holds indistinguishable from a deleted one.
func TestListKeepsUnreadablePads(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "p1", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing policy can do makes a pad unreadable now, so corrupt the file instead —
	// the other reason a row used to vanish.
	path := s.padPath("p1", strings.TrimPrefix(p.Ref(), "p1-"))
	if err := os.WriteFile(path, []byte("this is not a pad header\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pads, warnings, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	var row *PadMeta
	for i := range pads {
		if pads[i].Ref == p.Ref() {
			row = &pads[i]
		}
	}
	if row == nil {
		t.Fatal("the unreadable pad lost its row in List")
	}
	if row.Unreadable == "" {
		t.Fatal("the row does not carry a reason")
	}
}

// maxPadBytes takes the LARGER of policy and the fixed floor, in both directions.
func TestMaxPadBytesNeverDropsBelowTheFloor(t *testing.T) {
	s := testStore(t)

	tiny := config.Limits{MaxTitleKB: 1, MaxContentKB: 1, MaxSectionsPerPad: 1}
	if got := s.maxPadBytes(tiny); got != maxReadablePadBytes {
		t.Errorf("tiny limits: ceiling = %d, want the floor %d", got, maxReadablePadBytes)
	}

	huge := config.Limits{MaxTitleKB: 4, MaxContentKB: 1024, MaxSectionsPerPad: 10000}
	if got := s.maxPadBytes(huge); got <= maxReadablePadBytes {
		t.Errorf("huge limits: ceiling = %d, want policy to raise it above %d", got, maxReadablePadBytes)
	}
}

// TestOwnerCannotReassignByAddressing covers the write-time half of the ownership table.
// `to` on a task event is the OWNER SET, not addressing, and the fold takes the latest one
// written by anybody. So an owner reporting progress while naming a colleague used to hand
// the task away — and because the fold publishes states only for CURRENT owners, its own
// report went out with it. The task then read as unfinished work owned by an agent that had
// never touched it, with nothing in the transcript saying so.
func TestOwnerCannotReassignByAddressing(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "p", "pm", "seed", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "pm", Title: "Crash on resume", Content: "c",
		OpenTask: true, Meta: Meta{To: []string{"ios"}},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = s.Post(PostRequest{
		Ref: p.Ref(), Author: "ios", Title: "ios: done, telling android", Content: "c",
		Meta: Meta{Kind: pad.KindTask, Task: 1, Status: pad.StatusDone, To: []string{"android"}},
	})
	if !HasCode(err, CodeNotTaskOwner) {
		t.Fatalf("an owner reassigned the task by addressing someone: %v", err)
	}
	if !strings.Contains(err.Error(), "pm") {
		t.Errorf("the refusal must name who may reassign: %v", err)
	}

	// The report itself, without the reassignment, must still land — the fix removes a
	// right the owner never had, not the one it does.
	res, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "ios", Title: "ios: done", Content: "c",
		Meta: Meta{Kind: pad.KindTask, Task: 1, Status: pad.StatusDone},
	})
	if err != nil {
		t.Fatalf("an owner must still be able to report its own status: %v", err)
	}
	board, ok := res.Pad.Task(1)
	if !ok || board.Status != "done" {
		t.Fatalf("the owner's done must reach the board, got %+v", board)
	}
	if len(board.Owners) != 1 || board.Owners[0].Author != "ios" {
		t.Errorf("the task must still belong to ios, got %+v", board.Owners)
	}
}

// TestReplyDoesNotReassignATask closes the second door to the same defect, and the worse
// one: `re` implies `to` so a reply addresses the author it answers without repeating them.
// On a task event that convenience wrote an owner set the author never typed, so answering
// a question about your work silently handed the work to whoever asked. There is no `--to`
// in the command to notice.
func TestReplyDoesNotReassignATask(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "p", "pm", "seed", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "pm", Title: "Crash on resume", Content: "c",
		OpenTask: true, Meta: Meta{To: []string{"ios"}},
	}); err != nil {
		t.Fatal(err)
	}
	// A bystander asks about it, so the section being answered belongs to neither the
	// opener nor the owner.
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "erp", Title: "how is it going", Content: "c",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "ios", Title: "ios: still on it", Content: "c",
		Meta: Meta{Kind: pad.KindTask, Task: 1, Status: pad.StatusWIP, Re: 3},
	})
	if err != nil {
		t.Fatalf("answering a question about your own task must be allowed: %v", err)
	}
	board, ok := res.Pad.Task(1)
	if !ok {
		t.Fatal("task vanished")
	}
	if len(board.Owners) != 1 || board.Owners[0].Author != "ios" {
		t.Fatalf("replying reassigned the task, owners are now %+v", board.Owners)
	}
	if board.Owners[0].Status != "wip" {
		t.Errorf("the owner's own report was dropped, got %q", board.Owners[0].Status)
	}
	// `re` still records what was answered; it just no longer hands over the work.
	last := res.Pad.Last()
	if last.Re != 3 {
		t.Errorf("re should survive on a task event, got %d", last.Re)
	}
	if len(last.To) != 0 {
		t.Errorf("re must not inject an owner set on a task event, got %v", last.To)
	}
}
