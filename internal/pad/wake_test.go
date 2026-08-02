package pad

import "testing"

// wakePad is the five-agent shape the selectors exist for: a coordinator, two exchanges
// that belong to two other agents, and a shared task.
func wakePad() *Pad {
	return build(
		sec{"pm", "kickoff", Meta{}}, // §1 broadcast
		sec{"pm", "crash on resume", task(1, []string{"ios", "android"}, StatusOpen)}, // §2
		sec{"pm", "question for backend", Meta{To: []string{"backend"}}},              // §3
		sec{"backend", "answer", Meta{To: []string{"pm"}, Re: 3}},                     // §4
		sec{"ios", "progress", task(1, nil, StatusWIP)},                               // §5
		sec{"erp", "unrelated chatter", Meta{To: []string{"backend"}}},                // §6
	)
}

func TestWakeForMeIgnoresOtherAgentsExchanges(t *testing.T) {
	p := wakePad()
	wake, err := ParseWake([]string{"me"})
	if err != nil {
		t.Fatal(err)
	}
	woke := map[int]bool{}
	for _, s := range p.Sections {
		if p.Wakes(s, "ios", wake) {
			woke[s.N] = true
		}
	}
	// §1 is a broadcast — "the whole team" still means everyone, and it is also the
	// migration path for pads written before addressing existed.
	if !woke[1] {
		t.Error("a broadcast should still wake")
	}
	// §2 addresses ios.
	if !woke[2] {
		t.Error("a task addressed to me should wake")
	}
	// §3/§4 belong to pm and backend; §6 belongs to erp and backend. This is the whole
	// point: three agents no longer pay for a conversation belonging to two.
	for _, n := range []int{3, 4, 6} {
		if woke[n] {
			t.Errorf("§%d is not ios's business and should not wake it", n)
		}
	}
	// §5 is ios's own post.
	if woke[5] {
		t.Error("your own post is never news to you")
	}
}

func TestWakeForMeIncludesRepliesToMe(t *testing.T) {
	p := wakePad()
	wake, _ := ParseWake([]string{"me"})
	if !p.Wakes(p.Sections[3], "pm", wake) { // §4 replies to §3, which pm wrote
		t.Fatal("a reply to my section should wake me even without naming me")
	}
}

// TestWakeMineIsNotCoveredByMe is why `mine` exists: the section that OPENS a task
// carries `to`, but a co-owner's progress update usually does not repeat it — and that
// update is exactly what a co-owner needs.
func TestWakeMineIsNotCoveredByMe(t *testing.T) {
	p := wakePad()
	progress := p.Sections[4] // §5, ios reporting on T1, no `to`
	me, _ := ParseWake([]string{"me"})
	if p.Wakes(progress, "android", me) {
		t.Fatal("a bare task update should not reach `me` as if it were a broadcast")
	}
	mine, _ := ParseWake([]string{"mine"})
	if !p.Wakes(progress, "android", mine) {
		t.Fatal("a co-owner should be woken by movement on their own task")
	}
}

func TestWakeForSpecificTaskIgnoresOwnership(t *testing.T) {
	p := wakePad()
	wake, _ := ParseWake([]string{"task:1"})
	// backend owns nothing here but is blocked on T1 landing.
	if !p.Wakes(p.Sections[4], "backend", wake) {
		t.Fatal("task:<n> should follow a task whoever owns it")
	}
	if p.Wakes(p.Sections[2], "backend", wake) {
		t.Fatal("task:<n> should not wake on unrelated messages")
	}
}

func TestWakeDefaultsToTodaysBehaviour(t *testing.T) {
	p := wakePad()
	w, err := ParseWake(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Any {
		t.Fatal("no selectors must mean `any`, so existing callers are unaffected")
	}
	for _, s := range p.Sections {
		if s.Author == "erp" {
			continue
		}
		if !p.Wakes(s, "erp", w) {
			t.Fatalf("`any` should wake on §%d", s.N)
		}
	}
}

func TestParseWakeRejectsNonsense(t *testing.T) {
	if _, err := ParseWake([]string{"everything"}); !HasCode(err, CodeInvalidInput) {
		t.Fatalf("want invalid_input for an unknown selector, got %v", err)
	}
	if _, err := ParseWake([]string{"task:0"}); !HasCode(err, CodeInvalidInput) {
		t.Fatalf("want invalid_input for task:0, got %v", err)
	}
	if _, err := ParseWake([]string{"me"}); err != nil {
		t.Fatal(err)
	}
}

func TestInboxIsWhatIMissedSinceMyOwnLastPost(t *testing.T) {
	p := wakePad()
	in := p.Inbox("ios")
	if in.Since != 5 {
		t.Fatalf("ios last posted §5, got since=%d", in.Since)
	}
	// Nothing after §5 concerns ios, so the inbox is empty rather than "everything new".
	if len(in.Unread) != 0 {
		t.Fatalf("unread should hold only what concerns me: %#v", in.Unread)
	}
	if in := p.Inbox("android"); len(in.Unread) == 0 {
		t.Fatal("android has never posted and owes T1: its inbox must not be empty")
	}
}
