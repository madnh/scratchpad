// Package pad is the pure domain layer for pads: the on-disk format, the section
// metadata grammar, and every rule and derived view built on them.
//
// It performs NO I/O and takes no locks — that is internal/store's job. The split
// exists because the CLI, the MCP server and the Web UI all need the same answers
// ("whose turn is it", "which sections do I want", "what is T3's status"), and when
// those answers are computed from a raw []Section handed out by the storage layer,
// each surface grows its own subtly different copy. Everything here is a pure function
// over a parsed pad, so it is testable without a filesystem and there is exactly one
// implementation of each meaning.
package pad

import "strconv"

// Kind is the stream a section belongs to. Streams share one file, one numbering and
// one append-only sequence; what differs is the rules each one carries (see CheckTurn).
type Kind string

const (
	// KindMessage is the conversation: it takes turns.
	KindMessage Kind = "message"
	// KindTask is the work ledger: bookkeeping, exempt from the turn rule.
	KindTask Kind = "task"
	// KindRules is the pad's house style: how the agents on this pad are expected to
	// write. Like a task event it is bookkeeping and does NOT take the turn — stating
	// the rules is not a move in the conversation.
	KindRules Kind = "rules"
)

// SystemAuthor is the reserved identity for something a PERSON did through a surface
// that has no identity of its own — today, editing a pad's rules in the Web UI.
//
// It is a fixed string rather than something derived from the executable name, for the
// same reason the pad header says "scratchpad v1" and the marker says
// `type: "scratchpad"`: renaming the binary must not change what an already-written pad
// file means. ValidateAuthor refuses it, so no agent can claim it; only the rules-writing
// path is allowed to use it.
const SystemAuthor = "scratchpad"

// Status is a task event's state transition. Absent on an event means "no change".
type Status string

const (
	StatusOpen    Status = "open"
	StatusWIP     Status = "wip"
	StatusBlocked Status = "blocked"
	StatusDone    Status = "done"
	StatusDropped Status = "dropped"
)

// validStatus reports whether s is one of the five known transitions.
func validStatus(s Status) bool {
	switch s {
	case StatusOpen, StatusWIP, StatusBlocked, StatusDone, StatusDropped:
		return true
	}
	return false
}

// Meta is the routing/threading/task metadata carried on a section's comment line.
// Every field is optional; the zero value is an ordinary broadcast message, which is
// exactly what every section written before this format existed parses as.
type Meta struct {
	Kind   Kind     `json:"kind,omitempty"`
	To     []string `json:"to,omitempty"`
	Re     int      `json:"re,omitempty"`
	Task   int      `json:"task,omitempty"`
	Status Status   `json:"status,omitempty"`

	// Replace belongs to a rules section: it cuts the inheritance chain, so the levels
	// above (project, store) are ignored instead of being extended. It is written as
	// `rules: replace` on the metadata line and means nothing on any other kind.
	Replace bool `json:"replace,omitempty"`
}

// Section is one post in a pad. Meta is embedded so it flattens in JSON: a section
// serialises as {n, author, title, ts, kind, to, …} rather than nesting a "meta" object
// that every surface would have to reach through.
type Section struct {
	N       int    `json:"n"`
	Author  string `json:"author"`
	Title   string `json:"title"`
	TS      int64  `json:"ts"` // unix seconds
	Content string `json:"content,omitempty"`
	Meta
}

// IsTask reports whether this section is part of a task's record (as opposed to merely
// referencing a task, which a plain message may also do).
func (s Section) IsTask() bool { return s.Kind == KindTask }

// IsRules reports whether this section states the pad's rules.
func (s Section) IsRules() bool { return s.Kind == KindRules }

// Broadcast reports whether the section is addressed to everyone. Absent `to` means
// broadcast for a message; a task open without `to` is rejected at write time, so this
// stays unambiguous.
func (s Section) Broadcast() bool { return len(s.To) == 0 }

// AddressedTo reports whether author is named in the section's `to` list.
func (s Section) AddressedTo(author string) bool {
	for _, t := range s.To {
		if t == author {
			return true
		}
	}
	return false
}

// Label is how this section is named to a person: §12 for a section, T3 when it is a
// task event. Two number spaces appear on the same screen, so they must never look
// alike.
func (s Section) Label() string {
	if s.IsTask() && s.Task > 0 {
		return "T" + strconv.Itoa(s.Task)
	}
	return "§" + strconv.Itoa(s.N)
}

// Pad is a fully parsed pad file. Sections are in file order, which is also numbering
// order.
//
// Header and Sections are the only two things a pad is. Nothing derived is stored beside
// them — turn state, the roster, the task board are all folds over Sections, computed on
// demand, so there is never a cached answer to go stale against the file.
type Pad struct {
	Project  string
	ID       string
	Header   Header
	Sections []Section
}

// Ref returns the pad's full copy-pasteable identifier `<project>-<padid>`.
func (p *Pad) Ref() string { return p.Project + "-" + p.ID }

// CreatedTS is when the pad was opened, as a unix timestamp.
func (p *Pad) CreatedTS() int64 { return p.Header.Created.Unix() }

// PasswordHash is the pad's bcrypt hash, empty when it is unprotected.
func (p *Pad) PasswordHash() string { return p.Header.PasswordHash }

// Protected reports whether the pad requires a password.
func (p *Pad) Protected() bool { return p.Header.PasswordHash != "" }

// Continues is the ref of the pad this one took over from, empty on an original pad.
func (p *Pad) Continues() string { return p.Header.Continues }

// Last returns the final section, whatever its kind. Every pad has at least one section
// (created with section 1), so callers may rely on it existing.
func (p *Pad) Last() Section { return p.Sections[len(p.Sections)-1] }

// Find returns the section numbered n.
func (p *Pad) Find(n int) (Section, bool) {
	for _, sec := range p.Sections {
		if sec.N == n {
			return sec, true
		}
	}
	return Section{}, false
}

// Authors returns every author who has posted in this pad, in first-appearance order —
// section 1's author first, so the pad's opener leads the list. It is the pad's roster:
// what a listing publishes, and what lets a post warn about a `to` target never seen here.
//
// Derived on demand rather than stored: an author exists only by having posted, so the
// sections already ARE the roster. Recording it a second time in the pad header would
// turn an O(chunk) append into an O(size) rewrite of the file's first line, and would go
// stale the moment a pad is edited by hand.
//
// Deliberately NOT the same set as Participants: this is who has SPOKEN. Participants
// also counts an agent that was addressed and never answered — usually the one a person
// is looking for — which is a fact about a pad's inside, not part of its listing entry.
// SystemAuthor is excluded: it is not an agent on the pad but the tool writing down what
// a person changed, and listing it as a participant would put a fictional teammate in
// every view built on the roster.
func (p *Pad) Authors() []string {
	seen := make(map[string]bool, len(p.Sections))
	var out []string
	for _, sec := range p.Sections {
		if sec.Author == SystemAuthor || seen[sec.Author] {
			continue
		}
		seen[sec.Author] = true
		out = append(out, sec.Author)
	}
	return out
}

// Opener is the agent that owns this pad. It is who the `opener` rules policy trusts with
// the pad's rules, and the reason that policy is the default: a pad is nearly always
// opened by the agent handing work to the others, so the one who framed the job is the one
// who says how it is worked.
//
// It reads the HEADER and nothing else. It used to return section 1's author, which is the
// same answer on a pad opened by hand and the wrong one on a pad that CONTINUES another:
// there, section 1 belongs to whichever agent happened to fill the previous pad, and
// ownership would pass to a passer-by. A rule with two derivations has two answers the day
// they diverge, so the derivation from section 1 lives in exactly one place now — Upgrade,
// which runs once per v1 file and writes the answer into the header.
func (p *Pad) Opener() string {
	return p.Header.Opener
}

// HasPosted reports whether an author has any section in this pad. It is what "first
// time here" means — derived from the transcript, so no membership list is stored and a
// hand-deleted section takes the membership with it.
func (p *Pad) HasPosted(author string) bool {
	for _, sec := range p.Sections {
		if sec.Author == author {
			return true
		}
	}
	return false
}

// RulesSection returns the pad's rules — the LAST `kind: rules` section — plus the
// numbers of the earlier ones, newest first.
//
// Several rules sections are not several sets of rules: they are versions of one, and
// the last one wins, exactly as editing a rules FILE replaces what was there. The pad is
// append-only, so the earlier versions stay readable instead of being overwritten; that
// is history, not something still in force.
func (p *Pad) RulesSection() (cur Section, history []int, ok bool) {
	for i := len(p.Sections) - 1; i >= 0; i-- {
		if !p.Sections[i].IsRules() {
			continue
		}
		if !ok {
			cur, ok = p.Sections[i], true
			continue
		}
		history = append(history, p.Sections[i].N)
	}
	return cur, history, ok
}

// Title is the pad's display title. A pad has no name of its own, so it borrows the
// title of its first section — the opening question is what makes it recognisable in a
// listing. It is a method rather than something each caller digs out of Sections[0],
// which is how three surfaces end up disagreeing about what a pad is called.
func (p *Pad) Title() string {
	if len(p.Sections) == 0 {
		return ""
	}
	return p.Sections[0].Title
}
