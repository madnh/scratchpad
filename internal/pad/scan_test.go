package pad

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// referenceParse is the ORIGINAL line-splitting parser, kept here as the oracle the
// scanning parser is checked against. It is the thing scan replaced (it built one
// string per line of the file, which is why it had to go), so any behavioural drift
// between the two shows up as a test failure rather than as a surprise in a pad
// someone wrote months ago.
//
// It also parses the metadata line the OLD way — the whole remainder as a timestamp —
// which is exactly what an older binary does. Every case in padFiles() therefore
// doubles as a check that legacy pads still parse identically; the new grammar has its
// own tests below.
func referenceParse(project, id string, data []byte) (*Pad, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("not a scratchpad file")
	}
	header, err := ParseHeader([]byte(lines[0]))
	if err != nil {
		return nil, err
	}
	p := &Pad{Project: project, ID: id, Header: header}

	var cur *Section
	var content []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Content = strings.TrimRight(strings.TrimPrefix(strings.Join(content, "\n"), "\n"), "\n")
		if cur.Content != "" {
			cur.Content += "\n"
		}
		p.Sections = append(p.Sections, *cur)
		cur, content = nil, nil
	}
	for _, line := range lines[1:] {
		if m := sectionHeaderRe.FindStringSubmatch(line); m != nil {
			if author, title, ok := strings.Cut(m[2], " - "); ok {
				flush()
				n, _ := strconv.Atoi(m[1])
				cur = &Section{N: n, Author: author, Title: title, Meta: Meta{Kind: KindMessage}}
				continue
			}
		}
		if cur != nil {
			if cur.TS == 0 && len(content) == 0 && strings.HasPrefix(line, metaPrefix) {
				tsStr := strings.TrimSuffix(strings.TrimPrefix(line, metaPrefix), metaSuffix)
				if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(tsStr)); err == nil {
					cur.TS = ts.Unix()
					continue
				}
			}
			content = append(content, line)
		}
	}
	flush()
	if len(p.Sections) == 0 {
		return nil, fmt.Errorf("pad file has no sections")
	}
	return p, nil
}

// padFiles returns the awkward shapes a pad file can really take on disk — including
// the ones an agent can force by writing header-shaped or newline-only content.
func padFiles() map[string]string {
	head := "<!-- scratchpad v1; created: 2026-07-11T10:29:00Z -->\n"
	ts := "<!-- ts: 2026-07-11T10:30:00Z -->\n"
	sec := func(n int, author, title, body string) string {
		return fmt.Sprintf("\n# %d - %s - %s\n%s\n%s", n, author, title, ts, body)
	}
	return map[string]string{
		"single section":        head + sec(1, "a", "t", "hello\n"),
		"two sections":          head + sec(1, "a", "t", "one\n") + sec(2, "b", "u", "two\n"),
		"no trailing newline":   head + sec(1, "a", "t", "hello"),
		"blank lines in body":   head + sec(1, "a", "t", "one\n\n\ntwo\n\n"),
		"empty body":            head + sec(1, "a", "t", ""),
		"body only newlines":    head + sec(1, "a", "t", "\n\n\n\n"),
		"header-shaped body":    head + sec(1, "a", "t", "# 9 - z - forged\nafter\n"),
		"hash line not header":  head + sec(1, "a", "t", "# heading\n#not a header\n"),
		"missing ts comment":    head + "\n# 1 - a - t\nbody\n",
		"ts comment in body":    head + sec(1, "a", "t", "x\n"+ts+"y\n"),
		"crlf-ish body":         head + sec(1, "a", "t", "line\r\nline2\r\n"),
		"unicode + emoji":       head + sec(1, "å", "tïtle 🎯", "nội dung tiếng Việt 🎯\n"),
		"protected":             "<!-- scratchpad v1; created: 2026-07-11T10:29:00Z; password: $2b$12$abc -->\n" + sec(1, "a", "t", "x\n"),
		"windows line endings":  head + "\r\n# 1 - a - t\r\n" + ts + "\r\nbody\r\n",
		"trailing blank lines":  head + sec(1, "a", "t", "body\n") + "\n\n\n",
		"section number gaps":   head + sec(1, "a", "t", "x\n") + sec(7, "b", "u", "y\n"),
		"author with brackets":  head + sec(1, "a[]{}", "t - with dash", "x\n"),
		"long single-line body": head + sec(1, "a", "t", strings.Repeat("x", 5000)+"\n"),
	}
}

func TestScanMatchesReferenceParser(t *testing.T) {
	for name, text := range padFiles() {
		t.Run(name, func(t *testing.T) {
			want, wantErr := referenceParse("p", "id", []byte(text))
			got, gotErr := Parse("p", "id", []byte(text))
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("error mismatch: reference=%v scan=%v", wantErr, gotErr)
			}
			if wantErr != nil {
				return
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("parse mismatch\nreference: %#v\nscan:      %#v", want.Sections, got.Sections)
			}
		})
	}
}

func TestParseMetaMatchesParseWithoutBodies(t *testing.T) {
	for name, text := range padFiles() {
		t.Run(name, func(t *testing.T) {
			full, err := Parse("p", "id", []byte(text))
			if err != nil {
				return
			}
			meta, err := ParseMeta("p", "id", []byte(text))
			if err != nil {
				t.Fatalf("meta parse failed where full parse succeeded: %v", err)
			}
			if meta.Header != full.Header {
				t.Fatalf("header mismatch: %#v vs %#v", meta, full)
			}
			if len(meta.Sections) != len(full.Sections) {
				t.Fatalf("section count %d != %d", len(meta.Sections), len(full.Sections))
			}
			for i := range full.Sections {
				want := full.Sections[i]
				want.Content = "" // the only thing metadata parsing drops
				if !reflect.DeepEqual(meta.Sections[i], want) {
					t.Errorf("section %d: %#v != %#v", i, meta.Sections[i], want)
				}
			}
		})
	}
}

// TestLegacySectionsAreBroadcastMessages pins the migration promise: a pad written
// before the metadata line existed keeps working, with every section a broadcast
// message, so no store has to be migrated.
func TestLegacySectionsAreBroadcastMessages(t *testing.T) {
	text := "<!-- scratchpad v1; created: 2026-07-11T10:29:00Z -->\n" +
		"\n# 1 - a - t\n<!-- ts: 2026-07-11T10:30:00Z -->\n\nbody\n"
	p, err := Parse("p", "id", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	sec := p.Sections[0]
	if sec.Kind != KindMessage || !sec.Broadcast() || sec.Re != 0 || sec.Task != 0 || sec.Status != "" {
		t.Fatalf("legacy section did not parse as a plain broadcast message: %#v", sec.Meta)
	}
	if p.TurnState().LastAuthor != "a" {
		t.Fatalf("turn state changed for a legacy pad: %#v", p.TurnState())
	}
}

func TestMetaLineRoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 2, 10, 30, 0, 0, time.UTC)
	cases := []Meta{
		{Kind: KindMessage},
		{Kind: KindMessage, To: []string{"backend"}},
		{Kind: KindMessage, To: []string{"backend", "erp"}, Re: 9},
		{Kind: KindTask, Task: 3, To: []string{"ios", "android"}, Status: StatusOpen},
		{Kind: KindTask, Task: 12, Status: StatusDone, Re: 4},
	}
	for _, want := range cases {
		line := renderMetaLine(ts, want)
		gotTS, got, ok := parseMetaLine(line)
		if !ok {
			t.Fatalf("rendered line did not parse back: %q", line)
		}
		if !gotTS.Equal(ts) {
			t.Errorf("%q: ts %v != %v", line, gotTS, ts)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %#v != %#v", line, got, want)
		}
	}
}

// TestMetaLineOmitsDefaults keeps the common case byte-identical to what pads have
// always contained: a plain broadcast message writes the bare ts line and nothing more.
func TestMetaLineOmitsDefaults(t *testing.T) {
	ts := time.Date(2026, 8, 2, 10, 30, 0, 0, time.UTC)
	if got := renderMetaLine(ts, Meta{Kind: KindMessage}); got != "<!-- ts: 2026-08-02T10:30:00Z -->" {
		t.Fatalf("a plain message grew its metadata line: %q", got)
	}
}

// TestMetaLineIgnoresUnknownKeys is the forward-compatibility promise: a key this
// binary predates must be skipped, not rejected, and must not damage the keys around it.
func TestMetaLineIgnoresUnknownKeys(t *testing.T) {
	line := "<!-- ts: 2026-08-02T10:30:00Z; kind: task; encrypted: true; task: 7; to: ios -->"
	_, m, ok := parseMetaLine(line)
	if !ok {
		t.Fatal("a line with an unknown key was rejected")
	}
	if m.Kind != KindTask || m.Task != 7 || len(m.To) != 1 || m.To[0] != "ios" {
		t.Fatalf("unknown key disturbed its neighbours: %#v", m)
	}
}

// TestMetaLineRejectsBadTimestamp pins the degradation path: an unparseable line is not
// metadata, so it falls through to the body rather than producing a section with no
// timestamp.
func TestMetaLineRejectsBadTimestamp(t *testing.T) {
	if _, _, ok := parseMetaLine("<!-- ts: not-a-time; kind: task -->"); ok {
		t.Fatal("a line with an unparseable timestamp was accepted as metadata")
	}
}
