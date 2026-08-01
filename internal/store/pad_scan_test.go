package store

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// referenceParse is the ORIGINAL line-splitting parser, kept here as the oracle the
// scanning parser is checked against. It is the thing scanPad replaced (it built one
// string per line of the file, which is why it had to go), so any behavioural drift
// between the two shows up as a test failure rather than as a surprise in a pad
// someone wrote months ago.
func referenceParse(project, id string, data []byte) (*Pad, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], padHeaderPrefix) {
		return nil, fmt.Errorf("not a scratchpad file")
	}
	header := strings.TrimSuffix(strings.TrimPrefix(lines[0], padHeaderPrefix), " -->")
	createdStr, passwordHash, _ := strings.Cut(header, "; password: ")
	created, err := time.Parse(time.RFC3339, strings.TrimSpace(createdStr))
	if err != nil {
		return nil, fmt.Errorf("bad created timestamp")
	}
	pad := &Pad{Project: project, ID: id, CreatedTS: created.Unix(), PasswordHash: strings.TrimSpace(passwordHash)}

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
		pad.Sections = append(pad.Sections, *cur)
		cur, content = nil, nil
	}
	for _, line := range lines[1:] {
		if m := sectionHeaderRe.FindStringSubmatch(line); m != nil {
			if author, title, ok := strings.Cut(m[2], " - "); ok {
				flush()
				n, _ := strconv.Atoi(m[1])
				cur = &Section{N: n, Author: author, Title: title}
				continue
			}
		}
		if cur != nil {
			if cur.TS == 0 && len(content) == 0 && strings.HasPrefix(line, tsCommentPrefix) {
				tsStr := strings.TrimSuffix(strings.TrimPrefix(line, tsCommentPrefix), " -->")
				if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(tsStr)); err == nil {
					cur.TS = ts.Unix()
					continue
				}
			}
			content = append(content, line)
		}
	}
	flush()
	if len(pad.Sections) == 0 {
		return nil, fmt.Errorf("pad file has no sections")
	}
	return pad, nil
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

func TestScanPadMatchesReferenceParser(t *testing.T) {
	for name, text := range padFiles() {
		t.Run(name, func(t *testing.T) {
			want, wantErr := referenceParse("p", "id", []byte(text))
			got, gotErr := parsePad("p", "id", []byte(text))
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

func TestParsePadMetaMatchesParsePadWithoutBodies(t *testing.T) {
	for name, text := range padFiles() {
		t.Run(name, func(t *testing.T) {
			full, err := parsePad("p", "id", []byte(text))
			if err != nil {
				return
			}
			meta, err := parsePadMeta("p", "id", []byte(text))
			if err != nil {
				t.Fatalf("meta parse failed where full parse succeeded: %v", err)
			}
			if meta.CreatedTS != full.CreatedTS || meta.PasswordHash != full.PasswordHash {
				t.Fatalf("header mismatch: %#v vs %#v", meta, full)
			}
			if len(meta.Sections) != len(full.Sections) {
				t.Fatalf("section count %d != %d", len(meta.Sections), len(full.Sections))
			}
			for i := range full.Sections {
				want := full.Sections[i]
				want.Content = "" // the only thing metadata parsing drops
				if meta.Sections[i] != want {
					t.Errorf("section %d: %#v != %#v", i, meta.Sections[i], want)
				}
			}
		})
	}
}

// TestReadRefusesOversizedPad is the regression test for the memory-exhaustion finding:
// a pad file bigger than the deployment's own limits could ever produce is refused
// instead of being read into memory, and refusing it must not break the other pads.
func TestReadRefusesOversizedPad(t *testing.T) {
	st := testStore(t)
	pad, _, err := st.CreatePad("p", "alice", "title", "body", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := pad.Ref()

	// Grow the file past the ceiling behind the store's back, the way a hand-edit or a
	// corrupted write would.
	path := st.padPath(pad.Project, pad.ID)
	over := st.maxPadBytes() + 1
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat("\n", int(over))); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := st.Get(ref, ""); !HasCode(err, CodeContentTooLarge) {
		t.Fatalf("Get on an oversized pad: want content_too_large, got %v", err)
	}
	if _, err := st.Post(ref, "bob", "t", "c", ""); !HasCode(err, CodeContentTooLarge) {
		t.Fatalf("Post to an oversized pad: want content_too_large, got %v", err)
	}

	// A second, healthy pad must still list — one bad file cannot deny the store.
	if _, _, err := st.CreatePad("p", "alice", "other", "body", false); err != nil {
		t.Fatal(err)
	}
	pads, warnings, err := st.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(pads) != 1 {
		t.Fatalf("want the healthy pad listed, got %d pads", len(pads))
	}
	if len(warnings) != 1 {
		t.Fatalf("want one warning about the oversized pad, got %v", warnings)
	}
}

// newlineHeavyPad is the shape the memory-exhaustion finding used: content that is
// almost entirely newlines, which is what turned a line-splitting parser into tens of
// megabytes of string headers per megabyte of file.
func newlineHeavyPad(sections, bytesEach int) []byte {
	var b strings.Builder
	b.WriteString("<!-- scratchpad v1; created: 2026-07-11T10:29:00Z -->\n")
	for i := 1; i <= sections; i++ {
		fmt.Fprintf(&b, "\n# %d - a%d - t\n<!-- ts: 2026-07-11T10:30:00Z -->\n\n", i, i%2)
		b.WriteString(strings.Repeat("\n", bytesEach))
	}
	return []byte(b.String())
}

func BenchmarkParsePadNewlineHeavy(b *testing.B) {
	data := newlineHeavyPad(200, 64*1024)
	b.Run("reference-line-split", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			if _, err := referenceParse("p", "id", data); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("scan", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			if _, err := parsePad("p", "id", data); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("scan-meta-only", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			if _, err := parsePadMeta("p", "id", data); err != nil {
				b.Fatal(err)
			}
		}
	})
}
