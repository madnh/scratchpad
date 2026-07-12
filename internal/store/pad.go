package store

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Pad file format (one markdown file per pad):
//
//	<!-- scratchpad v1; created: 2026-07-11T10:29:00Z; password: $2b$12$... -->
//
//	# 1 - frontend - How does API X work
//	<!-- ts: 2026-07-11T10:30:00Z -->
//
//	body…
//
// The first line is the pad header (password appears only when protected). Every post
// is a section headed `# <n> - <author> - <title>`; ONLY lines matching that exact
// pattern count as section boundaries — a `# something` inside content does not (the
// residual collision risk of a content line shaped exactly like a header is accepted
// by design). Turn state is derived from the last section; there is no other state.

// padHeaderPrefix opens the mandatory first line of every pad file. "scratchpad v1"
// names the FILE format version — independent of the config marker's schema version.
const padHeaderPrefix = "<!-- scratchpad v1; created: "

// sectionHeaderRe matches exactly `# <n> - <rest>`; rest is split on the first " - "
// into author and title. Authors are validated to never contain " - ", so the split is
// unambiguous.
var sectionHeaderRe = regexp.MustCompile(`^# (\d+) - (.*)$`)

// tsCommentPrefix opens the per-section timestamp line (kept in an HTML comment so a
// human reading the raw markdown isn't distracted by it).
const tsCommentPrefix = "<!-- ts: "

// Section is one post in a pad.
type Section struct {
	N       int    `json:"n"`
	Author  string `json:"author"`
	Title   string `json:"title"`
	TS      int64  `json:"ts"` // unix seconds
	Content string `json:"content,omitempty"`
}

// Pad is a fully parsed pad file.
type Pad struct {
	Project      string
	ID           string
	CreatedTS    int64
	PasswordHash string // "" when unprotected
	Sections     []Section
}

// Ref returns the pad's full copy-pasteable identifier `<project>-<padid>`.
func (p *Pad) Ref() string { return p.Project + "-" + p.ID }

// Protected reports whether the pad requires a password.
func (p *Pad) Protected() bool { return p.PasswordHash != "" }

// Last returns the final section (the turn holder). Every pad has at least one
// section (created with section 1), so callers may rely on it existing.
func (p *Pad) Last() Section { return p.Sections[len(p.Sections)-1] }

// Turn describes whose move it is, derived entirely from the last section.
type Turn struct {
	LastAuthor string   `json:"last_author"`
	Blocked    []string `json:"blocked"`
	WaitingFor string   `json:"waiting_for"`
}

// TurnState derives the turn from the last section: its author is blocked, everyone
// else may post.
func (p *Pad) TurnState() Turn {
	last := p.Last().Author
	return Turn{
		LastAuthor: last,
		Blocked:    []string{last},
		WaitingFor: "any author other than " + strconv.Quote(last),
	}
}

// renderHeader builds the pad header line.
func renderHeader(created time.Time, passwordHash string) string {
	s := padHeaderPrefix + created.UTC().Format(time.RFC3339)
	if passwordHash != "" {
		s += "; password: " + passwordHash
	}
	return s + " -->"
}

// renderSection builds the on-disk text of one section, including the leading blank
// line that separates it from what came before. Content is stored verbatim with a
// guaranteed trailing newline.
func renderSection(n int, author, title string, ts time.Time, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# %d - %s - %s\n", n, author, title)
	b.WriteString(tsCommentPrefix + ts.UTC().Format(time.RFC3339) + " -->\n")
	b.WriteString("\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// parsePad parses a pad file's full text. project/id are taken from the file's
// location (they are not repeated inside the file).
func parsePad(project, id string, data []byte) (*Pad, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], padHeaderPrefix) {
		return nil, fmt.Errorf("not a scratchpad file: missing %q header on line 1", strings.TrimSpace(padHeaderPrefix))
	}
	header := strings.TrimSuffix(strings.TrimPrefix(lines[0], padHeaderPrefix), " -->")
	createdStr, passwordHash, _ := strings.Cut(header, "; password: ")
	created, err := time.Parse(time.RFC3339, strings.TrimSpace(createdStr))
	if err != nil {
		return nil, fmt.Errorf("bad created timestamp in pad header: %w", err)
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
			// The ts comment directly after the header line carries the timestamp.
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
