package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

// fillPad posts until the pad is one section short of its limit, and returns the ref.
func fillPad(t *testing.T, s *Store, limit int) string {
	t.Helper()
	p, _, err := create(s, "default", "opener", "opening", "content", false)
	if err != nil {
		t.Fatal(err)
	}
	authors := []string{"other", "opener"}
	for i := 0; len(sectionsOf(t, s, p.Ref())) < limit; i++ {
		if _, err := s.Post(PostRequest{
			Ref: p.Ref(), Author: authors[i%2], Title: "t", Content: "c",
		}); err != nil {
			t.Fatalf("filling: %v", err)
		}
	}
	return p.Ref()
}

func sectionsOf(t *testing.T, s *Store, ref string) []Section {
	t.Helper()
	p, err := s.Get(ref, "")
	if err != nil {
		t.Fatal(err)
	}
	return p.Sections
}

// TestFullPadContinuesInASuccessor is the feature: the post is not refused, it lands in a
// new pad, and the caller is told which one.
func TestFullPadContinuesInASuccessor(t *testing.T) {
	s := testStore(t) // 5 sections/pad, on_full defaults to continue
	old := fillPad(t, s, 5)

	res, err := s.Post(PostRequest{Ref: old, Author: "other", Title: "carry on", Content: "the next thing"})
	if err != nil {
		t.Fatalf("a full pad must continue rather than refuse: %v", err)
	}
	if res.ContinuedFrom != old {
		t.Errorf("ContinuedFrom = %q, want %q", res.ContinuedFrom, old)
	}
	if res.Pad.Ref() == old {
		t.Fatal("the post landed in the old pad")
	}
	newRef := res.Pad.Ref()

	// The post is really there, with its content, as the last section.
	moved, err := s.Get(newRef, "")
	if err != nil {
		t.Fatal(err)
	}
	last := moved.Last()
	if last.Author != "other" || !strings.Contains(last.Content, "the next thing") {
		t.Errorf("the post did not arrive intact: %+v", last)
	}
	// And the caller is told, in words, rather than having to notice the ref changed.
	var told bool
	for _, w := range res.Warnings {
		if strings.Contains(w, newRef) && strings.Contains(w, old) {
			told = true
		}
	}
	if !told {
		t.Errorf("no warning named both pads: %v", res.Warnings)
	}
}

// TestBothEndsPointAtEachOther is what makes this different from an agent quietly opening
// a second pad: whichever end a reader starts from, the other is one hop away.
func TestBothEndsPointAtEachOther(t *testing.T) {
	s := testStore(t)
	old := fillPad(t, s, 5)
	res, err := s.Post(PostRequest{Ref: old, Author: "other", Title: "carry on", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	newRef := res.Pad.Ref()

	before, err := s.Get(old, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Header.ContinuedBy != newRef {
		t.Errorf("old pad's header does not name its successor: %q", before.Header.ContinuedBy)
	}
	if got := before.Last(); got.Meta.Kind != pad.KindContinued || !strings.Contains(got.Content, newRef) {
		t.Errorf("old pad's last section should name the successor: %+v", got)
	}
	if before.Last().Author != pad.SystemAuthor {
		t.Errorf("the closing section must be the tool's, not an agent's: %q", before.Last().Author)
	}

	after, err := s.Get(newRef, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Continues() != old {
		t.Errorf("successor does not point back: %q", after.Continues())
	}
}

// TestTheOldPadIsClosedForGood: once continued, the old pad refuses posts even if the
// limit is raised afterwards. Two live ends would mean two conversations that both look
// current.
func TestTheOldPadIsClosedForGood(t *testing.T) {
	s := testStore(t)
	old := fillPad(t, s, 5)
	if _, err := s.Post(PostRequest{Ref: old, Author: "other", Title: "carry on", Content: "x"}); err != nil {
		t.Fatal(err)
	}

	raised := s.live.Get()
	raised.Limits.MaxSectionsPerPad = 500
	s.live.Set(raised)

	_, err := s.Post(PostRequest{Ref: old, Author: "opener", Title: "back here", Content: "x"})
	if !HasCode(err, CodePadContinued) {
		t.Fatalf("want pad_continued, got %v", err)
	}
	if !strings.Contains(err.Error(), "post there") {
		t.Errorf("the refusal must point at the successor: %v", err)
	}
	// Reading it is still fine — history does not close.
	if _, err := s.Get(old, ""); err != nil {
		t.Errorf("a continued pad must stay readable: %v", err)
	}
}

// TestTheSuccessorKeepsThePadsIdentity covers what an agent would otherwise have to
// re-establish by hand: who owns the pad, who may read it, and what T3 means.
func TestTheSuccessorKeepsThePadsIdentity(t *testing.T) {
	s := testStorePolicy(t, func() config.Limits {
		l := config.DefaultLimits
		l.MaxSectionsPerPad = 6
		l.MaxPadsPerProject = 10
		return l
	}(), config.DefaultRulesPolicy)

	p, password, err := s.CreatePad(CreateRequest{
		Project: "default", Author: "opener", Title: "opening", Content: "c", Protect: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	old := p.Ref()

	// House rules, set by the opener (who else the policy would refuse).
	if _, err := s.Post(PostRequest{
		Ref: old, Author: "opener", Title: "rules", Content: "- keep it short",
		Meta: Meta{Kind: pad.KindRules}, RulesDigest: "none", Password: password,
	}); err != nil {
		t.Fatal(err)
	}
	// One task that stays open, one that gets closed.
	if _, err := s.Post(PostRequest{
		Ref: old, Author: "opener", Title: "still open", Content: "do this",
		OpenTask: true, Meta: Meta{To: []string{"worker"}}, Password: password,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{
		Ref: old, Author: "opener", Title: "will close", Content: "do that",
		OpenTask: true, Meta: Meta{To: []string{"worker"}}, Password: password,
	}); err != nil {
		t.Fatal(err)
	}
	// The worker has not posted here yet, and the pad now has rules — quote them once.
	rulesNow, err := s.PadRules(old, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{
		Ref: old, Author: "worker", Title: "done", Content: "finished",
		Meta:     Meta{Kind: pad.KindTask, Task: 2, Status: pad.StatusDone},
		Password: password, AckRules: rulesNow.Digest,
	}); err != nil {
		t.Fatal(err)
	}
	// Fill the rest with conversation.
	authors := []string{"worker", "opener"}
	for i := 0; len(sectionsOf2(t, s, old, password)) < 6; i++ {
		if _, err := s.Post(PostRequest{
			Ref: old, Author: authors[i%2], Title: "t", Content: "c", Password: password,
		}); err != nil {
			t.Fatalf("filling: %v", err)
		}
	}

	// Whoever did NOT write the last section takes the next turn.
	filled, err := s.Get(old, password)
	if err != nil {
		t.Fatal(err)
	}
	nextAuthor := "worker"
	if filled.Last().Author == "worker" {
		nextAuthor = "opener"
	}
	res, err := s.Post(PostRequest{
		Ref: old, Author: nextAuthor, Title: "over the edge", Content: "x", Password: password,
	})
	if err != nil {
		t.Fatalf("continuing a protected pad: %v", err)
	}
	next, err := s.Get(res.Pad.Ref(), password)
	if err != nil {
		t.Fatalf("the successor must take the same password: %v", err)
	}

	if next.Opener() != "opener" {
		t.Errorf("owner = %q; the successor's section 1 is by whoever filled the old pad", next.Opener())
	}
	if _, err := s.Get(res.Pad.Ref(), ""); err == nil {
		t.Error("the successor lost its password")
	}
	// The house rules came along, restated by the tool rather than by an agent.
	rules, err := s.RulesOf(next)
	if err != nil {
		t.Fatal(err)
	}
	var carried bool
	for _, l := range rules.Layers {
		if l.Level == pad.LevelPad && strings.Contains(l.Text, "keep it short") {
			carried = true
			if l.Author != pad.SystemAuthor {
				t.Errorf("carried rules are authored by %q, not the tool", l.Author)
			}
		}
	}
	if !carried {
		t.Errorf("the pad's house rules did not carry over: %+v", rules.Layers)
	}

	// The open task is here and still open; the finished one is not.
	tasks := next.Tasks()
	if len(tasks) != 1 || tasks[0].Task != 1 || tasks[0].Status != pad.StatusOpen {
		t.Fatalf("open work did not carry over correctly: %+v", tasks)
	}
	if len(tasks[0].Owners) != 1 || tasks[0].Owners[0].Author != "worker" {
		t.Errorf("the carried task lost its owner: %+v", tasks[0].Owners)
	}
	// Numbering continues: the next task opened here must not be T1 again.
	if got := next.NextTaskNo(); got != 3 {
		t.Errorf("next task = T%d, want T3 — numbering restarted", got)
	}
}

func sectionsOf2(t *testing.T, s *Store, ref, password string) []Section {
	t.Helper()
	p, err := s.Get(ref, password)
	if err != nil {
		t.Fatal(err)
	}
	return p.Sections
}

// TestOnFullRejectKeepsTheOldBehaviour: a deployment that would rather stall than split.
func TestOnFullRejectKeepsTheOldBehaviour(t *testing.T) {
	s := testRejectingStore(t)
	old := fillPad(t, s, 5)

	_, err := s.Post(PostRequest{Ref: old, Author: "other", Title: "t", Content: "c"})
	if !HasCode(err, CodeLimitExceeded) {
		t.Fatalf("want limit_exceeded, got %v", err)
	}
	// The refusal should say what the operator can do about it, including the setting.
	if !strings.Contains(err.Error(), "on_full") {
		t.Errorf("the refusal should mention the alternative: %v", err)
	}
	before, err := s.Get(old, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Header.ContinuedBy != "" {
		t.Error("a refusing store still opened a successor")
	}
}

// TestAFailedPostDoesNotContinueThePad: continuation is a consequence of a post that would
// otherwise have succeeded. A post refused for any other reason must leave both the pad and
// the project exactly as they were.
func TestAFailedPostDoesNotContinueThePad(t *testing.T) {
	s := testStore(t)
	old := fillPad(t, s, 5)
	last, err := s.Get(old, "")
	if err != nil {
		t.Fatal(err)
	}
	lastAuthor := last.Last().Author

	// Same author twice in a row: not their turn, and the pad is also full.
	_, err = s.Post(PostRequest{Ref: old, Author: lastAuthor, Title: "t", Content: "c"})
	if !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("the turn rule must be checked before capacity, got %v", err)
	}
	after, err := s.Get(old, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Header.ContinuedBy != "" {
		t.Error("a refused post opened a successor")
	}
	if len(after.Sections) != len(last.Sections) {
		t.Error("a refused post changed the pad")
	}
}

// TestAWaiterOnTheOldPadIsWokenAndTold is the failure this feature must not produce: an
// agent parked on a pad that has just stopped accepting posts. It is woken regardless of
// its selectors — here the narrowest one, which nothing else in a continuation would match.
func TestAWaiterOnTheOldPadIsWokenAndTold(t *testing.T) {
	s := testStore(t)
	old := fillPad(t, s, 5)
	before, err := s.Get(old, "")
	if err != nil {
		t.Fatal(err)
	}
	since := before.Last().N

	woken := make(chan *WaitResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res, err := s.Wait(ctx, WaitRequest{
			Ref: old, Since: since, Author: "waiter",
			// The narrowest selector there is: task events on tasks this agent owns. A
			// continuation is not a task event and "waiter" owns nothing, so nothing but
			// the deliberate exemption can wake this.
			Wake: pad.Wake{Mine: true}, Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Errorf("wait: %v", err)
			close(woken)
			return
		}
		woken <- res
	}()

	// Give the waiter a moment to be waiting, then fill the pad past its limit.
	time.Sleep(100 * time.Millisecond)
	res, err := s.Post(PostRequest{Ref: old, Author: "other", Title: "carry on", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	newRef := res.Pad.Ref()

	select {
	case got, ok := <-woken:
		if !ok {
			t.Fatal("the waiter failed")
		}
		if !got.Changed {
			t.Fatal("the waiter timed out on a pad that was continued under it")
		}
		var named bool
		for _, sec := range append(append([]Section{}, got.Matched...), got.Skipped...) {
			if strings.Contains(sec.Content, newRef) || strings.Contains(sec.Title, newRef) {
				named = true
			}
		}
		if !named {
			t.Errorf("the waiter woke but was not told where to go: %+v", got.Matched)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the waiter was never woken")
	}
}
