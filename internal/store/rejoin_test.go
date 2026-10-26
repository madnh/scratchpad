package store

import (
	"testing"
	"time"
)

func rejoinRefs(plan RejoinPlan) map[string]int {
	out := map[string]int{}
	for _, p := range plan.Pads {
		out[p.Ref] = p.Since
	}
	return out
}

// The pads an identity takes part in are the ones it posted to and the ones holding an open
// task it owns — each watched from its OWN last post, so what arrived while it was down is
// still ahead of the mark. A pad it merely appears in as a stranger, and a protected pad
// nobody gave it the password for, are not.
func TestRejoinableFindsWhatTheAuthorTakesPartIn(t *testing.T) {
	s := testStore(t)

	talked, _, err := create(s, "alpha", "ping", "hello", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	// ping's last post is §1; §2 is addressed to it and still unanswered.
	if _, err := s.Post(PostRequest{Ref: talked.Ref(), Author: "allocator", Title: "for you", Content: "y", Meta: Meta{To: []string{"ping"}}}); err != nil {
		t.Fatal(err)
	}

	assigned, _, err := create(s, "beta", "allocator", "work", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: assigned.Ref(), Author: "allocator", Title: "do it", Content: "z", OpenTask: true, Meta: Meta{To: []string{"ping"}}}); err != nil {
		t.Fatal(err)
	}

	stranger, _, err := create(s, "gamma", "pong", "not ping's", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := create(s, "delta", "ping", "secret", "x", true); err != nil {
		t.Fatal(err)
	}

	plan, err := s.Rejoinable("ping", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := rejoinRefs(plan)
	if since, ok := got[talked.Ref()]; !ok || since != 1 {
		t.Errorf("posted-to pad: got since=%d present=%v, want §1 (ping's own last post)", since, ok)
	}
	if since, ok := got[assigned.Ref()]; !ok || since != 0 {
		t.Errorf("assigned pad: got since=%d present=%v, want 0 — ping never posted there", since, ok)
	}
	if _, ok := got[stranger.Ref()]; ok {
		t.Error("a pad ping has no part in was rejoined")
	}
	if len(got) != 2 || plan.Protected != 1 {
		t.Errorf("plan = %+v, want 2 pads and 1 protected left out", plan)
	}
}

// A pad whose last activity is older than the window is left out and COUNTED, so a short
// list cannot be read as "that is everything".
func TestRejoinableLeavesOutIdlePads(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "alpha", "ping", "hello", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.Rejoinable("ping", time.Hour, time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pads) != 0 || plan.Idle != 1 {
		t.Fatalf("plan = %+v, want %s left out as idle", plan, p.Ref())
	}
}

// A continued pad is replaced by its live end: the old one refuses every post, and the
// successor is where the answer will land. ping never posted in the successor, so it is
// watched from the start — it opens with the rules and tasks ping has not seen.
func TestRejoinableFollowsAContinuedPadToItsLiveEnd(t *testing.T) {
	s := testStore(t) // 5 sections per pad
	p, _, err := create(s, "default", "ping", "opening", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	authors := []string{"pong", "ping"}
	for i := 0; len(sectionsOf(t, s, p.Ref())) < 5; i++ {
		if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: authors[i%2], Title: "t", Content: "c"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Post(PostRequest{Ref: p.Ref(), Author: "pong", Title: "overflow", Content: "c"})
	if err != nil {
		t.Fatal(err)
	}
	next := res.Pad.Ref()
	if next == p.Ref() {
		t.Fatal("the pad did not continue")
	}

	plan, err := s.Rejoinable("ping", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := rejoinRefs(plan)
	if _, ok := got[p.Ref()]; ok {
		t.Error("the continued pad was rejoined; it refuses posts")
	}
	if since, ok := got[next]; !ok || since != 0 {
		t.Errorf("successor: since=%d present=%v, want watched from the start", since, ok)
	}
}
