package store

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

// The announcement is what makes a rules change ARRIVE rather than merely bind. This is
// the whole of it end to end: a person edits the store's rules, ticks the box, and every
// live pad gains a section that wakes whoever is parked on it.
func TestNotifyRulesChangedReachesLivePads(t *testing.T) {
	s := testStore(t)
	setRules(t, s, "", "- be brief", false)
	rules, err := s.ProjectRuleSet("proj")
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.CreatePad(CreateRequest{
		Project: "proj", Author: "pm", Title: "kickoff", Content: "starting", AckRules: rules.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second project, to pin that the STORE level reaches across projects.
	other, _, err := s.CreatePad(CreateRequest{
		Project: "ops", Author: "pm", Title: "elsewhere", Content: "starting", AckRules: rules.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.NotifyRulesChanged(NotifyScope{Level: pad.LevelStore})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notified) != 2 {
		t.Fatalf("both live pads should have been notified, got %v (failed: %v)", res.Notified, res.Failed)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("nothing should have failed: %v", res.Failed)
	}

	got, err := s.Get(p.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	last := got.Last()
	if last.Kind != pad.KindNotice {
		t.Fatalf("the announcement must be a notice, got kind %q", last.Kind)
	}
	if last.Author != pad.SystemAuthor {
		t.Fatalf("a notice is the tool speaking, not an agent: %q", last.Author)
	}
	// It must not take the turn. pm wrote the last MESSAGE and is still the one blocked —
	// if the notice had taken the turn, pm would suddenly be free to post twice running.
	if turn := got.TurnState(); turn.LastAuthor != "pm" || len(turn.Blocked) != 1 || turn.Blocked[0] != "pm" {
		t.Fatalf("a notice must not touch the turn: %+v", turn)
	}
	// And it says what to do and what happens if they do not — this store re-asks, so the
	// notice must say so rather than leaving each agent to guess which deployment it is on.
	if !strings.Contains(last.Content, "read them now") {
		t.Fatalf("the notice must tell an agent what to do: %q", last.Content)
	}
	if !strings.Contains(last.Content, "refused until it quotes the new digest") {
		t.Fatalf("the notice must name the consequence this deployment actually applies: %q", last.Content)
	}

	// The project level is narrower, and the count says so.
	res, err = s.NotifyRulesChanged(NotifyScope{Level: pad.LevelProject, Project: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notified) != 1 || res.Notified[0] != other.Ref() {
		t.Fatalf("a project's rules must reach only that project's pads: %v", res.Notified)
	}
}

// What the box is ticked ON is a number a person is shown first, so the filters have to be
// the same ones the announcement applies — and every pad left out has to be countable.
func TestNotifyTargetsSkipsAndSaysSo(t *testing.T) {
	limits := config.DefaultLimits
	limits.MaxSectionsPerPad = 2
	limits.OnFull = config.OnFullContinue
	s := testStorePolicy(t, limits, config.RulesPolicy{
		Store: config.RulesWriteAgent, Project: config.RulesWriteAgent, Pad: config.RulesWriteAny,
		Reack: config.ReackOnChange, NotifyActiveDays: 7,
	})

	live, _, err := create(s, "proj", "pm", "live one", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	protectedPad, _, err := create(s, "proj", "pm", "protected", "x", true)
	if err != nil {
		t.Fatal(err)
	}
	// A pad that fills and continues: the old end is closed, the new one is live.
	full, _, err := create(s, "proj", "pm", "will fill", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: full.Ref(), Author: "ios", Title: "two", Content: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: full.Ref(), Author: "pm", Title: "three", Content: "c"}); err != nil {
		t.Fatal(err)
	}

	targets, err := s.RulesNotifyTargets(NotifyScope{Level: pad.LevelStore})
	if err != nil {
		t.Fatal(err)
	}
	// A protected pad IS a target. The password keeps other agents out of it; it was never
	// a reason to leave the pad out of an announcement, since the rules bind it like every
	// other pad and its agents are refused by the read gate like every other pad's.
	if !slices.Contains(targets.Refs, protectedPad.Ref()) {
		t.Fatalf("a protected pad must still be told: %v", targets.Refs)
	}
	if targets.Skipped[SkipContinued] != 1 {
		t.Fatalf("a continued pad accepts no posts and must be skipped: %+v", targets)
	}
	// in_scope is the denominator a person reads the count against, so it counts the pads
	// that were skipped too.
	if targets.InScope != 4 {
		t.Fatalf("in_scope should count every pad the level binds, got %d (%+v)", targets.InScope, targets)
	}
	for _, ref := range targets.Refs {
		if ref == live.Ref() {
			return
		}
	}
	t.Fatalf("the live pad should be a target: %v", targets.Refs)
}

// TestNotifyReachesProtectedPads is the one boundary worth stating twice: a password keeps
// other AGENTS out of a pad, and is not a reason for the store to leave it uninformed.
//
// The rules bind a protected pad exactly as they bind every other, and its agents are
// refused by the read gate exactly as everyone else's are. Skipping it would make it the
// one pad that is blocked without ever being told why — and the notice says nothing beyond
// "the rules changed", which every agent on this store may read anyway.
func TestNotifyReachesProtectedPads(t *testing.T) {
	s := testStore(t)
	p, password, err := create(s, "proj", "pm", "secret work", "x", true)
	if err != nil {
		t.Fatal(err)
	}
	if password == "" {
		t.Fatal("the fixture did not actually protect the pad")
	}

	res, err := s.NotifyRulesChanged(NotifyScope{Level: pad.LevelStore})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Notified) != 1 || res.Notified[0] != p.Ref() {
		t.Fatalf("the protected pad should have been told: notified=%v failed=%v", res.Notified, res.Failed)
	}

	// It landed, and the password still governs everything else: reading it back needs one.
	if _, err := s.Get(p.Ref(), ""); err == nil {
		t.Fatal("announcing into a protected pad must not have opened it up")
	}
	got, err := s.Get(p.Ref(), password)
	if err != nil {
		t.Fatal(err)
	}
	if last := got.Last(); last.Kind != pad.KindNotice || last.Author != pad.SystemAuthor {
		t.Fatalf("want the notice as the last section, got %+v", last.Meta)
	}
	// And an ordinary post still cannot get in without the password — the bypass belongs
	// to the announcement path and to nothing else.
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "ios", Title: "sneaking in", Content: "no password here",
	}); err == nil {
		t.Fatal("the password gate must still refuse an ordinary post")
	}
}

// A pad nobody has touched in longer than the window has no agent parked on it to wake,
// and writing into every pad the store has ever held is how one edit costs hundreds of
// sections.
func TestNotifySkipsQuietPads(t *testing.T) {
	s := testStore(t)
	quiet, _, err := create(s, "proj", "pm", "old news", "x", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := create(s, "proj", "pm", "current", "x", false); err != nil {
		t.Fatal(err)
	}
	backdate(t, s, quiet.Ref(), 30*24*time.Hour)

	targets, err := s.RulesNotifyTargets(NotifyScope{Level: pad.LevelStore})
	if err != nil {
		t.Fatal(err)
	}
	if targets.Skipped[SkipQuiet] != 1 {
		t.Fatalf("the stale pad must be skipped as quiet: %+v", targets)
	}
	if len(targets.Refs) != 1 || targets.Refs[0] == quiet.Ref() {
		t.Fatalf("only the current pad should be a target: %v", targets.Refs)
	}
	if targets.ActiveDays != config.DefaultRulesPolicy.NotifyActiveDays {
		t.Fatalf("the window must be reported so the number can be read: %+v", targets)
	}
}

// The pad level has no announcement to make: its rules ARE a section, and every waiter on
// that pad wakes for it. Saying so beats announcing nothing, which would leave the caller
// believing a notice went out.
func TestNotifyRefusesThePadLevel(t *testing.T) {
	s := testStore(t)
	if _, err := s.RulesNotifyTargets(NotifyScope{Level: pad.LevelPad}); !HasCode(err, CodeInvalidInput) {
		t.Fatalf("announcing a pad's own rules: want invalid_input, got %v", err)
	}
}

// backdate rewinds a pad's last-activity time by rewriting the timestamp on its sections.
// The quiet filter reads what the FILE says, so this has to move the file rather than a
// clock the store does not consult.
func backdate(t *testing.T, s *Store, ref string, by time.Duration) {
	t.Helper()
	project, id, err := ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	path := s.padPath(project, id)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	then := time.Now().Add(-by).UTC().Format(time.RFC3339)
	out := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		// The timestamp runs from the prefix to whichever comes first: the separator before
		// another field, or the space before the closing marker. Neither character appears
		// in RFC3339, so this leaves any other metadata on the line exactly as it was.
		if rest, ok := strings.CutPrefix(line, "<!-- ts: "); ok {
			if end := strings.IndexAny(rest, "; "); end >= 0 {
				line = "<!-- ts: " + then + rest[end:]
			}
		}
		out = append(out, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}
