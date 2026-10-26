package store

import (
	"time"
)

// RejoinPad is one pad an author takes part in, with the mark a relay should watch it from.
//
// Since is the author's OWN last section in that pad, and 0 when it never posted there (a
// task was handed to it, or the conversation moved to a successor while it was away). It
// is never the pad's tail: an agent that was relaunched missed whatever arrived while it
// was down, and starting from the tail would make exactly that traffic invisible.
type RejoinPad struct {
	Ref   string `json:"ref"`
	Since int    `json:"since"`
}

// RejoinPlan is what Rejoinable found, and — counted, not dropped — what it left out.
type RejoinPlan struct {
	Pads []RejoinPad `json:"pads"`

	// Protected pads are left out because no password travels with a rejoin. A relay
	// registered on one without it could not read it, and would be silent there forever.
	Protected int `json:"protected,omitempty"`
	// Idle pads had no activity within the window the caller asked for.
	Idle int `json:"idle,omitempty"`
	// Unreadable pads could not be parsed at all.
	Unreadable int `json:"unreadable,omitempty"`
}

// maxContinuationChain bounds the walk from a continued pad to its live end. A chain is
// one pad per `limits.max_sections` sections, so a real one is short; the bound exists
// only so a hand-edited header that points in a circle cannot hang the caller.
const maxContinuationChain = 64

// Rejoinable lists the live pads an author takes part in: pads it has posted to, and pads
// where it owns or opened a task that is still open. It answers the question a relaunched
// agent's relay must ask — "what was this identity in the middle of" — from the transcript
// alone, the same way the inbox and the task board are derived. No subscription list is
// kept anywhere, so there is nothing to go stale.
//
// A continued pad is replaced by the live end of its chain: the old pad refuses every post,
// and the successor is where any answer will land. Pads whose live end has been quiet for
// longer than maxIdle are left out (0 means no limit) — an agent that took part in a
// conversation last month is not waiting on it now, and a relay that watched it would only
// nudge it about that conversation's leftovers.
func (s *Store) Rejoinable(author string, maxIdle time.Duration, now time.Time) (RejoinPlan, error) {
	var plan RejoinPlan
	metas, _, err := s.List("")
	if err != nil {
		return plan, err
	}
	byRef := make(map[string]PadMeta, len(metas))
	for _, m := range metas {
		byRef[m.Ref] = m
	}

	seen := map[string]bool{}
	for _, m := range metas {
		if m.Unreadable != "" {
			plan.Unreadable++
			continue
		}
		if m.Protected {
			plan.Protected++
			continue
		}
		p, err := s.readMeta(m.Ref)
		if err != nil {
			plan.Unreadable++
			continue
		}
		if !takesPart(p, author) {
			continue
		}

		end := m
		for i := 0; end.ContinuedBy != "" && i < maxContinuationChain; i++ {
			next, ok := byRef[end.ContinuedBy]
			if !ok {
				break
			}
			end = next
		}
		if end.ContinuedBy != "" || end.Unreadable != "" || seen[end.Ref] {
			continue
		}
		seen[end.Ref] = true
		if end.Protected {
			plan.Protected++
			continue
		}
		if maxIdle > 0 && now.Sub(time.Unix(end.LastTS, 0)) > maxIdle {
			plan.Idle++
			continue
		}
		live := p
		if end.Ref != m.Ref {
			if live, err = s.readMeta(end.Ref); err != nil {
				plan.Unreadable++
				continue
			}
		}
		plan.Pads = append(plan.Pads, RejoinPad{Ref: end.Ref, Since: live.Inbox(author).Since})
	}
	return plan, nil
}

// takesPart reports whether author has a stake in p that a relay should follow: a section
// of its own, or a task it owns or opened that is still open. Being merely addressed does
// not count — that is not something the agent's previous relay would have followed either,
// since registration only ever happened when the agent itself touched the pad.
func takesPart(p *Pad, author string) bool {
	if p.HasPosted(author) {
		return true
	}
	for _, t := range p.Tasks() {
		if !t.Open() {
			continue
		}
		if t.Opener == author {
			return true
		}
		for _, o := range t.Owners {
			if o.Author == author {
				return true
			}
		}
	}
	return false
}

// readMeta parses one pad by ref, metadata only and without the password gate.
func (s *Store) readMeta(ref string) (*Pad, error) {
	project, id, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	return s.readNoPassword(project, id)
}
