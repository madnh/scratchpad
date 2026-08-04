package pad

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Pad file format (one markdown file per pad):
//
//	<!-- scratchpad v2; created: 2026-07-11T10:29:00Z; opener: frontend; password: $2b$12$... -->
//
//	# 1 - frontend - How does API X work
//	<!-- ts: 2026-07-11T10:30:00Z -->
//
//	body…
//
// The first line is the pad header (see header.go — password and the rest appear only
// when they apply). Every post is a section headed `# <n> - <author> - <title>`; ONLY
// lines matching that exact pattern count as section boundaries — a `# something` inside
// content does not (the residual collision risk of a content line shaped exactly like a
// header is accepted by design). The line beneath carries the timestamp and the section's
// metadata (see meta.go). Turn and task state are derived from the sections; the header
// holds the little that the sections cannot say, and there is no other state.

// sectionHeaderRe matches exactly `# <n> - <rest>`; rest is split on the first " - "
// into author and title. Authors are validated to never contain " - ", so the split is
// unambiguous.
var sectionHeaderRe = regexp.MustCompile(`^# (\d+) - (.*)$`)

// RenderSection builds the on-disk text of one section, including the leading blank
// line that separates it from what came before. Content is stored verbatim with a
// guaranteed trailing newline.
func RenderSection(n int, author, title string, ts time.Time, m Meta, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# %d - %s - %s\n", n, author, title)
	b.WriteString(renderMetaLine(ts, m) + "\n")
	b.WriteString("\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// Parse parses a pad file's full text, bodies included. project/id are taken from the
// file's location (they are not repeated inside the file).
func Parse(project, id string, data []byte) (*Pad, error) {
	return scan(project, id, data, true)
}

// ParseMeta parses everything EXCEPT section bodies: same sections, same order, same
// turn state, same task state, with Content left empty. Listings, change notifications,
// the append path and every derived view (turn, tasks, participants) need only that, and
// skipping the bodies means a directory of large pads costs a scan rather than a copy of
// every pad's prose. It is what makes the task board affordable to compute often.
func ParseMeta(project, id string, data []byte) (*Pad, error) {
	return scan(project, id, data, false)
}

// ScanMeta is ParseMeta from a READER, for callers holding an open file rather than its
// bytes. It is what a listing wants: memory stays flat in the pad's size, so one enormous
// pad no longer sets what walking the whole store costs.
func ScanMeta(project, id string, r io.Reader) (*Pad, error) {
	return scanLines(project, id, r)
}

// scan walks bytes already in hand, holding only offsets — the path taken when the
// caller wants CONTENT.
//
// It is not the streaming path, and measurement is why. A body has to end up in memory
// either way (it is the return value), and slicing it once out of a resident array beats
// accumulating it: a strings.Builder grows by doubling and copying, which measured 840 MB
// peak against this function's 666 MB on a 244 MiB pad. Streaming wins where the body is
// DISCARDED, which is scanLines' job, not this one's.
//
// The two share every decision about what a line MEANS — isSectionHeader,
// sectionHeaderRe, parseMetaLine — so the format is defined once. What differs is only
// where the body's bytes come from, and TestParseMetaAgreesWithParseExceptBodies pins the
// two to the same answer.
// It deliberately never splits the file into a []string: pad content is written by
// agents, and a file that is mostly newlines would turn into one 16-byte string header
// per line — tens of times the file's size in live heap, for every read.
//
// Each body is materialised (when withContent) as exactly one string sliced out of
// data, so parsing costs the file's size once, not a multiple of it.
func scan(project, id string, data []byte, withContent bool) (*Pad, error) {
	firstLine, rest := splitLine(data)
	header, err := ParseHeader(firstLine)
	if err != nil {
		return nil, err
	}

	p := &Pad{Project: project, ID: id, Header: header}

	var cur *Section
	// Byte range of the current section's body within data. bodyStart < 0 means the
	// body has not begun yet (we are still on the header or the metadata line).
	bodyStart, bodyEnd := -1, -1
	flush := func() {
		if cur == nil {
			return
		}
		if withContent && bodyStart >= 0 && bodyEnd > bodyStart {
			// The same trims the line-joining parser applied: one leading newline is
			// the blank line RenderSection writes, trailing ones are the separator
			// before the next section.
			body := strings.TrimRight(strings.TrimPrefix(string(data[bodyStart:bodyEnd]), "\n"), "\n")
			if body != "" {
				body += "\n"
			}
			cur.Content = body
		}
		p.Sections = append(p.Sections, *cur)
		cur, bodyStart, bodyEnd = nil, -1, -1
	}

	// A file with no newline at all has no lines after the header, and therefore no
	// sections — the check below reports that.
	for rest != nil {
		line, next := splitLine(rest)
		lineStart := len(data) - len(rest)
		// A body ends where this line ends, WITHOUT its trailing newline — exactly the
		// text strings.Join(lines, "\n") produced from the same lines.
		lineEnd := lineStart + len(line)
		handled := false

		if isSectionHeader(line) {
			if m := sectionHeaderRe.FindSubmatch(line); m != nil {
				if author, title, ok := strings.Cut(string(m[2]), " - "); ok {
					flush()
					n, _ := strconv.Atoi(string(m[1]))
					cur = &Section{N: n, Author: author, Title: title, Meta: Meta{Kind: KindMessage}}
					handled = true
				}
			}
		}
		if !handled && cur != nil {
			// The metadata line directly after the header carries the timestamp and the
			// section's routing/task metadata.
			if cur.TS == 0 && bodyStart < 0 && bytes.HasPrefix(line, metaPrefixBytes) {
				if ts, meta, ok := parseMetaLine(string(line)); ok {
					cur.TS = ts.Unix()
					cur.Meta = meta
					handled = true
				}
			}
			if !handled {
				if bodyStart < 0 {
					bodyStart = lineStart
				}
				bodyEnd = lineEnd
			}
		}
		rest = next
	}
	flush()

	if len(p.Sections) == 0 {
		return nil, fmt.Errorf("pad file has no sections")
	}
	return p, nil
}

// scanLines walks the file line by line from a READER, keeping the header lines and
// throwing every body away as it goes. It is the METADATA path.
//
// This is what makes a listing cost the same on a 250 MiB pad as on a 10 KB one: measured,
// `pad list` over a store holding one 244 MiB pad went from 605 MB peak RSS to 22 MB.
// Author and title go through string(...), which COPIES — nothing in the result points
// back at the input, so the bytes behind each line are collectable as soon as the next
// line is read.
//
// It deliberately never splits the file into a []string: pad content is written by
// agents, and a file that is mostly newlines would turn into one 16-byte string header
// per line — tens of times the file's size in live heap, for every read.
func scanLines(project, id string, r io.Reader) (*Pad, error) {
	br := bufio.NewReaderSize(r, 64*1024)

	firstLine, more, err := readLine(br)
	if err != nil {
		return nil, err
	}
	header, err := ParseHeader(firstLine)
	if err != nil {
		return nil, err
	}

	p := &Pad{Project: project, ID: id, Header: header}

	var cur *Section
	// bodyStarted stands in for the offset parser's "bodyStart >= 0": it says the body has
	// begun, so the metadata line is no longer expected. The body itself is never kept —
	// discarding it is the entire point of this path.
	bodyStarted := false
	flush := func() {
		if cur == nil {
			return
		}
		p.Sections = append(p.Sections, *cur)
		cur, bodyStarted = nil, false
	}

	// A file with no newline at all has no lines after the header, and therefore no
	// sections — the check below reports that.
	for more {
		var line []byte
		line, more, err = readLine(br)
		if err != nil {
			return nil, err
		}
		handled := false

		if isSectionHeader(line) {
			if m := sectionHeaderRe.FindSubmatch(line); m != nil {
				if author, title, ok := strings.Cut(string(m[2]), " - "); ok {
					flush()
					n, _ := strconv.Atoi(string(m[1]))
					cur = &Section{N: n, Author: author, Title: title, Meta: Meta{Kind: KindMessage}}
					handled = true
				}
			}
		}
		if !handled && cur != nil {
			// The metadata line directly after the header carries the timestamp and the
			// section's routing/task metadata.
			if cur.TS == 0 && !bodyStarted && bytes.HasPrefix(line, metaPrefixBytes) {
				if ts, meta, ok := parseMetaLine(string(line)); ok {
					cur.TS = ts.Unix()
					cur.Meta = meta
					handled = true
				}
			}
			if !handled {
				bodyStarted = true
			}
		}
	}
	flush()

	if len(p.Sections) == 0 {
		return nil, fmt.Errorf("pad file has no sections")
	}
	return p, nil
}

// readLine returns the next line without its newline, and whether another line follows.
//
// It reproduces splitLine's contract exactly, including the corner that matters: input
// ending in "\n" yields a final EMPTY line, the way strings.Split does. Getting that
// wrong changes where a body ends, so it is spelled out rather than left to bufio's
// defaults.
func readLine(br *bufio.Reader) (line []byte, more bool, err error) {
	chunk, err := br.ReadSlice('\n')
	switch err {
	case nil:
		return chunk[:len(chunk)-1], true, nil
	case bufio.ErrBufferFull:
		// A line longer than the buffer: take what we have and keep going. Agent prose
		// runs to whole paragraphs on one line, so this is ordinary, not exceptional.
		buf := append([]byte(nil), chunk...)
		for {
			chunk, err = br.ReadSlice('\n')
			buf = append(buf, chunk...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err == io.EOF {
				return buf, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			return buf[:len(buf)-1], true, nil
		}
	case io.EOF:
		return chunk, false, nil
	default:
		return nil, false, err
	}
}

// splitLine returns the next line (without its newline) and everything after that
// newline. rest is nil when the line was the last one — i.e. the data ran out without
// a newline — which is how the loop above knows to stop. Data ending in "\n" yields a
// final empty line, matching strings.Split's behaviour.
func splitLine(b []byte) (line, rest []byte) {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i], b[i+1:]
	}
	return b, nil
}

// isSectionHeader is the cheap pre-filter that keeps the regexp off the ~99% of lines
// that are ordinary prose.
func isSectionHeader(line []byte) bool {
	return len(line) > 2 && line[0] == '#' && line[1] == ' '
}
