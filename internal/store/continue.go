package store

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/madnh/scratchpad/internal/pad"
)

// A pad that fills up does not end the conversation — it hands it to a successor pad and
// says so at both ends.
//
// The alternative, which this replaces as the default, is a refusal: correct, honest, and
// a dead end for everyone except the person running the deployment. An agent cannot raise
// a limit. It cannot open a replacement either without splitting the work into two
// transcripts nobody can follow — which is why the skill document tells it not to try, and
// why the tool has to be the one doing this.
//
// What makes a continuation different from "the agent opened another pad":
//
//   - The successor is found, not guessed. The old pad's last section names it, its header
//     records it, and the new pad's header points back.
//   - Nobody is left waiting. The closing section wakes every waiter regardless of their
//     selectors (see pad.KindContinued) — an agent parked on a pad that can no longer
//     receive its answer is the failure mode this must not produce.
//   - The pad keeps its identity. Owner, password and house rules carry over, and task
//     numbering continues, so "T3" still means the same work on the other side.
//
// What does NOT carry over is the conversation itself. That is deliberate: copying history
// into the new pad would double every byte and still not make the two one transcript. The
// old pad stays readable forever; only writing moves.

// continuation is what a full pad produced: the successor's ref and the section number the
// post landed on over there.
type continuation struct {
	Ref     string
	Section int
	Task    int
}

// continuePad builds the successor pad, writes the post into it, and closes the old one.
//
// oldFile is held under the exclusive flock Post already took, and oldData is its current
// contents (already upgraded to the running format). Both are needed because closing the
// old pad rewrites its header as well as appending to it.
func (s *Store) continuePad(
	project, id string, old *Pad, full *Pad, oldFile *os.File, oldData []byte,
	req PostRequest, meta Meta, now time.Time,
) (*continuation, error) {
	oldRef := project + "-" + id

	// The successor is written FIRST. If anything fails here the old pad is untouched and
	// the post is refused the way it always was — a bad continuation must not be able to
	// leave a pad closed with nowhere to go.
	body, section, task := s.renderSuccessor(oldRef, old, full, req, meta, now)
	newID, err := s.writeNewPad(project, body)
	if err != nil {
		return nil, err
	}
	newRef := project + "-" + newID

	// Only now does the old pad close. Its header records the successor so a reader that
	// starts there is never left guessing, and the closing section is what wakes everyone
	// still waiting on it.
	closing := pad.RenderSection(old.Last().N+1, pad.SystemAuthor,
		fmt.Sprintf("This pad is full — continued in %s", newRef),
		now, pad.Meta{Kind: pad.KindContinued},
		fmt.Sprintf("This pad reached its section limit. The conversation continues in %s,\n"+
			"which carries this pad's owner, house rules, password and task numbering.\n\n"+
			"This pad stays readable; it accepts no further posts.\n", newRef))

	upgraded := old.Header
	upgraded.ContinuedBy = newRef
	closed := append([]byte(pad.RenderHeader(upgraded)), afterFirstLine(oldData)...)
	if err := writeWholePad(oldFile, closed, closing); err != nil {
		// The successor exists and the old pad does not point at it. Say so rather than
		// reporting a generic write error: the pads are both intact and readable, and the
		// person who has to reconcile them needs both refs.
		return nil, fmt.Errorf("pad %s is full and %s was opened to continue it, but closing %s failed: %w",
			oldRef, newRef, oldRef, err)
	}
	return &continuation{Ref: newRef, Section: section, Task: task}, nil
}

// renderSuccessor builds the whole successor file: header, what carries over, and the post
// that could not fit. It returns the section number the post landed on and its task number.
//
// The order is what a person reads top to bottom: what binds you here (rules), what is
// still open (tasks), then the conversation resuming.
func (s *Store) renderSuccessor(
	oldRef string, old *Pad, full *Pad, req PostRequest, meta Meta, now time.Time,
) (body string, postSection int, postTask int) {
	var b strings.Builder
	b.WriteString(pad.RenderHeader(pad.Header{
		Created: now,
		// Owner, password and task numbering are the pad's identity rather than its
		// contents. Carried in the header because nothing in the new file's sections could
		// say them: its section 1 is written by whoever filled the old pad, which is not
		// the owner, and a task number it never issued leaves no trace at all.
		Opener:       old.Opener(),
		PasswordHash: old.PasswordHash(),
		Continues:    oldRef,
		TasksFrom:    highestTaskNo(full),
	}))
	b.WriteString("\n")

	n := 0
	add := func(author, title string, m pad.Meta, content string) int {
		n++
		b.WriteString(pad.RenderSection(n, author, title, now, m, content))
		return n
	}

	// The pad's own rules, restated by the tool. They are copied rather than inherited
	// because pad-level rules live IN the pad: leaving them behind would silently drop the
	// house style at the exact moment a conversation is most in need of one.
	if text, replace := padRulesOf(full); text != "" {
		add(pad.SystemAuthor, "House rules, carried over",
			pad.Meta{Kind: pad.KindRules, Replace: replace}, text)
	}

	// Work that is still open. Closed tasks are not carried: they are history, and history
	// stays in the pad that holds it. What must survive is the work somebody still owes.
	for _, t := range openTasks(full) {
		add(pad.SystemAuthor, t.Title,
			pad.Meta{Kind: pad.KindTask, Task: t.Task, Status: t.Status, To: taskOwners(t)},
			fmt.Sprintf("Carried over from %s §%d, still %s. Its history is in that pad.\n",
				oldRef, t.OpenedSection, t.Status))
	}

	postSection = add(req.Author, req.Title, meta, req.Content)
	return b.String(), postSection, meta.Task
}

// afterFirstLine returns everything below the header line, so a header can be replaced
// without touching a byte of what the pad says.
func afterFirstLine(data []byte) []byte {
	for i, c := range data {
		if c == '\n' {
			return data[i:] // keep the newline: it separates the new header from the body
		}
	}
	return nil
}

// highestTaskNo is the largest task number this pad may have issued, including the ones it
// inherited. A successor starts numbering above it.
func highestTaskNo(p *Pad) int {
	return p.NextTaskNo() - 1
}

// openTasks is the work a successor has to carry: anything not finished and not abandoned.
func openTasks(p *Pad) []pad.Task {
	var out []pad.Task
	for _, t := range p.Tasks() {
		switch t.Status {
		case pad.StatusDone, pad.StatusDropped:
			continue
		}
		out = append(out, t)
	}
	return out
}

// taskOwners flattens a task's owner states back into the `to` list that reopens it with
// the same people on the hook.
func taskOwners(t pad.Task) []string {
	out := make([]string, 0, len(t.Owners))
	for _, o := range t.Owners {
		out = append(out, o.Author)
	}
	return out
}

// padRulesOf returns the pad-level rules currently in force, and whether they cut the
// inheritance chain. Only the PAD level is copied: the store's and the project's apply to
// the successor already, since it lives in the same project.
//
// It asks internal/pad which section holds them rather than scanning for the last
// kind: rules — "which rules are in force" is a rule about pads, and there is one
// implementation of it (RulesSection), used by the rules view as well.
func padRulesOf(p *Pad) (text string, replace bool) {
	sec, _, ok := p.RulesSection()
	if !ok {
		return "", false
	}
	return strings.TrimRight(sec.Content, "\n"), sec.Replace
}

// writeNewPad allocates an id and writes a complete pad file, refusing to overwrite an
// existing one. It is the same allocation CreatePad does, kept in one place so a
// continuation cannot grow its own subtly different version of "a new pad".
func (s *Store) writeNewPad(project, body string) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		id, err := newPadID()
		if err != nil {
			return "", err
		}
		f, err := os.OpenFile(s.padPath(project, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // id collision — roll again
		}
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(s.padPath(project, id))
			return "", werr
		}
		return id, nil
	}
	return "", fmt.Errorf("could not allocate a unique pad id after 10 attempts")
}
