package pad

import "strconv"

// Turn describes whose move it is, derived entirely from the last MESSAGE section.
type Turn struct {
	LastAuthor string   `json:"last_author"`
	Blocked    []string `json:"blocked"`
	WaitingFor string   `json:"waiting_for"`
}

// LastMessage returns the last section belonging to the conversation stream — the one
// that holds the turn. A pad whose sections are all bookkeeping (only reachable by hand
// editing) has none, and then nobody is blocked.
//
// The test is `== KindMessage`, deliberately not `!= KindTask`: every stream added later
// is bookkeeping until proven otherwise, and stating it the other way round would hand
// the turn to each new kind on the day it appears — silently, and to the one piece of
// state every agent relies on.
func (p *Pad) LastMessage() (Section, bool) {
	for i := len(p.Sections) - 1; i >= 0; i-- {
		if p.Sections[i].Kind == KindMessage {
			return p.Sections[i], true
		}
	}
	return Section{}, false
}

// TurnState derives the turn: the author of the last MESSAGE is blocked, everyone else
// may post.
//
// Filtering by kind is the whole difference between a two-agent pad and a five-agent
// one. Bookkeeping is not conversation: a coordinator opening five tasks in a row is
// not monologuing at anybody, and a progress report is not an answer somebody was
// waiting for. So task events neither take the turn nor hand it on — and the rule stays
// purely derived, because the filter happens before taking the last element, not by
// remembering anything.
// A pad that has been CONTINUED holds no turn at all. It accepts nothing from anyone, so
// naming one blocked author would say the opposite of what is true — "waiting for any
// author other than pm" reads as an invitation to pm's counterpart, who would write a
// reply and only then be told the pad is closed. Everyone is blocked, and the answer says
// where the conversation went.
func (p *Pad) TurnState() Turn {
	if next := p.Header.ContinuedBy; next != "" {
		last, _ := p.LastMessage()
		return Turn{
			LastAuthor: last.Author,
			Blocked:    p.Authors(),
			WaitingFor: "nobody — this pad is full and was continued in " + next,
		}
	}
	last, ok := p.LastMessage()
	if !ok {
		return Turn{WaitingFor: "any author"}
	}
	return Turn{
		LastAuthor: last.Author,
		Blocked:    []string{last.Author},
		WaitingFor: "any author other than " + strconv.Quote(last.Author),
	}
}

// CheckTurn enforces the turn rule for a section about to be appended. Only a message
// takes the turn; task events and rules are bookkeeping and are exempt.
//
// Like every rule here this is a GUARD RAIL, not security: identity is self-declared,
// so a determined agent can claim any author or label a message as a task. It stops
// accidents and drift between cooperating agents, which is what it is for.
func (p *Pad) CheckTurn(author string, kind Kind) error {
	if kind != KindMessage {
		return nil
	}
	last, ok := p.LastMessage()
	if !ok || last.Author != author {
		return nil
	}
	return Coded(CodeNotYourTurn,
		"you (%q) posted section %d; wait for another agent to reply (use pad_wait)", author, last.N)
}
