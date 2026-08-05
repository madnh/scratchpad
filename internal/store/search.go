package store

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/madnh/scratchpad/internal/pad"
)

// SearchRequest describes a content search across the store.
//
// It is the one read that cannot be answered from metadata: every other view (list, the
// task board, turn state) derives from section headers, while a search has to look at
// the prose an agent wrote. That makes it the only place where reading the whole store
// is the CHEAP option rather than the careless one — see Search.
type SearchRequest struct {
	// Query is a literal substring by default; Regexp reads it as an RE2 pattern instead.
	Query string

	// Project narrows the walk to one project, Ref to one pad. Ref is the only way to
	// search a PROTECTED pad, because it is the only form that can carry a password.
	Project  string
	Ref      string
	Password string

	// ExcludePads drops pads by ref. It is the other half of Ref, and it exists because
	// the pad a team is arguing in TODAY is the one that buries the pad where the thing
	// was settled: a word being discussed loudly is exactly the word whose origin is
	// hardest to find.
	ExcludePads []string

	// Before and After bound a hit's own timestamp (unix seconds, 0 = unbounded). They
	// filter SECTIONS, not pads: a pad that is still active usually also holds the old
	// decision, so dropping the whole pad would drop the answer with the noise.
	Before int64
	After  int64

	// Oldest orders by when a line was written, earliest first, instead of by pad
	// activity. It answers a different question from the default, and it is the question
	// people actually arrive with: "where was this DECIDED" is almost always the first
	// time the word appears, while the default surfaces the most recent restatements.
	Oldest bool

	Regexp bool

	// Word requires the match to stand as a whole word. The boundary is spelled with
	// Unicode classes rather than \b, which is ASCII-only in RE2 and would happily match
	// inside "ngữ" — the searches this command exists for are frequently not ASCII.
	Word bool

	// CaseSensitive turns off the default folding. The default is the opposite of grep's
	// because a pad is prose written by several agents: whether a noun was capitalised is
	// almost never what the person searching meant to ask.
	CaseSensitive bool

	// Author and Kind filter by the section a line belongs to, not by the line.
	Author string
	Kind   string

	// Limit caps the hits RETURNED, not the bytes read: a pad whose scan has begun is
	// finished, and only the pads after it are skipped. Result.Truncated says the cap bound.
	Limit int
}

// Hit is one matching line, addressed the way the rest of the tool addresses things: by
// pad ref and section number, so `pad read <ref> --section <n>` picks up where a search
// left off. Line is 1-based within the file — it locates the line inside a long section,
// which the section number alone cannot do.
type Hit struct {
	Ref     string   `json:"ref"`
	Section int      `json:"section"`
	Author  string   `json:"author"`
	Title   string   `json:"title"`
	Kind    pad.Kind `json:"kind,omitempty"`
	TS      int64    `json:"ts"`
	Line    int      `json:"line"`
	Text    string   `json:"text"`

	// InTitle marks a hit on the section's TITLE rather than its body. Titles are the
	// index a person reads a long pad by, so a search that ignored them would miss the
	// most deliberate statement of what a section is about. Line is 0 for these.
	InTitle bool `json:"in_title,omitempty"`
}

// SearchResult is what a search found, plus what it did NOT look at. Skipped and
// Warnings exist so an empty result can never be mistaken for "the word is nowhere in
// the store": a protected pad and an unreadable one are both absences the caller has to
// be told about, not gaps to leave silent.
type SearchResult struct {
	Hits      []Hit    `json:"hits"`
	Skipped   []string `json:"skipped,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`

	// Scanned is how many pad files were actually read.
	Scanned int `json:"scanned"`
}

// matcher tests one line. It keeps a literal fast path because the common search is a
// case-sensitive noun, and bytes.Contains on the reader's buffer allocates nothing; the
// regexp path is chosen for folding too, since RE2 matching over []byte allocates nothing
// either, while strings.ToLower on every line of the store would.
type matcher struct {
	lit []byte
	re  *regexp.Regexp
}

func newMatcher(req SearchRequest) (matcher, error) {
	q := req.Query
	if q == "" {
		return matcher{}, fmt.Errorf("search needs a pattern")
	}
	if !req.Regexp && !req.Word && req.CaseSensitive {
		return matcher{lit: []byte(q)}, nil
	}
	expr := q
	if !req.Regexp {
		expr = regexp.QuoteMeta(q)
	}
	if req.Word {
		// No lookaround in RE2, so the boundaries are consuming character classes. That is
		// harmless here: the result is only ever used as a boolean.
		expr = `(?:^|[^\p{L}\p{N}_])(?:` + expr + `)(?:[^\p{L}\p{N}_]|$)`
	}
	if !req.CaseSensitive {
		expr = `(?i)` + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return matcher{}, fmt.Errorf("bad search pattern: %w", err)
	}
	return matcher{re: re}, nil
}

func (m matcher) match(b []byte) bool {
	if m.re != nil {
		return m.re.Match(b)
	}
	return bytes.Contains(b, m.lit)
}

// padHits groups one pad's hits so the result can be ordered by the pad's last activity,
// the way `pad list` orders pads — a search across a store is read newest conversation
// first, not alphabetically.
type padHits struct {
	ref  string
	ts   int64
	hits []Hit
}

// Search walks pad files and returns the lines matching req, newest pad first.
//
// It reads every byte of every pad it looks at, and there is no index. That is a choice,
// not a gap: an index would be state living outside the pad files, and this tool derives
// everything from them precisely so that a person with `rm` or an editor cannot leave a
// stale second copy of the truth behind. Narrowing (Project, Ref) is what keeps a large
// store affordable.
//
// Memory does NOT follow the bytes read: the scan streams, and only matching lines are
// kept, so the cost is proportional to the RESULT rather than to the store. Protected
// pads are skipped unless addressed by Ref with their password — a search that read
// through a password would be a way to read a protected pad one noun at a time.
func (s *Store) Search(req SearchRequest) (*SearchResult, error) {
	m, err := newMatcher(req)
	if err != nil {
		return nil, err
	}
	if req.Project != "" {
		if err := ValidateProject(req.Project); err != nil {
			return nil, err
		}
	}
	excluded, err := refSet(req.ExcludePads)
	if err != nil {
		return nil, err
	}
	res := &SearchResult{}

	if req.Ref != "" {
		project, id, err := ParseRef(req.Ref)
		if err != nil {
			return nil, err
		}
		ph, err := s.searchPad(project, id, req, m)
		if err != nil {
			return nil, err
		}
		res.Scanned = 1
		s.order(res, []padHits{ph}, req.Oldest)
		s.applyLimit(res, req.Limit)
		return res, nil
	}

	projects, err := s.projectNames()
	if err != nil {
		return nil, err
	}
	var groups []padHits
	for _, p := range projects {
		if req.Project != "" && p != req.Project {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.projectsDir, p))
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("project %s: %v", p, err))
			continue
		}
		for _, e := range entries {
			// The same naming law every other walk obeys: a pad is a pad file, `_rules.md`
			// is the tool's, and anything else is doctor's business rather than a corrupt pad.
			if e.IsDir() || !pad.IsPadFileName(e.Name()) {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			ref := p + "-" + id
			if excluded[ref] {
				continue
			}
			// Protection is read from the header alone, before a single body line is
			// looked at — the cheap check has to come first or the skip would be a
			// formality performed after reading what it meant to protect.
			protected, err := s.padIsProtected(p, id)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", ref, err))
				continue
			}
			if protected {
				res.Skipped = append(res.Skipped, ref)
				continue
			}
			ph, err := s.searchPad(p, id, req, m)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", ref, err))
				continue
			}
			res.Scanned++
			if len(ph.hits) > 0 {
				groups = append(groups, ph)
			}
		}
	}
	s.order(res, groups, req.Oldest)
	s.applyLimit(res, req.Limit)
	return res, nil
}

// order flattens the per-pad groups into the result in one of two orders.
//
// The default keeps a pad's hits TOGETHER, newest pad first, because that is how the rest
// of the tool presents pads and because grouping is what makes a long result readable.
// Oldest deliberately breaks the grouping: "which of these came first" is a question about
// absolute time, and answering it inside pad groups would answer a different one.
func (s *Store) order(res *SearchResult, groups []padHits, oldest bool) {
	if !oldest {
		// Newest pad first, and inside a pad the sections stay in file order.
		sort.SliceStable(groups, func(i, j int) bool { return groups[i].ts > groups[j].ts })
	}
	res.Hits = nil
	for _, g := range groups {
		res.Hits = append(res.Hits, g.hits...)
	}
	if oldest {
		sort.SliceStable(res.Hits, func(i, j int) bool {
			a, b := res.Hits[i], res.Hits[j]
			if a.TS != b.TS {
				return a.TS < b.TS
			}
			if a.Ref != b.Ref {
				return a.Ref < b.Ref
			}
			if a.Section != b.Section {
				return a.Section < b.Section
			}
			return a.Line < b.Line
		})
	}
}

// applyLimit trims the result and records that it did. It runs AFTER ordering, so a
// limited search returns the first hits of the order that was asked for — with Oldest
// that is the earliest mentions, which is the whole point of asking for it.
func (s *Store) applyLimit(res *SearchResult, limit int) {
	if limit > 0 && len(res.Hits) > limit {
		res.Hits = res.Hits[:limit]
		res.Truncated = true
	}
}

// refSet validates and indexes the refs to leave out. An unparsable ref is refused rather
// than ignored: a typo that silently excludes nothing would show the very pad the caller
// asked to be rid of, and they would read that as "the word is only here".
func refSet(refs []string) (map[string]bool, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make(map[string]bool, len(refs))
	for _, r := range refs {
		if _, _, err := ParseRef(r); err != nil {
			return nil, fmt.Errorf("--exclude-pad %q: %w", r, err)
		}
		out[r] = true
	}
	return out, nil
}

// padIsProtected reads a pad's header line and nothing else.
//
// Parsing stays in pad.ParseHeader — the header is defined in one place and this is not a
// second definition of it, only a reader that stops after line 1.
func (s *Store) padIsProtected(project, id string) (bool, error) {
	ref := project + "-" + id
	f, err := openPad(s.padPath(project, id), ref, os.O_RDONLY, unix.LOCK_SH)
	if err != nil {
		return false, err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 8*1024).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	h, err := pad.ParseHeader(bytes.TrimSuffix(line, []byte("\n")))
	if err != nil {
		return false, err
	}
	return h.PasswordHash != "", nil
}

// searchPad scans one pad file, collecting the lines that match.
//
// The password is checked AFTER the scan rather than before, because the header this
// pad's protection is stated in is line 1 of the same stream. Nothing collected is
// returned when the check fails — the hits are built and dropped, which costs a scan and
// keeps the gate in one place instead of splitting it across two reads of the file.
func (s *Store) searchPad(project, id string, req SearchRequest, m matcher) (padHits, error) {
	ref := project + "-" + id
	f, err := openPad(s.padPath(project, id), ref, os.O_RDONLY, unix.LOCK_SH)
	if err != nil {
		return padHits{}, err
	}
	defer f.Close()

	var hits []Hit
	kind := pad.Kind(req.Kind)
	selects := func(sec *pad.Section) bool {
		if req.Author != "" && sec.Author != req.Author {
			return false
		}
		if kind != "" && sec.Kind != kind {
			return false
		}
		// The window is on the SECTION's own time, so an old decision stays findable
		// inside a pad that is still busy today.
		if req.Before > 0 && sec.TS >= req.Before {
			return false
		}
		if req.After > 0 && sec.TS <= req.After {
			return false
		}
		return true
	}

	p, err := pad.ScanBodyLines(project, id, f, func(sec *pad.Section, lineNo int, line []byte) {
		if !selects(sec) || !m.match(line) {
			return
		}
		hits = append(hits, Hit{
			Ref: ref, Section: sec.N, Author: sec.Author, Title: sec.Title,
			Kind: sec.Kind, TS: sec.TS, Line: lineNo, Text: cleanLine(line),
		})
	})
	if err != nil {
		return padHits{}, fmt.Errorf("pad %s is corrupt: %w", ref, err)
	}
	if err := checkPassword(p.PasswordHash(), req.Password); err != nil {
		return padHits{}, err
	}

	// Titles are matched from the parsed sections rather than from the stream: the header
	// line is where a section's number and author are still being assembled, and a title
	// hit wants the finished section. Which sections those are is asked in the one
	// vocabulary for that question — Selector — not by walking the list here.
	for _, sec := range p.Select(pad.Selector{Kind: kind}).Sections {
		// The same predicate the body lines went through, so a filter can never mean one
		// thing for a title and another for the prose under it.
		if !selects(&sec) || !m.match([]byte(sec.Title)) {
			continue
		}
		hits = append(hits, Hit{
			Ref: ref, Section: sec.N, Author: sec.Author, Title: sec.Title,
			Kind: sec.Kind, TS: sec.TS, Text: sec.Title, InTitle: true,
		})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Section != hits[j].Section {
			return hits[i].Section < hits[j].Section
		}
		return hits[i].Line < hits[j].Line
	})
	return padHits{ref: ref, ts: p.Last().TS, hits: hits}, nil
}

// cleanLine makes a matching line safe to print in a column: tabs would misalign the
// output and a trailing carriage return would overwrite it.
func cleanLine(line []byte) string {
	return strings.TrimRight(strings.ReplaceAll(string(line), "\t", "    "), "\r")
}
