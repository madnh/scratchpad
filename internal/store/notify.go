package store

import (
	"fmt"
	"sort"
	"time"

	"github.com/madnh/scratchpad/internal/pad"
)

// Announcing a rules change into the pads it binds.
//
// The read gate (pad.CheckAck) makes a rules change STICK: whoever posts next has to have
// read it. What it cannot do is make the change ARRIVE. An agent parked in `pad wait` finds
// out at the moment it tries to speak, which may be an hour of work later, and an agent
// that was reasoning under the old rules the whole time has already done the thing the new
// rule exists to prevent.
//
// So the change has to reach the pad, and that means a SECTION. Not because a section is
// tidy, but because nothing else crosses the gap:
//
//   - Every agent's wait is its own process. There is no in-memory wake to deliver.
//   - internal/watch reports pad files. `_rules.md` is a tool file, not a pad, and is
//     deliberately not one — so editing it notifies nobody.
//   - Even if the watcher did report it, Wait only counts a pad as having news when a new
//     SECTION appears. Touching a file would wake every waiter to find nothing and sleep
//     again.
//
// Nothing here runs unless a caller asks for it, and the surfaces ask BY DEFAULT — the
// dialog's box is ticked, `--notify` defaults to true. That is the right way round: a rules
// version nobody is told about binds only whoever happens to post next, and the person
// editing has no other way to reach an agent that is mid-task. Announcing nothing is the
// exception (a typo fix), and it is one click or one `--notify=false` away.
//
// The distinction that survives is between "the surfaces default to yes" and "the store
// does it by itself". This function is never reached without somebody deciding, which is
// what keeps a rules edit from being a thing that silently writes into live conversations.
//
// What is left out is decided by whether the pad can USE the notice, never by who may read
// the pad:
//
//   - A pad that has already been continued takes no posts at all.
//   - A pad at its section limit is skipped, or a notice would be the post that fills it,
//     and a person fixing a sentence in the rules would find they had opened a new pad.
//   - A pad nobody has touched in NotifyActiveDays has no agent parked on it to wake.
//
// A password-protected pad is NOT skipped. A password keeps other agents out of a pad; it
// was never a reason to leave one out of an announcement. The rules bind it like any other
// pad and its agents are refused by the read gate like any other pad's, so skipping it
// would make it the one pad blocked without being told why — and the notice says nothing
// but "the rules changed", which every agent on the store may read anyway. The append goes
// through PostRequest.ToolNotice, which is where that reasoning is enforced.
//
// Every skip is REPORTED (NotifyResult.Skipped). A fan-out that quietly did less than it
// said reads as "everyone knows" — the one belief this feature must not create falsely.

// NotifyScope is the level of rules that changed, and therefore which pads it reaches: the
// store's rules bind every pad, a project's bind that project's.
//
// The pad level is deliberately absent. A pad's own rules ARE a section, so they already
// arrive the way this is trying to imitate — and pad.KindRules wakes every waiter for the
// same reason KindNotice does.
type NotifyScope struct {
	Level   pad.RuleLevel
	Project string // required when Level is LevelProject, ignored otherwise
}

// NotifyTargets is what an announcement WOULD reach, so a person can be shown the size of
// the fan-out before agreeing to it. The counts are the answer to "12 of 340" — a number
// without its denominator invites the wrong decision in both directions.
type NotifyTargets struct {
	Refs []string `json:"refs"`

	// InScope is every pad the level binds, before any filtering.
	InScope int `json:"in_scope"`
	// Skipped says why the rest were left out, keyed by reason.
	Skipped map[string]int `json:"skipped,omitempty"`
	// ActiveDays is the window that was applied, so the number can be read.
	ActiveDays int `json:"active_days"`
}

// NotifyResult is what an announcement actually did.
type NotifyResult struct {
	Notified []string       `json:"notified"`
	Skipped  map[string]int `json:"skipped,omitempty"`
	// Failed names pads the notice could not be written to, with the reason. A pad that
	// refused the append is not a reason to abandon the rest: the point is to reach as many
	// of the affected agents as possible, and the ones that were missed have to be nameable.
	Failed map[string]string `json:"failed,omitempty"`
}

// Skip reasons, spelled once and exported so the API, the CLI and the UI report the same
// words. SkipOrder is the order they are listed in, so a line a person diffs does not
// reshuffle itself between runs — map iteration is not an order.
const (
	SkipContinued = "continued"
	SkipQuiet     = "quiet"
	SkipFull      = "full"
)

// SkipOrder lists the reasons most-expected first: a quiet pad is the ordinary case and
// the other two are each a property of that particular pad.
var SkipOrder = []string{SkipQuiet, SkipContinued, SkipFull}

// RulesNotifyTargets lists the pads an announcement at this level would reach, applying the
// same filters the announcement itself does. It takes no lock and writes nothing — it is
// what the edit dialog calls to put a count beside its checkbox.
func (s *Store) RulesNotifyTargets(scope NotifyScope) (NotifyTargets, error) {
	project, err := scope.projectFilter()
	if err != nil {
		return NotifyTargets{}, err
	}
	days := s.rulesPolicy().NotifyActiveDays
	out := NotifyTargets{ActiveDays: days, Skipped: map[string]int{}}

	pads, _, err := s.List(project)
	if err != nil {
		return NotifyTargets{}, err
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	lim := s.limits()
	for _, m := range pads {
		// An unreadable pad is not a pad this can write to, and it is already reported by
		// every listing surface. Counting it in scope would inflate the denominator with
		// rows nobody can act on.
		if m.Unreadable != "" {
			continue
		}
		out.InScope++
		switch {
		case m.ContinuedBy != "":
			out.Skipped[SkipContinued]++
		case m.SectionCount >= lim.MaxSectionsPerPad:
			out.Skipped[SkipFull]++
		case m.LastTS < cutoff:
			out.Skipped[SkipQuiet]++
		default:
			out.Refs = append(out.Refs, m.Ref)
		}
	}
	sort.Strings(out.Refs)
	if len(out.Skipped) == 0 {
		out.Skipped = nil
	}
	return out, nil
}

// NotifyRulesChanged appends a notice to every pad the level binds. It is called AFTER the
// rules were written — a notice about a change that then failed to save would be the worst
// of both outcomes.
//
// Each append goes through Post, so it takes that pad's exclusive flock, obeys the section
// limit and lands in the numbering like anything else. It carries SystemPost because it is
// written under the reserved identity, which no agent may claim.
func (s *Store) NotifyRulesChanged(scope NotifyScope) (NotifyResult, error) {
	targets, err := s.RulesNotifyTargets(scope)
	if err != nil {
		return NotifyResult{}, err
	}
	res := NotifyResult{Skipped: targets.Skipped}
	title, content := scope.notice(s.reackPolicy())
	for _, ref := range targets.Refs {
		if _, err := s.Post(PostRequest{
			Ref: ref, Author: pad.SystemAuthor, Title: title, Content: content,
			Meta: Meta{Kind: pad.KindNotice}, SystemPost: true, ToolNotice: true,
		}); err != nil {
			if res.Failed == nil {
				res.Failed = map[string]string{}
			}
			res.Failed[ref] = err.Error()
			continue
		}
		res.Notified = append(res.Notified, ref)
	}
	return res, nil
}

// projectFilter turns a scope into the argument List takes, and refuses a scope that names
// no pads. The pad level is refused here rather than ignored: a caller asking to announce a
// pad's own rules has misunderstood something, and silently announcing nothing would let it
// go on believing the notice went out.
func (n NotifyScope) projectFilter() (string, error) {
	switch n.Level {
	case pad.LevelStore:
		return "", nil
	case pad.LevelProject:
		if err := ValidateProject(n.Project); err != nil {
			return "", err
		}
		return n.Project, nil
	default:
		return "", coded(CodeInvalidInput,
			"only the store and project rules are announced this way (got %q):"+
				" a pad's own rules are already a section, and every waiter on that pad wakes for it", n.Level)
	}
}

// notice is what the section says. It is addressed to agents, so it says what to DO — an
// announcement that only reports a fact leaves each agent to work out whether it is now
// blocked, and they will not all reach the same answer.
//
// It names no digest. The digest an agent must quote spans all three levels, so it differs
// per pad; printing the changed level's own token here would give every agent on a pad with
// house rules the wrong string to copy.
func (n NotifyScope) notice(policy pad.ReackPolicy) (title, content string) {
	where := "The store rules"
	if n.Level == pad.LevelProject {
		where = fmt.Sprintf("The rules of project %q", n.Project)
	}
	// What happens next is not a maybe — this deployment has a setting and the store knows
	// it. An announcement that hedges ("if this deployment re-asks…") makes every agent
	// work out which deployment it is on, and they will not all get it right.
	consequence := "Your next post is refused until it quotes the new digest, so read them now:\n" +
		"the refusal carries the full text, but not before you have written a post it throws away."
	if policy == pad.ReackOnce {
		consequence = "Nothing here will stop you posting under the old ones, so read them now:\n" +
			"on this deployment the rules are asked for once, and you have already been asked."
	}
	title = "Rules changed — read them before your next post"
	content = fmt.Sprintf("%s changed just now, and they apply to this pad.\n\n"+
		"Read them with `scratchpad pad rules <ref>`, or the pad_rules tool.\n\n%s\n", where, consequence)
	return title, content
}
