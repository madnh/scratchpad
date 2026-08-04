package pad

import (
	"strings"
	"testing"
	"time"
)

// renderPadFile renders a pad the way the store does, so these tests exercise the real
// on-disk shape rather than a hand-written approximation of it.
func renderPadFile(t *testing.T, bodies ...string) string {
	t.Helper()
	ts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var b strings.Builder
	b.WriteString(RenderHeader(Header{Created: ts, Opener: "alpha"}) + "\n")
	for i, body := range bodies {
		b.WriteString(RenderSection(i+1, "alpha", "title", ts, Meta{Kind: KindMessage}, body))
	}
	return b.String()
}

// Whatever a section was written with must come back out of it. This is the invariant the
// streaming rewrite had to preserve, and the one that a mistake in the line reader — an
// off-by-one on the newline, a lost final empty line — breaks first.
func TestBodyRoundTripsThroughTheParser(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"one line", "hello\n"},
		{"no trailing newline", "hello"},
		{"blank line inside", "para one\n\npara two\n"},
		{"leading blank line", "\nstarts blank\n"},
		{"trailing blanks", "text\n\n\n"},
		{"only a blank line", "\n"},
		{"hash line that is not a header", "# not a section header\n"},
		{"looks like a header but has no author split", "# 12 nosplit\n"},
		{"unicode", "cần hiểu rõ — ổn\n"},
		{"a line longer than the read buffer", strings.Repeat("x", 200*1024) + "\n"},
		{"long line, no trailing newline", strings.Repeat("y", 200*1024)},
		{"many long lines", strings.Repeat(strings.Repeat("z", 70*1024)+"\n", 3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := renderPadFile(t, tc.body)
			p, err := Parse("proj", "abc123", []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Sections) != 1 {
				t.Fatalf("sections = %d, want 1", len(p.Sections))
			}
			// The format normalises trailing blank lines to exactly one newline — they are
			// the separator before the next section, so they cannot be part of a body.
			// Verified against the pre-streaming parser: same input, same output.
			want := strings.TrimRight(tc.body, "\n")
			if want != "" {
				want += "\n"
			}
			if got := p.Sections[0].Content; got != want {
				t.Errorf("content mismatch\n got %q\nwant %q", got, want)
			}
		})
	}
}

// Metadata-only parsing must see exactly the same sections as a full parse: same count,
// same numbers, same authors, same metadata. Only the bodies differ.
func TestParseMetaAgreesWithParseExceptBodies(t *testing.T) {
	raw := renderPadFile(t,
		"first\n",
		"second\n\nwith a blank\n",
		strings.Repeat("w", 100*1024)+"\n",
		"# 99 - looks - like a header\n",
	)
	full, err := Parse("proj", "abc123", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseMeta("proj", "abc123", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Sections) != len(meta.Sections) {
		t.Fatalf("sections: full %d, meta %d", len(full.Sections), len(meta.Sections))
	}
	for i := range full.Sections {
		f, m := full.Sections[i], meta.Sections[i]
		if f.N != m.N || f.Author != m.Author || f.Title != m.Title || f.TS != m.TS {
			t.Errorf("section %d differs:\n full %+v\n meta %+v", i+1, f, m)
		}
		if m.Content != "" {
			t.Errorf("section %d: meta parse kept a body: %q", i+1, m.Content)
		}
	}
	if full.Header != meta.Header {
		t.Error("pad header differs between the two parses")
	}
}

// A body written as one enormous line is the case the hand-written buffer-full path in
// readLine exists for. It is worth its own test because getting it wrong silently
// truncates or duplicates content rather than failing.
func TestSectionBoundariesSurviveHugeLines(t *testing.T) {
	huge := strings.Repeat("q", 300*1024)
	raw := renderPadFile(t, huge+"\n", "after the huge one\n")
	p, err := Parse("proj", "abc123", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sections) != 2 {
		t.Fatalf("sections = %d, want 2 — a long line swallowed a boundary", len(p.Sections))
	}
	if p.Sections[0].Content != huge+"\n" {
		t.Errorf("huge body length = %d, want %d", len(p.Sections[0].Content), len(huge)+1)
	}
	if p.Sections[1].Content != "after the huge one\n" {
		t.Errorf("section 2 body = %q", p.Sections[1].Content)
	}
}
