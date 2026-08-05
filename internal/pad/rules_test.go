package pad

import (
	"strings"
	"testing"
	"time"
)

// buildPad assembles a pad from (author, kind, body) triples, through the SAME renderer
// the store writes with — so a change to the metadata line breaks these tests in the
// same way it would break a real pad.
func buildPad(t *testing.T, secs ...Section) *Pad {
	t.Helper()
	now := time.Now().UTC()
	var b strings.Builder
	b.WriteString(RenderHeader(Header{Created: now, Opener: secs[0].Author}) + "\n")
	for i, sec := range secs {
		title := sec.Title
		if title == "" {
			title = "t"
		}
		b.WriteString(RenderSection(i+1, sec.Author, title, now, sec.Meta, sec.Content))
	}
	p, err := Parse("proj", "abc123", []byte(b.String()))
	if err != nil {
		t.Fatalf("the pad this test built does not parse: %v", err)
	}
	return p
}

func rulesSection(author, body string, replace bool) Section {
	return Section{Author: author, Content: body, Meta: Meta{Kind: KindRules, Replace: replace}}
}

func msg(author, body string) Section {
	return Section{Author: author, Content: body, Meta: Meta{Kind: KindMessage}}
}

// ackedMsg is a message carrying the receipt a real post would have left: the digest its
// author had read. It goes through RenderSection like every other fixture here, so these
// tests also pin that `acked` survives the round trip through the file format.
func ackedMsg(author, body, acked string) Section {
	return Section{Author: author, Content: body, Meta: Meta{Kind: KindMessage, Acked: acked}}
}

// A rules section must survive a round trip through the on-disk format — including
// `replace`, which is a key an older binary is expected to ignore rather than choke on.
func TestRulesSectionRoundTrip(t *testing.T) {
	p := buildPad(t,
		msg("pm", "kickoff"),
		rulesSection("pm", "- be brief", true),
	)
	sec, history, ok := p.RulesSection()
	switch {
	case !ok:
		t.Fatal("the rules section did not parse back")
	case sec.Kind != KindRules:
		t.Fatalf("kind = %q, want rules", sec.Kind)
	case !sec.Replace:
		t.Fatal("replace was lost in the round trip")
	case strings.TrimSpace(sec.Content) != "- be brief":
		t.Fatalf("body = %q", sec.Content)
	case len(history) != 0:
		t.Fatalf("one rules section is not a history: %v", history)
	}
}

// Several rules sections are VERSIONS of one rule set: the last is in force and the
// earlier ones are history. Getting this backwards would show a pad's oldest rules as
// current, which is worse than showing none.
func TestRulesLatestWins(t *testing.T) {
	p := buildPad(t,
		msg("pm", "kickoff"),
		rulesSection("pm", "- first", false),
		msg("ios", "hello"),
		rulesSection("pm", "- second", false),
	)
	sec, history, ok := p.RulesSection()
	if !ok || strings.TrimSpace(sec.Content) != "- second" {
		t.Fatalf("in force = %q, want the LAST rules section", sec.Content)
	}
	if len(history) != 1 || history[0] != 2 {
		t.Fatalf("history = %v, want [2]", history)
	}
}

// The bug a new `kind` walks straight into: turn state filtered with `!= task` would let
// a rules section hold the turn, so the agent that wrote it could not post next.
func TestRulesDoNotTakeTheTurn(t *testing.T) {
	p := buildPad(t,
		msg("pm", "kickoff"),
		msg("ios", "hello"),
		rulesSection("pm", "- be brief", false),
	)
	if last, ok := p.LastMessage(); !ok || last.Author != "ios" {
		t.Fatalf("turn holder = %+v, want the last MESSAGE (ios)", last)
	}
	if err := p.CheckTurn("pm", KindMessage); err != nil {
		t.Fatalf("pm must still be allowed to reply to ios: %v", err)
	}
	if err := p.CheckTurn("ios", KindMessage); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("ios posted the last message; want not_your_turn, got %v", err)
	}
	// And writing rules is never blocked by whose turn it is.
	if err := p.CheckTurn("ios", KindRules); err != nil {
		t.Fatalf("a rules section must be exempt from the turn rule: %v", err)
	}
}

func TestBuildRulesLayersAndReplace(t *testing.T) {
	p := buildPad(t, msg("pm", "kickoff"), rulesSection("pm", "- pad rule", false))

	r := BuildRules("- store rule", false, "proj", "- project rule", false, p)
	if len(r.Layers) != 3 {
		t.Fatalf("want three layers, got %d", len(r.Layers))
	}
	if want := "- store rule\n\n- project rule\n\n- pad rule"; r.Text != want {
		t.Fatalf("combined text =\n%q\nwant\n%q", r.Text, want)
	}
	if r.Digest == "" || len(r.Digest) != DigestLen {
		t.Fatalf("digest = %q, want %d hex characters", r.Digest, DigestLen)
	}

	// Same inputs, same digest: an agent that re-reads the rules must not be told they
	// changed.
	if again := BuildRules("- store rule", false, "proj", "- project rule", false, p); again.Digest != r.Digest {
		t.Fatalf("digest is not stable: %q vs %q", r.Digest, again.Digest)
	}

	// A pad-level `replace` cuts the levels above — but they stay in Layers, marked, so
	// a reader can see a store rule EXISTS and is currently not in force.
	pr := buildPad(t, msg("pm", "kickoff"), rulesSection("pm", "- pad only", true))
	cut := BuildRules("- store rule", false, "proj", "- project rule", false, pr)
	if cut.Text != "- pad only" {
		t.Fatalf("replace did not cut the chain: %q", cut.Text)
	}
	if len(cut.Layers) != 3 || !cut.Layers[0].Superseded || !cut.Layers[1].Superseded {
		t.Fatalf("superseded levels must be kept and marked: %+v", cut.Layers)
	}
	if cut.Digest == r.Digest {
		t.Fatal("different effective rules must not share a digest")
	}
}

// No rules anywhere must mean no gate at all — otherwise every existing store would
// start refusing posts the day it upgrades.
func TestBuildRulesEmpty(t *testing.T) {
	p := buildPad(t, msg("pm", "kickoff"))
	r := BuildRules("", false, "proj", "   \n", false, p)
	if !r.Empty() || len(r.Layers) != 0 {
		t.Fatalf("blank rules must read as none: %+v", r)
	}
	for _, policy := range []ReackPolicy{ReackOnce, ReackOnChange} {
		if err := CheckAck(p, "anyone", "", r, policy); err != nil {
			t.Fatalf("a pad with no rules must never gate a post (%s): %v", policy, err)
		}
	}
}

func TestCheckAck(t *testing.T) {
	p := buildPad(t, msg("pm", "kickoff"), rulesSection("pm", "- be brief", false))
	r := BuildRules("", false, "", "", false, p)

	err := CheckAck(p, "ios", "", r, ReackOnce)
	if !HasCode(err, CodeRulesUnread) {
		t.Fatalf("a first post without an ack must be refused: %v", err)
	}
	// The refusal has to carry the rules AND the digest, or the retry needs a second
	// call to find out what to quote.
	if msg := err.Error(); !strings.Contains(msg, r.Digest) || !strings.Contains(msg, "be brief") {
		t.Fatalf("the error must carry the rules and the digest: %q", msg)
	}
	if err := CheckAck(p, "ios", r.Digest, r, ReackOnce); err != nil {
		t.Fatalf("the right digest must pass: %v", err)
	}
	if err := CheckAck(p, "ios", strings.ToUpper(r.Digest), r, ReackOnce); err != nil {
		t.Fatalf("the digest comparison must not be case-sensitive: %v", err)
	}
	if err := CheckAck(p, "ios", "deadbeef", r, ReackOnce); !HasCode(err, CodeRulesUnread) {
		t.Fatalf("a wrong digest must be refused: %v", err)
	}
	// Under `once` someone already on the pad is never gated again: the rules are read on
	// the way IN, and that is the whole of it.
	if err := CheckAck(p, "pm", "", r, ReackOnce); err != nil {
		t.Fatalf("an author who has posted here must not be gated: %v", err)
	}
}

// TestCheckAckOnChange is the difference the two policies make, on the one author `once`
// waves through: somebody who is already on the pad, whose rules have moved since.
func TestCheckAckOnChange(t *testing.T) {
	// pm opened the pad and has a receipt — for rules that no longer say what they said.
	stale := "0000dead"
	p := buildPad(t,
		ackedMsg("pm", "kickoff", stale),
		rulesSection("pm", "- be brief", false))
	r := BuildRules("", false, "", "", false, p)
	if r.Digest == stale {
		t.Fatal("the fixture needs a receipt that does NOT match the rules in force")
	}

	err := CheckAck(p, "pm", "", r, ReackOnChange)
	if !HasCode(err, CodeRulesUnread) {
		t.Fatalf("a stale receipt must be refused under on-change: %v", err)
	}
	// The refusal must not claim pm is new here. It is the sentence a returning agent
	// reads, and "you have not posted here before" sends it looking for the wrong bug.
	if msg := err.Error(); !strings.Contains(msg, "CHANGED") {
		t.Fatalf("the refusal must say what actually happened: %q", msg)
	}
	// Quoting the current digest passes, and so does a receipt that already names it.
	if err := CheckAck(p, "pm", r.Digest, r, ReackOnChange); err != nil {
		t.Fatalf("quoting the digest in force must pass: %v", err)
	}
	fresh := buildPad(t, ackedMsg("pm", "kickoff", r.Digest), rulesSection("pm", "- be brief", false))
	if err := CheckAck(fresh, "pm", "", r, ReackOnChange); err != nil {
		t.Fatalf("a receipt for the version in force must pass: %v", err)
	}
	// And the same pad under `once` is not gated at all — the policy is the only thing
	// that differs between these two outcomes.
	if err := CheckAck(p, "pm", "", r, ReackOnce); err != nil {
		t.Fatalf("`once` must ignore a stale receipt: %v", err)
	}
}

func TestSystemAuthorIsReserved(t *testing.T) {
	// Case and stray whitespace do not get around the reservation.
	for _, name := range []string{SystemAuthor, "Scratchpad", "  scratchpad  "} {
		if err := ValidateAuthor(name); !HasCode(err, CodeInvalidInput) {
			t.Errorf("ValidateAuthor(%q) must refuse the reserved name, got %v", name, err)
		}
	}
	if err := ValidateAuthorAllowSystem(SystemAuthor); err != nil {
		t.Errorf("the rules-writing path must accept the reserved name: %v", err)
	}
	if err := ValidateAuthorAllowSystem("  scratchpad  "); err == nil {
		t.Error("even the privileged path keeps the ordinary format rules")
	}
	// Reading an existing pad is never affected: only writes are gated.
	p := buildPad(t, msg("pm", "kickoff"), rulesSection(SystemAuthor, "- set by a person", false))
	if got := p.Authors(); len(got) != 1 || got[0] != "pm" {
		t.Fatalf("the roster must not list the tool as a teammate: %v", got)
	}
	if parts := p.Participants(); len(parts) != 1 || parts[0].Author != "pm" {
		t.Fatalf("participants must not list the tool: %+v", parts)
	}
}

func TestPadFileNames(t *testing.T) {
	cases := map[string]bool{
		"ab3k9x.md":  true,
		"abc123.md":  true,
		"_rules.md":  false, // the tool's own file
		"rules.md":   true,  // a legal pad id — which is exactly why the tool's files take a prefix
		"README.md":  false,
		"notes.txt":  false,
		".hidden.md": false,
		"ab3k9x":     false,
	}
	for name, want := range cases {
		if got := IsPadFileName(name); got != want {
			t.Errorf("IsPadFileName(%q) = %t, want %t", name, got, want)
		}
	}
	if !IsToolFileName(RulesFileName) {
		t.Errorf("%q must be recognised as the tool's own file", RulesFileName)
	}
	if IsToolFileName("ab3k9x.md") {
		t.Error("a pad must never be mistaken for a tool file")
	}
}

func TestRulesFileMarkerRoundTrip(t *testing.T) {
	text, replace := ParseRulesFile([]byte(RenderRulesFile("- be brief", true)))
	if text != "- be brief" || !replace {
		t.Fatalf("round trip lost something: %q, replace=%t", text, replace)
	}
	if got := RenderRulesFile("   \n\n", false); got != "" {
		t.Fatalf("blank rules must render as an empty file, got %q", got)
	}
	// A file with only the marker carries no rules: "none" has one representation.
	if text, _ := ParseRulesFile([]byte("<!-- rules: replace -->\n")); text != "" {
		t.Fatalf("marker-only file should carry no text, got %q", text)
	}
}

// A rules section is not about a task and not addressed at anyone: the rules reach
// everyone by being the rules.
func TestRulesMetaValidation(t *testing.T) {
	cases := []Meta{
		{Kind: KindRules, To: []string{"ios"}},
		{Kind: KindRules, Task: 3},
		{Kind: KindMessage, Replace: true},
	}
	for _, m := range cases {
		if err := ValidateMeta(m); err == nil {
			t.Errorf("ValidateMeta(%+v) should have been refused", m)
		}
	}
	if err := ValidateMeta(Meta{Kind: KindRules, Replace: true}); err != nil {
		t.Errorf("a plain rules section must validate: %v", err)
	}
}

// A level's version is what a writer quotes to say which text it is replacing. Every
// level that APPLIES has one, empty levels included — filling an empty level is exactly
// the write two agents are most likely to race on.
func TestLevelVersions(t *testing.T) {
	r := BuildRules("- be brief", false, "proj", "", false, nil)
	if got := r.Version(LevelProject); got != NoRules {
		t.Fatalf("an empty project level is %q, want %q", got, NoRules)
	}
	if r.Version(LevelStore) == NoRules || r.Version(LevelStore) == "" {
		t.Fatalf("a written level must have a digest: %q", r.Version(LevelStore))
	}
	// A level that does not apply here has no version at all, which is not the same as
	// having none: there is no pad to write rules on.
	if got := r.Version(LevelPad); got != "" {
		t.Fatalf("a project view has no pad version, got %q", got)
	}

	// The versions are per-level, so a change at one level leaves the others where they
	// were — the whole reason they are not the combined digest.
	r2 := BuildRules("- be brief", false, "proj", "- always --to", false, nil)
	if r2.Version(LevelStore) != r.Version(LevelStore) {
		t.Fatal("writing the project's rules must not move the store's version")
	}
	if r2.Digest == r.Digest {
		t.Fatal("the combined digest, by contrast, must move when any level changes")
	}

	// `replace` is part of what a level SAYS, so flipping it alone moves the version.
	if LevelDigest("- be brief", true) == LevelDigest("- be brief", false) {
		t.Fatal("replace must be part of a level's version")
	}
}

func TestCheckVersion(t *testing.T) {
	cur := LevelDigest("- be brief", false)
	if err := CheckVersion(LevelStore, cur, cur, "- be brief"); err != nil {
		t.Fatalf("the current version must be accepted: %v", err)
	}
	if err := CheckVersion(LevelStore, "  "+strings.ToUpper(cur)+"\n", cur, "- be brief"); err != nil {
		t.Fatalf("a digest is a token, not a byte comparison: %v", err)
	}
	// Blank and stale get the same code — the remedy is the same — but not the same
	// sentence, because they are not the same mistake.
	blank := CheckVersion(LevelStore, "", cur, "- be brief")
	stale := CheckVersion(LevelStore, "deadbeef", cur, "- be brief")
	if !HasCode(blank, CodeRulesConflict) || !HasCode(stale, CodeRulesConflict) {
		t.Fatalf("want rules_conflict for both: %v / %v", blank, stale)
	}
	if blank.Error() == stale.Error() {
		t.Fatal("forgetting the version and quoting a stale one must read differently")
	}
	// Both carry the version that won, so the retry needs no extra read.
	for _, err := range []error{blank, stale} {
		if !strings.Contains(err.Error(), "- be brief") || !strings.Contains(err.Error(), cur) {
			t.Fatalf("a conflict must carry the current rules and digest: %v", err)
		}
	}
	// An empty level has nothing to merge with, so it says so rather than trailing off.
	empty := CheckVersion(LevelPad, "deadbeef", NoRules, "")
	if !strings.Contains(empty.Error(), "empty") {
		t.Fatalf("a conflict on an empty level: %v", empty)
	}
}
