package pad

import (
	"testing"
	"time"
)

// build assembles a pad from compact section specs, so a test reads as the conversation
// it is describing rather than as a wall of struct literals.
type sec struct {
	author string
	title  string
	meta   Meta
}

func build(sections ...sec) *Pad {
	p := &Pad{Project: "p", ID: "id", CreatedTS: 1000}
	for i, s := range sections {
		m := s.meta
		if m.Kind == "" {
			m.Kind = KindMessage
		}
		p.Sections = append(p.Sections, Section{
			N: i + 1, Author: s.author, Title: s.title, TS: int64(1000 + i*60), Meta: m,
		})
	}
	return p
}

func task(n int, to []string, status Status) Meta {
	return Meta{Kind: KindTask, Task: n, To: to, Status: status}
}

// TestSharedTaskNeedsEveryOwner is the reason completion is tracked per owner: one
// investigation covering two platforms is one task, and the first owner finishing must
// not erase the other owner's outstanding work from the board.
func TestSharedTaskNeedsEveryOwner(t *testing.T) {
	p := build(
		sec{"pm", "kickoff", Meta{}},
		sec{"pm", "Crash on resume", task(1, []string{"ios", "android"}, StatusOpen)},
		sec{"ios", "iOS: background timer", task(1, nil, StatusDone)},
	)
	got, ok := p.Task(1)
	if !ok {
		t.Fatal("task 1 not found")
	}
	if got.Status != StatusWIP {
		t.Errorf("one of two owners done should read as wip, got %q", got.Status)
	}
	if got.Title != "Crash on resume" {
		t.Errorf("title must come from the OPENING section, got %q", got.Title)
	}
	byOwner := map[string]Status{}
	for _, o := range got.Owners {
		byOwner[o.Author] = o.Status
	}
	if byOwner["ios"] != StatusDone || byOwner["android"] != StatusOpen {
		t.Errorf("per-owner status wrong: %#v", byOwner)
	}

	// Now the second owner finishes: only then is the task done.
	p.Sections = append(p.Sections, Section{
		N: 4, Author: "android", Title: "Android: same root cause", TS: 1300,
		Meta: task(1, nil, StatusDone),
	})
	if got, _ := p.Task(1); got.Status != StatusDone {
		t.Errorf("all owners done should read as done, got %q", got.Status)
	}
}

// TestTaskTitleSurvivesUpdates pins the trap: an update's section title describes the
// UPDATE, so folding the latest title would silently rename the task on every report.
func TestTaskTitleSurvivesUpdates(t *testing.T) {
	p := build(
		sec{"pm", "Order API contract", task(1, []string{"backend"}, StatusOpen)},
		sec{"backend", "Done 2 of 3 endpoints", task(1, nil, StatusWIP)},
	)
	got, _ := p.Task(1)
	if got.Title != "Order API contract" {
		t.Fatalf("task was renamed by an update: %q", got.Title)
	}
	if got.LastNote != "Done 2 of 3 endpoints" {
		t.Fatalf("the update's title should surface as the last note, got %q", got.LastNote)
	}
}

func TestOpenerManagesButDoesNotComplete(t *testing.T) {
	base := []sec{
		{"pm", "Payment screen", task(1, []string{"ios"}, StatusOpen)},
	}
	t.Run("opener may drop", func(t *testing.T) {
		p := build(append(append([]sec{}, base...), sec{"pm", "not needed", task(1, nil, StatusDropped)})...)
		if got, _ := p.Task(1); got.Status != StatusDropped {
			t.Fatalf("the opener could not drop the task: %q", got.Status)
		}
	})
	t.Run("opener reopening hands it back", func(t *testing.T) {
		p := build(append(append([]sec{}, base...),
			sec{"ios", "shipped", task(1, nil, StatusDone)},
			sec{"pm", "regressed", task(1, nil, StatusOpen)},
		)...)
		// The reopen clears the override rather than pinning the task at "open", so the
		// board reflects the owners again.
		if got, _ := p.Task(1); got.Status != StatusDone {
			t.Fatalf("after a reopen the per-owner state should rule, got %q", got.Status)
		}
	})
	t.Run("a stranger may not move it", func(t *testing.T) {
		p := build(base...)
		if err := p.CheckTaskOwner(1, "erp"); !HasCode(err, CodeNotTaskOwner) {
			t.Fatalf("want not_task_owner, got %v", err)
		}
		if err := p.CheckTaskOwner(1, "ios"); err != nil {
			t.Fatalf("an owner must be allowed: %v", err)
		}
		if err := p.CheckTaskOwner(1, "pm"); err != nil {
			t.Fatalf("the opener must be allowed: %v", err)
		}
	})
}

// TestTaskEventsDoNotTakeTheTurn is the rule that lets a coordinator dispatch work:
// bookkeeping is not conversation, so it neither takes nor hands on the turn.
func TestTaskEventsDoNotTakeTheTurn(t *testing.T) {
	p := build(
		sec{"pm", "kickoff", Meta{}},
		sec{"pm", "task one", task(1, []string{"ios"}, StatusOpen)},
		sec{"pm", "task two", task(2, []string{"android"}, StatusOpen)},
	)
	if err := p.CheckTurn("pm", KindTask); err != nil {
		t.Fatalf("opening tasks in a row must be allowed: %v", err)
	}
	if p.TurnState().LastAuthor != "pm" {
		t.Fatalf("turn should still be derived from the last MESSAGE: %#v", p.TurnState())
	}
	if err := p.CheckTurn("pm", KindMessage); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("the message turn rule must still bite: %v", err)
	}
	if err := p.CheckTurn("ios", KindMessage); err != nil {
		t.Fatalf("another agent must be free to reply: %v", err)
	}
}

func TestNextTaskNoNeverRecycles(t *testing.T) {
	p := build(
		sec{"pm", "one", task(1, []string{"a"}, StatusOpen)},
		sec{"pm", "three", task(3, []string{"a"}, StatusOpen)},
		// A plain message merely REFERENCING a task still reserves the number, so a
		// stale reference can never come to mean a different task.
		sec{"a", "about seven", Meta{Task: 7}},
	)
	if got := p.NextTaskNo(); got != 8 {
		t.Fatalf("next task number should clear every reference, got %d", got)
	}
}

func TestOrphanedTaskDoesNotBreakTheBoard(t *testing.T) {
	// The opening section was deleted by hand: history remains, title and opener do not.
	p := build(sec{"ios", "still working", task(4, nil, StatusWIP)})
	got, ok := p.Task(4)
	if !ok {
		t.Fatal("an orphaned task should still appear")
	}
	if !got.Orphaned {
		t.Fatal("the task should be marked orphaned")
	}
	if len(p.Tasks()) != 1 {
		t.Fatalf("the board should survive an orphan: %#v", p.Tasks())
	}
}

// TestOwedRules pins the two acknowledgement rules — the whole basis for knowing
// whether work is moving without inventing presence.
func TestOwedRules(t *testing.T) {
	p := build(
		sec{"pm", "kickoff", Meta{}},                                      // §1 broadcast
		sec{"pm", "question", Meta{To: []string{"backend"}}},              // §2 message
		sec{"pm", "the work", task(1, []string{"android"}, StatusOpen)},   // §3 task
		sec{"backend", "chat with ios", Meta{To: []string{"ios"}}},        // §4
		sec{"android", "unrelated remark", Meta{To: []string{"backend"}}}, // §5
	)
	owed := p.Owed()
	// Keyed by the section that addressed it, so each rule is checked on its own case
	// rather than on "does this author owe anything at all".
	outstanding := map[int]bool{}
	for _, o := range owed {
		outstanding[o.Section] = true
	}
	// backend posted §4, after §2 → a MESSAGE is acknowledged by any later post.
	if outstanding[2] {
		t.Error("a later post should acknowledge a message")
	}
	// android posted §5, but never a task event on T1 → a TASK needs an event on that
	// task, because an owner can be alive and still sitting silently on the work.
	if !outstanding[3] {
		t.Error("chatting elsewhere must not acknowledge a task")
	}
	// §5 addressed backend, who has not posted since — still outstanding, correctly.
	if !outstanding[5] {
		t.Error("a message nobody has answered yet should be outstanding")
	}
	// A broadcast is owed by nobody in particular.
	for _, o := range owed {
		if o.Section == 1 {
			t.Error("a broadcast should not be owed by anyone")
		}
	}
	if got := p.AwaitedBy("pm"); len(got) != 1 || got[0].To != "android" {
		t.Errorf("pm should be waiting on android only: %#v", got)
	}
	if got := p.OwedBy("ios"); len(got) != 1 || got[0].From != "backend" {
		t.Errorf("ios should owe backend: %#v", got)
	}
}

func TestSilenceWarningsFlagUnknownAndQuietTargets(t *testing.T) {
	now := time.Unix(100000, 0)
	p := build(sec{"pm", "kickoff", Meta{}})
	got := p.SilenceWarnings([]string{"pm", "nobody"}, now)
	if len(got) != 2 {
		t.Fatalf("want a warning for the quiet author and the unknown one, got %#v", got)
	}
}

// TestParticipantsIncludeTheAgentThatNeverShowedUp is the case the board exists for:
// an agent given work that has never posted appears in no section, and is exactly the
// one a person is looking for.
func TestParticipantsIncludeTheAgentThatNeverShowedUp(t *testing.T) {
	p := build(
		sec{"pm", "kickoff", Meta{}},
		sec{"pm", "the work", task(1, []string{"android"}, StatusOpen)},
	)
	var found *Participant
	for i, part := range p.Participants() {
		if part.Author == "android" {
			found = &p.Participants()[i]
		}
	}
	if found == nil {
		t.Fatal("an owner who has never posted must still be listed")
	}
	if found.LastSection != 0 || len(found.Owes) != 1 {
		t.Fatalf("want no activity and one debt, got %#v", *found)
	}
}
