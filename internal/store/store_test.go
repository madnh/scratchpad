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

	after, err := s.Post(pad.Ref(), "backend", "Answer", "The answer", "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Last().N != 2 || after.Last().Author != "backend" {
		t.Fatalf("bad post: %+v", after.Last())
	}
}

func TestTurnRule(t *testing.T) {
	s := testStore(t)
	pad, _, err := s.CreatePad("default", "a", "t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(pad.Ref(), "a", "again", "c", ""); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("want not_your_turn, got %v", err)
	}
	if _, err := s.Post(pad.Ref(), "b", "reply", "c", ""); err != nil {
		t.Fatalf("other author must be allowed: %v", err)
	}
	// And now a may post again, but b may not.
	if _, err := s.Post(pad.Ref(), "b", "again", "c", ""); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("want not_your_turn for b, got %v", err)
	}
	if _, err := s.Post(pad.Ref(), "a", "back", "c", ""); err != nil {
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
	if _, err := s.Post(pad.Ref(), "b", "t", "c", pw); err != nil {
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
		_, lastErr = s.Post(pad.Ref(), who, "t", "c", "")
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
	p, changed, err := s.Wait(context.Background(), pad.Ref(), "", 1, 50*time.Millisecond)
	if err != nil || changed {
		t.Fatalf("want quiet timeout, got changed=%v err=%v", changed, err)
	}
	if p.Last().N != 1 {
		t.Fatalf("timeout should still return the pad: %+v", p.Last())
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
		if _, err := s.Post(pad.Ref(), "b", "reply", "answer", ""); err != nil {
			t.Error(err)
		}
	}()
	p, changed, err = s.Wait(context.Background(), pad.Ref(), "", 1, 10*time.Second)
	wg.Wait()
	if err != nil || !changed {
		t.Fatalf("want changed=true, got changed=%v err=%v", changed, err)
	}
	if p.Last().N != 2 || p.Last().Author != "b" {
		t.Fatalf("waiter saw wrong state: %+v", p.Last())
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
			_, _ = s.Post(pad.Ref(), who, "t", "c", "") // not_your_turn errors are expected
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
