package pad

import (
	"fmt"
	"strconv"
	"time"
)

// SilenceThreshold is how long an addressee may have been quiet before a post to them
// carries a warning. It is a warning, never a refusal: the addressee may simply be
// working, and a post that succeeded must not look like it failed.
const SilenceThreshold = 30 * time.Minute

// SilenceWarnings reports which of a post's addressees look like nobody is listening —
// at the one moment the sender can still do something about it, and computed from the
// metadata scan the append path performs anyway.
//
// This is what presence was wanted for, without presence: "has not posted since §12"
// is derivable and never lies, whereas "is not currently blocked in a wait" is neither.
func (p *Pad) SilenceWarnings(to []string, now time.Time) []string {
	lastPost := map[string]Section{}
	for _, sec := range p.Sections {
		lastPost[sec.Author] = sec
	}
	var out []string
	for _, target := range to {
		last, seen := lastPost[target]
		if !seen {
			out = append(out, fmt.Sprintf(
				"%q has never posted in this pad — check the name, or tell them the ref", target))
			continue
		}
		if age := now.Sub(time.Unix(last.TS, 0)); age > SilenceThreshold {
			out = append(out, fmt.Sprintf(
				"%q has not posted since §%d (%s ago) — nobody may be listening", target, last.N, roundAge(age)))
		}
	}
	return out
}

// roundAge renders a duration the way a person reads one: whole hours or whole minutes,
// never "3h17m42.113s".
func roundAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// Whether an agent is "present" cannot be derived from an append-only transcript: a
// crashed agent never appends "I left". It is also the wrong question — it lies in both
// directions, since an agent implementing for two hours is not inside a wait and shows
// as absent, while an agent parked in a wait with the wrong selector shows as present.
//
// Whether the work MOVED is derivable, and is the fact worth acting on. That is what
// this file computes.

// Owed is one outstanding acknowledgement: something From addressed to To that To has
// not answered.
type Owed struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Section int    `json:"section"`         // the section that addressed it
	Task    int    `json:"task,omitempty"`  // 0 when it is a message
	What    string `json:"what"`            // "T3" or "§40", ready to print
	Title   string `json:"title,omitempty"` // what was asked
	TS      int64  `json:"ts"`              // when it was addressed
}

// Owed lists every outstanding acknowledgement in the pad.
//
// The two streams have different rules on purpose. A MESSAGE is acknowledged by any
// later post from the addressee: that proves they are alive and reading, which is what
// the sender needs to know. A TASK needs an event on that task from its owner, because
// an owner can be alive, busy talking about something else, and still sitting silently
// on the work — and `status: wip` is exactly the "I have this" signal that rule asks for.
func (p *Pad) Owed() []Owed {
	lastPost := map[string]int{}
	for _, sec := range p.Sections {
		if sec.N > lastPost[sec.Author] {
			lastPost[sec.Author] = sec.N
		}
	}

	var out []Owed
	for _, t := range p.Tasks() {
		if !t.Open() {
			continue
		}
		for _, o := range t.Owners {
			if o.LastSection != 0 {
				continue // the owner has responded on this task
			}
			out = append(out, Owed{
				From: t.Opener, To: o.Author, Section: t.OpenedSection, Task: t.Task,
				What: t.Label(), Title: t.Title, TS: t.OpenedTS,
			})
		}
	}
	for _, sec := range p.Sections {
		if sec.IsTask() || sec.Broadcast() {
			continue // a broadcast is owed by nobody in particular
		}
		for _, target := range sec.To {
			if lastPost[target] > sec.N {
				continue
			}
			out = append(out, Owed{
				From: sec.Author, To: target, Section: sec.N,
				What: "§" + strconv.Itoa(sec.N), Title: sec.Title, TS: sec.TS,
			})
		}
	}
	return out
}

// OwedBy returns what a given author owes others — used to flag the agent that has
// fallen behind.
func (p *Pad) OwedBy(author string) []Owed {
	return filterOwed(p.Owed(), func(o Owed) bool { return o.To == author })
}

// AwaitedBy returns what others owe a given author — what a waiting agent is stuck on,
// and what `--unacked` fires on.
func (p *Pad) AwaitedBy(author string) []Owed {
	return filterOwed(p.Owed(), func(o Owed) bool { return o.From == author })
}

// filterOwed keeps the entries matching a predicate.
func filterOwed(all []Owed, keep func(Owed) bool) []Owed {
	var out []Owed
	for _, o := range all {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

// Participant is one author's standing in the pad: when they were last heard from, and
// what is waiting on them.
type Participant struct {
	Author      string `json:"author"`
	LastSection int    `json:"last_section"`
	LastTS      int64  `json:"last_ts"`
	Owes        []Owed `json:"owes,omitempty"`
}

// Participants reports every author in the pad with their last activity and what they
// owe, newest activity first. This is the board a person reads to find out who has
// fallen behind — the derivable answer to the question presence was wanted for.
func (p *Pad) Participants() []Participant {
	owed := p.Owed()
	idx := map[string]int{}
	var out []Participant
	for _, sec := range p.Sections {
		i, seen := idx[sec.Author]
		if !seen {
			idx[sec.Author] = len(out)
			out = append(out, Participant{Author: sec.Author, LastSection: sec.N, LastTS: sec.TS})
			continue
		}
		out[i].LastSection, out[i].LastTS = sec.N, sec.TS
	}
	// An agent that was given work and has never said anything does not appear in the
	// sections at all — and is precisely the one a person is looking for. It is listed
	// with no last activity, which is the whole story about it.
	for _, o := range owed {
		if _, seen := idx[o.To]; !seen {
			idx[o.To] = len(out)
			out = append(out, Participant{Author: o.To})
		}
	}
	for i := range out {
		out[i].Owes = filterOwed(owed, func(o Owed) bool { return o.To == out[i].Author })
	}
	// Sort by last activity, freshest first, without pulling in a comparator for four
	// authors: an insertion pass is clearer at this size.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].LastTS > out[j-1].LastTS; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Inbox is what one agent missed while it was away. "Unread" means "you have not posted
// since it" — the observable fact, and the one that matters: an agent that read a
// section but never acted on it is indistinguishable from one that never saw it, and
// both need the same nudge.
type Inbox struct {
	Since    int       `json:"since"` // the asking author's own last section
	Unread   []Section `json:"unread"`
	Owes     []Owed    `json:"owes,omitempty"`     // what this author owes others
	Awaiting []Owed    `json:"awaiting,omitempty"` // what others owe this author
}

// Inbox derives one author's backlog: the sections since their last post that were
// meant for them, plus both directions of outstanding acknowledgement.
func (p *Pad) Inbox(author string) Inbox {
	in := Inbox{Owes: p.OwedBy(author), Awaiting: p.AwaitedBy(author)}
	for _, sec := range p.Sections {
		if sec.Author == author && sec.N > in.Since {
			in.Since = sec.N
		}
	}
	wake := Wake{Me: true, Mine: true}
	for _, sec := range p.Sections {
		if sec.N > in.Since && p.Wakes(sec, author, wake) {
			sec.Content = ""
			in.Unread = append(in.Unread, sec)
		}
	}
	return in
}
