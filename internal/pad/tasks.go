package pad

import (
	"sort"
	"strconv"
	"strings"
)

// A task is not a row that gets edited. It is OPENED by one section and MOVED by later
// ones, and everything below is a fold over those events — the same shape as TurnState.
// That is what lets a work ledger live in an append-only file behind an append-only MCP
// surface: opening, updating and closing a task are ordinary posts carrying metadata,
// and no mutating tool exists.
//
// The fold is computed from metadata alone, so it never materialises a section body.
// That is what makes the board affordable to ask for often on a 600-section pad — which
// is the entire point of having one.

// OwnerState is one owner's slice of a task. Completion is tracked PER OWNER because a
// shared task (one investigation, two platforms) is the common case, and a single
// last-event-wins status would let the first `done` close the task and erase the other
// owner's outstanding work from the board.
//
// No extra key is needed to know whose status an event carries: every section already
// records its author.
type OwnerState struct {
	Author      string `json:"author"`
	Status      Status `json:"status"`
	LastSection int    `json:"last_section,omitempty"`
	LastTS      int64  `json:"last_ts,omitempty"`
}

// Task is one folded task.
type Task struct {
	Task          int          `json:"task"`
	Title         string       `json:"title"`
	Status        Status       `json:"status"` // the aggregate over Owners
	Opener        string       `json:"opener,omitempty"`
	OpenedSection int          `json:"opened_section,omitempty"`
	OpenedTS      int64        `json:"opened_ts,omitempty"`
	Owners        []OwnerState `json:"owners"`
	LastSection   int          `json:"last_section"`
	LastTS        int64        `json:"last_ts"`
	LastNote      string       `json:"last_note,omitempty"` // the last event's title
	Orphaned      bool         `json:"orphaned,omitempty"`
}

// Label is how the task is named to a person: T3, never #3 — section numbers share the
// screen with it.
func (t Task) Label() string { return "T" + strconv.Itoa(t.Task) }

// Open reports whether the task still needs attention.
func (t Task) Open() bool { return t.Status != StatusDone && t.Status != StatusDropped }

// Tasks folds the pad into its task board, ordered by task number.
func (p *Pad) Tasks() []Task {
	byNo := map[int][]Section{}
	var numbers []int
	for _, sec := range p.Sections {
		if !sec.IsTask() || sec.Task == 0 {
			continue
		}
		if _, seen := byNo[sec.Task]; !seen {
			numbers = append(numbers, sec.Task)
		}
		byNo[sec.Task] = append(byNo[sec.Task], sec)
	}
	sort.Ints(numbers)

	out := make([]Task, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, foldTask(n, byNo[n]))
	}
	return out
}

// Task returns one folded task.
func (p *Pad) Task(n int) (Task, bool) {
	var events []Section
	for _, sec := range p.Sections {
		if sec.IsTask() && sec.Task == n {
			events = append(events, sec)
		}
	}
	if len(events) == 0 {
		return Task{}, false
	}
	return foldTask(n, events), true
}

// Thread returns every section concerning a task — the opening event, later events, and
// plain messages that merely reference it — in order. It is the per-task transcript an
// agent reads instead of the pad around it.
func (p *Pad) Thread(n int) []Section {
	return p.Select(Selector{Task: n}).Sections
}

// foldTask replays one task's events in file order. events is non-empty and ordered.
func foldTask(n int, events []Section) Task {
	t := Task{Task: n, Status: StatusOpen}

	// The opening event is the one that declared the task open. If it was deleted by
	// hand the task is orphaned: it has history but no title or opener, which renders
	// as orphaned rather than failing — the same courtesy the format extends to a
	// vanished pad file.
	for _, ev := range events {
		if ev.Status == StatusOpen {
			t.Title, t.Opener = ev.Title, ev.Author
			t.OpenedSection, t.OpenedTS = ev.N, ev.TS
			break
		}
	}
	if t.OpenedSection == 0 {
		t.Orphaned = true
		t.Title = "(opening section is gone)"
	}

	var owners []string
	perOwner := map[string]OwnerState{}
	var openerOverride Status

	for _, ev := range events {
		if len(ev.To) > 0 {
			owners = append([]string(nil), ev.To...) // reassignment: the latest `to` wins
		}
		if ev.Status == "" {
			continue // an update with no transition: it is a note, not a move
		}
		// The opener manages the task (reassign, drop, force-close) but does not occupy
		// a completion slot — the owner set means "the parties whose completion is
		// required", and a coordinator is not one of them. Without these rights a task
		// assigned to an agent that never returns would be immortal.
		if ev.Author == t.Opener && !containsStr(owners, ev.Author) {
			switch {
			case ev.N == t.OpenedSection:
				// Creating the task is not a management action. Counting it as one
				// would pin every task at the status it was born with, which is the
				// one status a fold must never get stuck on.
			case ev.Status == StatusOpen:
				// Reopening hands the task back to its owners rather than asserting an
				// answer over them, so it CLEARS the override instead of setting one.
				openerOverride = ""
			default:
				openerOverride = ev.Status
			}
			continue
		}
		perOwner[ev.Author] = OwnerState{
			Author: ev.Author, Status: ev.Status, LastSection: ev.N, LastTS: ev.TS,
		}
	}

	t.Owners = make([]OwnerState, 0, len(owners))
	for _, o := range owners {
		st, ok := perOwner[o]
		if !ok {
			st = OwnerState{Author: o, Status: StatusOpen}
		}
		t.Owners = append(t.Owners, st)
	}

	last := events[len(events)-1]
	t.LastSection, t.LastTS, t.LastNote = last.N, last.TS, last.Title
	t.Status = aggregate(t.Owners, openerOverride)
	return t
}

// aggregate reduces the per-owner states to the task's headline status. `done` requires
// EVERY current owner to be done; anything else reports the most pressing state still
// outstanding, so a half-finished shared task can never read as finished.
func aggregate(owners []OwnerState, openerOverride Status) Status {
	if openerOverride != "" {
		return openerOverride // dropped or force-closed by the opener
	}
	if len(owners) == 0 {
		return StatusOpen
	}
	allDone, anyBlocked, anyProgress := true, false, false
	for _, o := range owners {
		if o.Status != StatusDone {
			allDone = false
		}
		switch o.Status {
		case StatusBlocked:
			anyBlocked = true
		case StatusWIP, StatusDone:
			anyProgress = true
		}
	}
	switch {
	case allDone:
		return StatusDone
	case anyBlocked:
		return StatusBlocked
	case anyProgress:
		return StatusWIP
	default:
		return StatusOpen
	}
}

// NextTaskNo returns the number a newly opened task gets. It is DERIVED, the way
// section numbers already are — max + 1, computed in the metadata scan the append path
// performs anyway, so allocation costs no extra read and no counter is stored.
//
// It considers every `task:` reference, not only task events, so a number can never be
// recycled: an old section pointing at T3 must not come to mean a different task after
// T3's events are deleted by hand.
func (p *Pad) NextTaskNo() int {
	max := 0
	for _, sec := range p.Sections {
		if sec.Task > max {
			max = sec.Task
		}
	}
	return max + 1
}

// CheckTaskRef enforces that a `task:` reference names a task this pad has opened.
//
// It applies to CROSS-REFERENCES as well as task events — anyone may point at a task,
// but nobody may point at one that does not exist. Without this a mistyped number posts
// happily and attaches the section to a task nothing will ever fold, which reads as a
// lost remark rather than as the typo it is.
func (p *Pad) CheckTaskRef(taskNo int) error {
	if _, ok := p.Task(taskNo); !ok {
		return Coded(CodeNoSuchTask, "pad %s has no task T%d", p.Ref(), taskNo)
	}
	return nil
}

// CheckTaskOwner enforces who may move a task: an owner reports on their own slice, and
// the opener manages the task as a whole.
func (p *Pad) CheckTaskOwner(taskNo int, author string) error {
	t, ok := p.Task(taskNo)
	if !ok {
		return Coded(CodeNoSuchTask, "pad %s has no task T%d", p.Ref(), taskNo)
	}
	if author == t.Opener {
		return nil
	}
	for _, o := range t.Owners {
		if o.Author == author {
			return nil
		}
	}
	names := make([]string, 0, len(t.Owners))
	for _, o := range t.Owners {
		names = append(names, o.Author)
	}
	who := "nobody"
	if len(names) > 0 {
		who = strings.Join(names, ", ")
	}
	return Coded(CodeNotTaskOwner,
		"T%d is owned by %s and was opened by %q; %q may not move it — post a message instead, or ask an owner",
		taskNo, who, t.Opener, author)
}

// containsStr reports membership in a small slice.
func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
