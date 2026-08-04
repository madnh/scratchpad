package pad

import (
	"strings"
	"testing"
	"time"
)

// v1Pad is a pad file exactly as builds before the header change wrote one. It is a
// hard-coded STRING on purpose: rendering it with this package's own RenderHeader would
// test the new code against itself and pass no matter what happened to compatibility.
//
// It carries TWO sections by two different authors, and that is not decoration. With one
// section, "the first author" and "the last author" are the same string, so a migration
// that picked the wrong end of the pad would pass every assertion here. Verified by
// mutation: deriving the opener from the last section leaves this file's tests failing.
const v1Pad = "<!-- scratchpad v1; created: 2026-07-11T10:29:00Z -->\n" +
	"\n# 1 - frontend - How does API X work\n" +
	"<!-- ts: 2026-07-11T10:30:00Z -->\n" +
	"\nthe question\n" +
	"\n# 2 - backend - Answer\n" +
	"<!-- ts: 2026-07-11T10:40:00Z; to: frontend; re: 1 -->\n" +
	"\nthe answer\n"

const v1PadProtected = "<!-- scratchpad v1; created: 2026-07-11T10:29:00Z; password: $2b$12$abcdefghijklmnopqrstuv -->\n" +
	"\n# 1 - frontend - How does API X work\n" +
	"<!-- ts: 2026-07-11T10:30:00Z -->\n" +
	"\nthe question\n" +
	"\n# 2 - backend - Answer\n" +
	"<!-- ts: 2026-07-11T10:40:00Z; to: frontend; re: 1 -->\n" +
	"\nthe answer\n"

func TestV1FilesStillParse(t *testing.T) {
	for name, text := range map[string]string{"plain": v1Pad, "protected": v1PadProtected} {
		t.Run(name, func(t *testing.T) {
			p, err := Parse("default", "abc123", []byte(text))
			if err != nil {
				t.Fatalf("a v1 pad must still parse: %v", err)
			}
			if p.Header.Version != 1 {
				t.Errorf("version = %d, want 1", p.Header.Version)
			}
			if len(p.Sections) != 2 || p.Sections[0].Author != "frontend" || p.Sections[1].Author != "backend" {
				t.Fatalf("sections did not survive: %#v", p.Sections)
			}
			if p.CreatedTS() != time.Date(2026, 7, 11, 10, 29, 0, 0, time.UTC).Unix() {
				t.Errorf("created = %d", p.CreatedTS())
			}
		})
	}
	protected, err := Parse("default", "abc123", []byte(v1PadProtected))
	if err != nil {
		t.Fatal(err)
	}
	// The v1 parser cut the header at "; password: ", so this is the field most likely to
	// be quietly mangled by a keyed parser: check the hash arrives whole.
	if got := protected.PasswordHash(); got != "$2b$12$abcdefghijklmnopqrstuv" {
		t.Errorf("password hash = %q, want it intact", got)
	}
}

// TestV1PadHasNoOpenerUntilUpgraded pins the deliberate gap: a v1 file records no owner,
// and Opener() does NOT fall back to section 1. Upgrade is what fills it in.
func TestV1PadHasNoOpenerUntilUpgraded(t *testing.T) {
	p, err := Parse("default", "abc123", []byte(v1Pad))
	if err != nil {
		t.Fatal(err)
	}
	if p.Opener() != "" {
		t.Errorf("a v1 pad reports opener %q; it should report none until upgraded", p.Opener())
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	want := Header{
		Version:      FileVersion,
		Created:      time.Date(2026, 8, 4, 9, 30, 0, 0, time.UTC),
		PasswordHash: "$2b$12$abcdefghijklmnopqrstuv",
		Opener:       "design",
		Continues:    "default-ab3k9x",
	}
	got, err := ParseHeader([]byte(RenderHeader(want)))
	if err != nil {
		t.Fatalf("a header this package rendered must parse: %v", err)
	}
	if got != want {
		t.Errorf("round trip lost something:\n got %#v\nwant %#v", got, want)
	}
}

// TestHeaderOmitsEmptyFields keeps an unprotected original pad's header from growing
// empty keys — the file is read by people, and "password: " with nothing after it invites
// exactly the wrong conclusion.
func TestHeaderOmitsEmptyFields(t *testing.T) {
	line := RenderHeader(Header{Created: time.Now().UTC(), Opener: "design"})
	for _, absent := range []string{"password", "continues"} {
		if strings.Contains(line, absent) {
			t.Errorf("header names %q with nothing to say: %s", absent, line)
		}
	}
}

// TestFutureVersionIsRefusedNotGuessed is the property v1 did not have. v1's parser
// matched a literal prefix, so a later format was "not a scratchpad file" — indistinguishable
// from a random text file, and no path to an upgrade message.
func TestFutureVersionIsRefusedNotGuessed(t *testing.T) {
	future := "<!-- scratchpad v99; created: 2026-07-11T10:29:00Z; opener: x -->\n" +
		"\n# 1 - a - t\n<!-- ts: 2026-07-11T10:30:00Z -->\n\nbody\n"
	_, err := Parse("default", "abc123", []byte(future))
	if err == nil {
		t.Fatal("a newer file format must be refused, not parsed on a guess")
	}
	if !strings.Contains(err.Error(), "99") || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("the refusal must name the version and say what to do; got: %v", err)
	}
}

func TestNonPadFilesAreRefused(t *testing.T) {
	for name, text := range map[string]string{
		"empty":          "",
		"prose":          "just some notes\nnothing to do with pads\n",
		"no version":     "<!-- scratchpad vX; created: 2026-07-11T10:29:00Z -->\n\n# 1 - a - t\n",
		"no created":     "<!-- scratchpad v2; opener: a -->\n\n# 1 - a - t\n",
		"bad created":    "<!-- scratchpad v2; created: yesterday -->\n\n# 1 - a - t\n",
		"header only":    "<!-- scratchpad v2; created: 2026-07-11T10:29:00Z; opener: a -->\n",
		"unclosed comme": "<!-- scratchpad v2; created: 2026-07-11T10:29:00Z\n\n# 1 - a - t\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse("default", "abc123", []byte(text)); err == nil {
				t.Errorf("parsed something that is not a pad: %q", text)
			}
		})
	}
}

// TestUnknownHeaderKeysSurvive: a key this build does not model must not be a reason to
// refuse the file, and must not be silently absorbed into a neighbouring field the way v1
// absorbed everything after "; password: ".
func TestUnknownHeaderKeysSurvive(t *testing.T) {
	text := "<!-- scratchpad v2; created: 2026-07-11T10:29:00Z; opener: design; note: hand-edited; password: $2b$12$x -->\n" +
		"\n# 1 - design - t\n<!-- ts: 2026-07-11T10:30:00Z -->\n\nbody\n"
	p, err := Parse("default", "abc123", []byte(text))
	if err != nil {
		t.Fatalf("an unmodelled key must not make the file unreadable: %v", err)
	}
	if p.Opener() != "design" {
		t.Errorf("opener = %q, want design", p.Opener())
	}
	if p.PasswordHash() != "$2b$12$x" {
		t.Errorf("password hash = %q — a neighbouring key bled into it", p.PasswordHash())
	}
}
