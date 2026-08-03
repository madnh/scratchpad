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
	b.WriteString(RenderHeader(now, "") + "\n")
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
	if err := CheckAck(p, "anyone", "", r); err != nil {
		t.Fatalf("a pad with no rules must never gate a post: %v", err)
	}
}

func TestCheckAck(t *testing.T) {
	p := buildPad(t, msg("pm", "kickoff"), rulesSection("pm", "- be brief", false))
	r := BuildRules("", false, "", "", false, p)

	err := CheckAck(p, "ios", "", r)
	if !HasCode(err, CodeRulesUnread) {
		t.Fatalf("a first post without an ack must be refused: %v", err)
	}
	// The refusal has to carry the rules AND the digest, or the retry needs a second
	// call to find out what to quote.
	if msg := err.Error(); !strings.Contains(msg, r.Digest) || !strings.Contains(msg, "be brief") {
		t.Fatalf("the error must carry the rules and the digest: %q", msg)
	}
	if err := CheckAck(p, "ios", r.Digest, r); err != nil {
		t.Fatalf("the right digest must pass: %v", err)
	}
	if err := CheckAck(p, "ios", strings.ToUpper(r.Digest), r); err != nil {
		t.Fatalf("the digest comparison must not be case-sensitive: %v", err)
	}
	if err := CheckAck(p, "ios", "deadbeef", r); !HasCode(err, CodeRulesUnread) {
		t.Fatalf("a wrong digest must be refused: %v", err)
	}
	// Someone already on the pad is never gated — the rules are read on the way IN, and
	// a mid-conversation gate is what would make agents route around them.
	if err := CheckAck(p, "pm", "", r); err != nil {
		t.Fatalf("an author who has posted here must not be gated: %v", err)
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
