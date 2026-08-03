package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/config"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	limits := config.DefaultLimits
	limits.MaxSectionsPerPad = 5
	limits.MaxPadsPerProject = 3
	return New(t.TempDir(), limits)
}

func TestCreatePostReadRoundtrip(t *testing.T) {
	s := testStore(t)
	pad, pw, err := s.CreatePad("projectx", "frontend", "How does API X work", "The question\n", false)
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
	pad, _, err := s.CreatePad("default", "a", "t", "c", false)
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
	pad, pw, err := s.CreatePad("default", "a", "t", "c", true)
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
	if strings.Contains(got.PasswordHash, pw) {
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
		{"bad project", func() error { _, _, err := s.CreatePad("Bad-Name", "a", "t", "c", false); return err }, CodeInvalidProjectName},
		{"empty author", func() error { _, _, err := s.CreatePad("p1", "", "t", "c", false); return err }, CodeInvalidInput},
		{"separator in author", func() error { _, _, err := s.CreatePad("p1", "a - b", "t", "c", false); return err }, CodeInvalidInput},
		{"empty title", func() error { _, _, err := s.CreatePad("p1", "a", "", "c", false); return err }, CodeInvalidInput},
		{"multiline title", func() error { _, _, err := s.CreatePad("p1", "a", "x\ny", "c", false); return err }, CodeInvalidInput},
		{"empty content", func() error { _, _, err := s.CreatePad("p1", "a", "t", "", false); return err }, CodeInvalidInput},
		{"huge content", func() error {
			_, _, err := s.CreatePad("p1", "a", "t", strings.Repeat("x", 65*1024), false)
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
	s := testStore(t) // 5 sections/pad, 3 pads/project
	pad, _, err := s.CreatePad("p1", "a", "t", "c", false)
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
		if _, _, err := s.CreatePad("p1", "a", "t", "c", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreatePad("p1", "a", "t", "c", false); !HasCode(err, CodeLimitExceeded) {
		t.Fatalf("want limit_exceeded on pad overflow, got %v", err)
	}
}

func TestContentWithHashLines(t *testing.T) {
	s := testStore(t)
	content := "intro\n\n# heading inside content\nmore\n# 5 - fake - but no trailing pattern match?\n"
	pad, _, err := s.CreatePad("default", "a", "t", content, false)
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
	pad, _, err := s.CreatePad("default", "a", "t", "c", false)
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
	limits := config.DefaultLimits
	s.limits = limits // plenty of room
	pad, _, err := s.CreatePad("default", "seed", "t", "c", false)
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
	pad, _, err := s.CreatePad("p1", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreatePad("p2", "a", "t", "c", false); err != nil {
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
	p, _, err := s.CreatePad("default", "pm", "kickoff", "starting", false)
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
	p, _, err := s.CreatePad("default", "pm", "kickoff", "starting", false)
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
	p, _, err := s.CreatePad("default", "frontend", "question", "how?", false)
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
	p, _, err := s.CreatePad("default", "pm", "kickoff", "starting", false)
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
	p, _, err := s.CreatePad("default", "ios", "kickoff", "starting", false)
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
	p, _, err := s.CreatePad("default", "ios", "kickoff", "starting", false)
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
