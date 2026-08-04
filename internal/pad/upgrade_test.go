package pad

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestUpgradeFillsOpenerFromSectionOne(t *testing.T) {
	out, changed, err := Upgrade("default", "abc123", []byte(v1Pad))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a v1 file must report as changed")
	}
	p, err := Parse("default", "abc123", out)
	if err != nil {
		t.Fatalf("the upgraded file must parse: %v", err)
	}
	if p.Header.Version != FileVersion {
		t.Errorf("version = %d, want %d", p.Header.Version, FileVersion)
	}
	if p.Opener() != "frontend" {
		t.Errorf("opener = %q, want frontend — section 1's author", p.Opener())
	}
}

// TestUpgradeAnswersWhatV1AnsweredFor checks the migration against the rule it replaces:
// for the same bytes, the new header must say what the old derivation said. If these ever
// disagree, an upgrade silently changes who owns a pad.
func TestUpgradeAnswersWhatV1AnsweredFor(t *testing.T) {
	before, err := Parse("default", "abc123", []byte(v1Pad))
	if err != nil {
		t.Fatal(err)
	}
	v1Answer := before.Sections[0].Author // what Opener() returned before the header existed

	out, _, err := Upgrade("default", "abc123", []byte(v1Pad))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse("default", "abc123", out)
	if err != nil {
		t.Fatal(err)
	}
	if after.Opener() != v1Answer {
		t.Errorf("upgrade changed the owner: %q -> %q", v1Answer, after.Opener())
	}
}

// TestUpgradeTouchesOnlyTheFirstLine is the property that makes an automatic rewrite
// acceptable at all: whatever else is wrong, an upgrade cannot alter what the pad SAYS.
func TestUpgradeTouchesOnlyTheFirstLine(t *testing.T) {
	for name, text := range map[string]string{"plain": v1Pad, "protected": v1PadProtected} {
		t.Run(name, func(t *testing.T) {
			out, _, err := Upgrade("default", "abc123", []byte(text))
			if err != nil {
				t.Fatal(err)
			}
			_, wantRest := splitLine([]byte(text))
			_, gotRest := splitLine(out)
			if !bytes.Equal(gotRest, wantRest) {
				t.Errorf("upgrade rewrote the body:\n got %q\nwant %q", gotRest, wantRest)
			}
		})
	}
}

func TestUpgradeKeepsThePassword(t *testing.T) {
	out, _, err := Upgrade("default", "abc123", []byte(v1PadProtected))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse("default", "abc123", out)
	if err != nil {
		t.Fatal(err)
	}
	if p.PasswordHash() != "$2b$12$abcdefghijklmnopqrstuv" {
		t.Fatalf("password hash lost or mangled: %q", p.PasswordHash())
	}
	if !p.Protected() {
		t.Error("an upgraded protected pad must stay protected — losing this unlocks it")
	}
}

// TestUpgradeIsIdempotent matters because the store will call this on every write. A second
// call must be free and must not touch the file, or every post rewrites the whole pad.
func TestUpgradeIsIdempotent(t *testing.T) {
	once, _, err := Upgrade("default", "abc123", []byte(v1Pad))
	if err != nil {
		t.Fatal(err)
	}
	twice, changed, err := Upgrade("default", "abc123", once)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("upgrading an already-current file reports a change")
	}
	if !bytes.Equal(once, twice) {
		t.Error("a second upgrade altered the file")
	}
}

// TestUpgradeDoesNotInventAnOpener: a v2 file that somehow has no opener (hand-edited) gets
// one derived the same way, rather than being left ownerless or refused.
func TestUpgradeFillsAMissingOpenerOnACurrentFile(t *testing.T) {
	text := "<!-- scratchpad v2; created: 2026-07-11T10:29:00Z -->\n" +
		"\n# 1 - handwritten - t\n<!-- ts: 2026-07-11T10:30:00Z -->\n\nbody\n"
	out, changed, err := Upgrade("default", "abc123", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a file with no opener must be completed")
	}
	p, err := Parse("default", "abc123", out)
	if err != nil {
		t.Fatal(err)
	}
	if p.Opener() != "handwritten" {
		t.Errorf("opener = %q", p.Opener())
	}
}

// TestUpgradeKeepsAnExistingOpener is the case a continued pad depends on: its opener is
// NOT section 1's author, and an upgrade must never "correct" it to one.
func TestUpgradeKeepsAnExistingOpener(t *testing.T) {
	text := RenderHeader(Header{
		Created: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
		Opener:  "pm",
	}) + "\n" + RenderSection(1, "worker", "t", time.Now().UTC(), Meta{Kind: KindMessage}, "body")

	out, changed, err := Upgrade("default", "abc123", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a complete file was rewritten")
	}
	p, err := Parse("default", "abc123", out)
	if err != nil {
		t.Fatal(err)
	}
	if p.Opener() != "pm" {
		t.Errorf("opener = %q, want pm — section 1's author must not win over the header", p.Opener())
	}
}

func TestUpgradeRefusesWhatIsNotAPad(t *testing.T) {
	if _, _, err := Upgrade("default", "abc123", []byte("not a pad\n")); err == nil {
		t.Fatal("upgrading a non-pad must fail rather than write a header onto it")
	}
	future := "<!-- scratchpad v99; created: 2026-07-11T10:29:00Z -->\n\n# 1 - a - t\n"
	_, _, err := Upgrade("default", "abc123", []byte(future))
	if err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("upgrading a NEWER file must refuse, not downgrade it; got %v", err)
	}
}
