package pad

import (
	"strconv"
	"strings"
)

// Reading is universal; waking is selective. Every agent can always read every section
// of a pad — that never changes. What these selectors decide is which sections are
// worth INTERRUPTING an agent for.
//
// Without the split, a five-agent pad wakes all five for every exchange, so three of
// them pay a context bill for a conversation belonging to the other two. With it, the
// bill lands on the participants.

// MaxWakeSelectors bounds one call's selector list, like every other resource here.
const MaxWakeSelectors = 20

// Wake is a parsed selector set. The selectors are a UNION: any one of them matching is
// enough to wake.
type Wake struct {
	Any     bool  // any new section — the default, and today's behaviour
	Me      bool  // addressed to me, replying to me, or broadcast
	Mine    bool  // a task event on a task I own
	Tasks   bool  // any task event
	TaskNos []int // task events on these specific tasks, whoever owns them
}

// DefaultWake is what a caller that says nothing gets: unchanged behaviour, so an
// existing script or skill keeps working.
func DefaultWake() Wake { return Wake{Any: true} }

// NeedsAuthor reports whether the selector set can only be evaluated with an identity.
func (w Wake) NeedsAuthor() bool { return w.Me || w.Mine }

// Empty reports whether nothing was selected, so a caller can fall back to the default.
// Wake holds a slice and therefore is not comparable with ==, which is exactly the kind
// of thing worth having a method for rather than rediscovering at each call site.
func (w Wake) Empty() bool {
	return !w.Any && !w.Me && !w.Mine && !w.Tasks && len(w.TaskNos) == 0
}

// ParseWake reads selector specs: "any", "me", "mine", "tasks", "task:<n>". An empty
// list means the default.
func ParseWake(specs []string) (Wake, error) {
	if len(specs) == 0 {
		return DefaultWake(), nil
	}
	if len(specs) > MaxWakeSelectors {
		return Wake{}, Coded(CodeLimitExceeded, "at most %d wake selectors (got %d)", MaxWakeSelectors, len(specs))
	}
	var w Wake
	for _, raw := range specs {
		spec := strings.TrimSpace(raw)
		switch {
		case spec == "":
			continue
		case spec == "any":
			w.Any = true
		case spec == "me":
			w.Me = true
		case spec == "mine":
			w.Mine = true
		case spec == "tasks":
			w.Tasks = true
		case strings.HasPrefix(spec, "task:"):
			n, err := strconv.Atoi(strings.TrimPrefix(spec, "task:"))
			if err != nil || n <= 0 {
				return Wake{}, Coded(CodeInvalidInput, "bad wake selector %q: expected task:<number>", spec)
			}
			w.TaskNos = append(w.TaskNos, n)
		default:
			return Wake{}, Coded(CodeInvalidInput,
				"unknown wake selector %q (want any, me, mine, tasks or task:<n>)", spec)
		}
	}
	return w, nil
}

// Wakes reports whether one section should wake the given author under this selector
// set.
func (p *Pad) Wakes(sec Section, author string, w Wake) bool {
	// Your own post is never news to you. This matters most for an always-armed
	// background wait, which would otherwise fire on the very section that armed it.
	if author != "" && sec.Author == author {
		return false
	}
	// Three kinds wake everyone, whatever they asked to be woken for. Selective waking
	// exists to spare an agent traffic it has no part in; it must never spare an agent
	// something that changes what it is ALLOWED to do next. This is checked before the
	// selectors precisely so no selector can opt out of it.
	//
	//   continued — the pad can no longer receive the answer this agent is waiting for.
	//   rules     — the house style just changed under everyone on the pad, and with
	//               reacking on the next post is refused until it has been read.
	//   notice    — the tool reporting the same thing for a level that lives in a file.
	//
	// `me` used to be the trap here: it is answered by concernsAuthor, which counts a
	// broadcast only for a MESSAGE, so a pad's own rules section woke `any` and nobody
	// else. An agent narrowing its waits was precisely the one that went on posting under
	// rules it had never seen.
	switch sec.Meta.Kind {
	case KindContinued, KindRules, KindNotice:
		return true
	}
	if w.Any {
		return true
	}
	if w.Me && p.concernsAuthor(sec, author) {
		return true
	}
	if sec.IsTask() {
		if w.Tasks {
			return true
		}
		for _, n := range w.TaskNos {
			if sec.Task == n {
				return true
			}
		}
		if w.Mine && p.ownsTask(sec.Task, author) {
			return true
		}
	}
	return false
}

// concernsAuthor is the `me` selector: addressed to me, or replying to something I
// wrote — plus, for a MESSAGE only, a broadcast.
//
// Broadcast waking is deliberate: a message with no `to` means "the whole team", and
// should behave that way. It is also the migration path, since every section written
// before this format existed is a broadcast, so `me` starts out behaving exactly like
// `any` and grows quieter as agents begin addressing. A task event is excluded from
// that, because its silence about `to` means "a progress note", not "everyone" — task
// traffic reaches a watcher through the task selectors instead.
func (p *Pad) concernsAuthor(sec Section, author string) bool {
	if author == "" {
		return false
	}
	if sec.AddressedTo(author) {
		return true
	}
	if sec.Re > 0 {
		if parent, ok := p.Find(sec.Re); ok && parent.Author == author {
			return true
		}
	}
	return !sec.IsTask() && sec.Broadcast()
}

// ownsTask reports whether author is a current owner of the task — the `mine` selector.
//
// It is not covered by `me`: the section that OPENS a task carries `to`, but a
// co-owner's progress update usually does not repeat it, and that update is exactly
// what a co-owner needs to see.
func (p *Pad) ownsTask(taskNo int, author string) bool {
	if taskNo == 0 || author == "" {
		return false
	}
	t, ok := p.Task(taskNo)
	if !ok {
		return false
	}
	for _, o := range t.Owners {
		if o.Author == author {
			return true
		}
	}
	return false
}
