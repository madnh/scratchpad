// Package store is the shared storage layer for pads: one markdown file per pad under
// <dir>/projects/<project>/<padid>.md. BOTH the CLI and the MCP server go through this
// package, with the same flock discipline, so an agent on the CLI and an agent on MCP
// can safely interleave on one store — appends take an exclusive flock on the pad file,
// reads a shared one, and every rule and derived view comes from internal/pad (there is
// no state outside the pad files; a deleted file simply is a deleted pad).
//
// This package owns FILES and LOCKS. What a pad MEANS — the format, the turn rule, task
// ownership, selection, the derived views — lives in internal/pad, which touches no
// disk. Keeping the two apart is what stops each surface from growing its own copy of
// "which sections did I want" and "is T3 done".
package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

// The pad vocabulary, re-exported so a caller that already imports store for I/O does
// not need a second import to name what comes back.
type (
	Pad         = pad.Pad
	Section     = pad.Section
	Meta        = pad.Meta
	Turn        = pad.Turn
	Selector    = pad.Selector
	Kind        = pad.Kind
	Task        = pad.Task
	Owed        = pad.Owed
	Participant = pad.Participant
	Wake        = pad.Wake
)

// projectNameRe is the full validation rule for project names: only a-z and 0-9 —
// deliberately no '-' or '_', because '-' separates project from pad id in a ref.
var projectNameRe = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// idAlphabet generates pad ids from a-z0-9 minus the confusable characters (l/1, o/0):
// a human relays refs between sessions, sometimes reading or typing them by hand.
const idAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// idLength is the pad id length (~30 bits — uniqueness is guaranteed by O_EXCL
// creation with retries, not by the raw entropy).
const idLength = 6

// waitPollInterval is how often waiters re-check the pad file. The file is the single
// source of truth (no push channel), so waiting is periodic re-parse.
const waitPollInterval = 750 * time.Millisecond

// padSizeSlack covers the per-section framing this store writes around content — the
// header line, the metadata comment and the blank separators — plus the pad header, so
// the ceiling below is never tighter than what an honest writer can legitimately
// produce.
const padSizeSlack = 1024

// maxPadBytes is the largest pad file this store will read into memory. It is DERIVED
// from the deployment's own limits rather than configured: a pad that exceeds
// (title + content + framing) x MaxSectionsPerPad cannot have been produced through
// Post, so it was hand-written or corrupted, and reading it is all cost and no value.
//
// Without this, a pad file is an unbounded allocation triggered by anyone who can
// append — and a single oversized pad would take down every listing that walks the
// store, not just its own page.
func (s *Store) maxPadBytes() int64 {
	perSection := int64(s.limits.MaxContentKB+s.limits.MaxTitleKB)*1024 + padSizeSlack
	return perSection*int64(s.limits.MaxSectionsPerPad) + padSizeSlack
}

// readPadFile reads a pad file with that ceiling enforced, so an oversized file fails
// with a clear error instead of an OOM. The size is checked twice: once from the
// file's own metadata (cheap, catches the normal case) and once by reading one byte
// past the limit (authoritative, and immune to the file growing between the two).
func (s *Store) readPadFile(f *os.File, ref string) ([]byte, error) {
	limit := s.maxPadBytes()
	if st, err := f.Stat(); err == nil && st.Size() > limit {
		return nil, coded(CodeContentTooLarge,
			"pad %s is %d bytes; this deployment reads at most %d (raise limits, or split the pad)", ref, st.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, coded(CodeContentTooLarge,
			"pad %s exceeds the %d byte read limit for this deployment", ref, limit)
	}
	return data, nil
}

// Store reads and writes pads under a projects directory, enforcing the deployment's
// limits. It holds no open handles or caches — every operation goes to disk, which is
// what lets separate processes (CLI, server) share one store safely.
type Store struct {
	projectsDir string
	limits      config.Limits
}

// New builds a Store rooted at projectsDir with the given limits (zero fields have
// been defaulted by the config loader).
func New(projectsDir string, limits config.Limits) *Store {
	return &Store{projectsDir: projectsDir, limits: limits}
}

// ProjectsDir returns the root the store operates under.
func (s *Store) ProjectsDir() string { return s.projectsDir }

// padPath returns the pad file location for a project/id pair.
func (s *Store) padPath(project, id string) string {
	return filepath.Join(s.projectsDir, project, id+".md")
}

// ParseRef splits a full pad reference `<project>-<padid>` into its parts. Project
// names cannot contain '-', so the FIRST '-' is always the separator.
func ParseRef(ref string) (project, id string, err error) {
	project, id, ok := strings.Cut(strings.TrimSpace(ref), "-")
	if !ok || !projectNameRe.MatchString(project) || id == "" || !regexp.MustCompile(`^[a-z0-9]+$`).MatchString(id) {
		return "", "", coded(CodeInvalidRef, "%q is not a pad ref; expected <project>-<padid> like \"projectx-abc123\" (both parts a-z0-9 only)", ref)
	}
	return project, id, nil
}

// ValidateProject checks a project name against the naming rule.
func ValidateProject(name string) error {
	if !projectNameRe.MatchString(name) {
		return coded(CodeInvalidProjectName, "project name %q is invalid: only a-z and 0-9 are allowed (no '-' or '_'), max 64 chars", name)
	}
	return nil
}

// validateTitle enforces the single-line title and its size limit.
func (s *Store) validateTitle(title string) error {
	switch {
	case strings.TrimSpace(title) == "":
		return coded(CodeInvalidInput, "title is required")
	case strings.ContainsAny(title, "\n\r"):
		return coded(CodeInvalidInput, "title must be a single line")
	case len(title) > s.limits.MaxTitleKB*1024:
		return coded(CodeContentTooLarge, "title is %d bytes; the limit is %d KB", len(title), s.limits.MaxTitleKB)
	}
	return nil
}

// validateContent enforces the per-section content size limit.
func (s *Store) validateContent(content string) error {
	switch {
	case strings.TrimSpace(content) == "":
		return coded(CodeInvalidInput, "content is required (pass the message body)")
	case len(content) > s.limits.MaxContentKB*1024:
		return coded(CodeContentTooLarge, "content is %d bytes; the limit is %d KB per section", len(content), s.limits.MaxContentKB)
	}
	return nil
}

// CreatePad creates a new pad with its first section and returns the parsed pad plus,
// when protect is set, the freshly generated password (returned exactly once — only
// its bcrypt hash is stored, in the pad file's header). The project directory is
// auto-created; the pad id is random and uniqueness comes from O_EXCL creation.
func (s *Store) CreatePad(project, author, title, content string, protect bool) (*Pad, string, error) {
	if err := ValidateProject(project); err != nil {
		return nil, "", err
	}
	if err := pad.ValidateAuthor(author); err != nil {
		return nil, "", err
	}
	if err := s.validateTitle(title); err != nil {
		return nil, "", err
	}
	if err := s.validateContent(content); err != nil {
		return nil, "", err
	}

	projDir := filepath.Join(s.projectsDir, project)
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		return nil, "", err
	}
	if n, err := countPads(projDir); err != nil {
		return nil, "", err
	} else if n >= s.limits.MaxPadsPerProject {
		return nil, "", coded(CodeLimitExceeded, "project %q already holds %d pads (the limit); delete old pads first", project, n)
	}

	password, hash := "", ""
	if protect {
		var err error
		password, err = GeneratePassword()
		if err != nil {
			return nil, "", err
		}
		hash, err = HashPassword(password)
		if err != nil {
			return nil, "", err
		}
	}

	now := time.Now()
	body := pad.RenderHeader(now, hash) + "\n" +
		pad.RenderSection(1, author, title, now, pad.Meta{Kind: pad.KindMessage}, content)

	for attempt := 0; attempt < 10; attempt++ {
		id, err := newPadID()
		if err != nil {
			return nil, "", err
		}
		f, err := os.OpenFile(s.padPath(project, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // id collision — roll again
		}
		if err != nil {
			return nil, "", err
		}
		_, werr := f.WriteString(body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(s.padPath(project, id))
			return nil, "", werr
		}
		p, err := pad.Parse(project, id, []byte(body))
		if err != nil {
			return nil, "", err
		}
		return p, password, nil
	}
	return nil, "", fmt.Errorf("could not allocate a unique pad id after 10 attempts")
}

// PostRequest is one append. It is a struct rather than a parameter list because a
// section now carries routing and task metadata, and seven positional arguments at
// three call sites is how surfaces start disagreeing about what they mean.
type PostRequest struct {
	Ref      string
	Author   string
	Title    string
	Content  string
	Password string
	Meta     Meta

	// OpenTask asks the store to allocate the next task number for this pad and open a
	// task with it. The number cannot come from the caller: allocation has to happen
	// under the same lock as the append, or two agents opening a task at once would
	// choose the same number.
	OpenTask bool
}

// PostResult is what an append produced.
type PostResult struct {
	Pad     *Pad
	Section int
	Task    int // the task this post opened or moved, 0 when it touched none

	// Warnings are advisory and never mean failure — today, that an addressee has been
	// silent long enough that nobody may be listening. They are returned at the moment
	// the sender can still act on it, which is what presence was wanted for.
	Warnings []string
}

// Post appends a new section, enforcing the rules under an exclusive flock: parse the
// pad's metadata, check the turn (against the last MESSAGE) and task ownership, allocate
// the section number and any new task number, then append. The lock makes
// check-allocate-append atomic against concurrent writers (CLI or server).
func (s *Store) Post(req PostRequest) (*PostResult, error) {
	if err := pad.ValidateAuthor(req.Author); err != nil {
		return nil, err
	}
	if err := s.validateTitle(req.Title); err != nil {
		return nil, err
	}
	if err := s.validateContent(req.Content); err != nil {
		return nil, err
	}
	project, id, err := ParseRef(req.Ref)
	if err != nil {
		return nil, err
	}

	f, err := openPad(s.padPath(project, id), req.Ref, os.O_RDWR, unix.LOCK_EX)
	if err != nil {
		return nil, err
	}
	defer f.Close() // closing the fd releases the flock

	data, err := s.readPadFile(f, req.Ref)
	if err != nil {
		return nil, err
	}
	// Appending needs the turn holder, the section count, the task state and the
	// password hash — never the bodies, so they are not materialised on the write path.
	// That one scan answers all of it, which is why ownership checks and task-number
	// allocation cost no extra read.
	p, err := pad.ParseMeta(project, id, data)
	if err != nil {
		return nil, fmt.Errorf("pad %s is corrupt: %w", req.Ref, err)
	}
	if err := checkPassword(p.PasswordHash, req.Password); err != nil {
		return nil, err
	}

	meta := req.Meta
	if meta.Kind == "" {
		meta.Kind = pad.KindMessage
	}
	if req.OpenTask {
		meta.Kind, meta.Task, meta.Status = pad.KindTask, p.NextTaskNo(), pad.StatusOpen
	}
	if err := pad.ValidateMeta(meta); err != nil {
		return nil, err
	}
	if meta.Re > 0 {
		parent, ok := p.Find(meta.Re)
		if !ok {
			return nil, coded(CodeInvalidInput, "pad %s has no section %d to reply to", req.Ref, meta.Re)
		}
		// Replying to a section addresses its author without the caller repeating it.
		if !containsStr(meta.To, parent.Author) && parent.Author != req.Author {
			meta.To = append(meta.To, parent.Author)
		}
	}
	if err := p.CheckTurn(req.Author, meta.Kind); err != nil {
		return nil, err
	}
	// The two layers of a `task:` reference, checked in the order they narrow. ANY
	// section may point at a task, and only an existing one. Being part of the task's
	// RECORD is the stricter claim, and the only one ownership governs.
	if meta.Task > 0 && !req.OpenTask {
		if err := p.CheckTaskRef(meta.Task); err != nil {
			return nil, err
		}
		if meta.Kind == pad.KindTask {
			if err := p.CheckTaskOwner(meta.Task, req.Author); err != nil {
				return nil, err
			}
		}
	}
	if len(p.Sections) >= s.limits.MaxSectionsPerPad {
		return nil, coded(CodeLimitExceeded, "pad %s already holds %d sections (the limit)", req.Ref, len(p.Sections))
	}

	warnings := p.SilenceWarnings(meta.To, time.Now())

	n := p.Last().N + 1
	now := time.Now()
	chunk := pad.RenderSection(n, req.Author, req.Title, now, meta, req.Content)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		chunk = "\n" + chunk
	}
	if _, err := f.WriteString(chunk); err != nil {
		return nil, err
	}

	p.Sections = append(p.Sections, Section{
		N: n, Author: req.Author, Title: req.Title, TS: now.Unix(),
		Content: strings.TrimRight(req.Content, "\n") + "\n",
		Meta:    meta,
	})
	return &PostResult{Pad: p, Section: n, Task: meta.Task, Warnings: warnings}, nil
}

// Get reads and parses a pad (shared flock, read-only), enforcing its password.
func (s *Store) Get(ref, password string) (*Pad, error) {
	project, id, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	f, err := openPad(s.padPath(project, id), ref, os.O_RDONLY, unix.LOCK_SH)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := s.readPadFile(f, ref)
	if err != nil {
		return nil, err
	}
	p, err := pad.Parse(project, id, data)
	if err != nil {
		return nil, fmt.Errorf("pad %s is corrupt: %w", ref, err)
	}
	if err := checkPassword(p.PasswordHash, password); err != nil {
		return nil, err
	}
	return p, nil
}

// WaitRequest describes what a waiter is waiting for. Since is always a SECTION number
// regardless of kind — task events advance it even when they wake nobody.
type WaitRequest struct {
	Ref      string
	Password string
	Since    int
	Author   string
	Wake     Wake
	Timeout  time.Duration // <=0 waits until the context is cancelled

	// Unacked adds a second way to return: something this author addressed has gone
	// unanswered that long. It is what puts a floor under a wait that would otherwise
	// never end because the other agent was never listening.
	Unacked time.Duration
}

// WaitResult is what a wait returned and why.
type WaitResult struct {
	Pad     *Pad
	Changed bool
	Reason  string // "match" | "unacked" | "" when it timed out

	// Matched carries the sections that satisfied the selectors, with bodies. Skipped
	// is the table of contents of everything else above Since: waking is selective,
	// catch-up never is, because an agent that wakes with a silent gap answers from
	// stale context — the exact failure the selectors exist to prevent.
	Matched []Section
	Skipped []Section
	Unacked []Owed
}

// Wait blocks until the pad has a section above Since that matches the caller's
// selectors, until something they addressed has gone unacknowledged too long, until the
// timeout elapses, or until ctx is cancelled. A timeout is NOT an error (Changed=false),
// so callers can cleanly loop.
func (s *Store) Wait(ctx context.Context, req WaitRequest) (*WaitResult, error) {
	if req.Wake.Empty() {
		req.Wake = pad.DefaultWake()
	}
	if req.Wake.NeedsAuthor() && req.Author == "" {
		return nil, coded(CodeInvalidInput, "the me/mine wake selectors need an author: pass --as (or the author parameter)")
	}
	deadline := time.Time{}
	if req.Timeout > 0 {
		deadline = time.Now().Add(req.Timeout)
	}
	for {
		p, err := s.Get(req.Ref, req.Password)
		if err != nil {
			return nil, err
		}
		res := &WaitResult{Pad: p}
		for _, sec := range p.Select(pad.Selector{Since: req.Since}).Sections {
			if p.Wakes(sec, req.Author, req.Wake) {
				res.Matched = append(res.Matched, sec)
				continue
			}
			sec.Content = ""
			res.Skipped = append(res.Skipped, sec)
		}
		if len(res.Matched) > 0 {
			res.Changed, res.Reason = true, "match"
			return res, nil
		}
		if req.Unacked > 0 && req.Author != "" {
			cutoff := time.Now().Add(-req.Unacked)
			for _, o := range p.AwaitedBy(req.Author) {
				if time.Unix(o.TS, 0).Before(cutoff) {
					res.Unacked = append(res.Unacked, o)
				}
			}
			if len(res.Unacked) > 0 {
				res.Changed, res.Reason = true, "unacked"
				return res, nil
			}
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(waitPollInterval):
		}
	}
}

// PadMeta is one pad's listing entry — metadata only, no content. Title is borrowed
// from section 1 (pads have no name of their own).
type PadMeta struct {
	Ref          string `json:"ref"`
	Project      string `json:"project"`
	Title        string `json:"title"`
	SectionCount int    `json:"section_count"`
	LastAuthor   string `json:"last_author"`
	LastTS       int64  `json:"last_ts"`
	CreatedTS    int64  `json:"created_ts"`
	Protected    bool   `json:"protected"`
	OpenTasks    int    `json:"open_tasks,omitempty"`
	Overdue      int    `json:"overdue,omitempty"`
}

// meta reduces a parsed pad to its listing entry.
func meta(p *Pad) PadMeta {
	last := p.Last()
	m := PadMeta{
		Ref:          p.Ref(),
		Project:      p.Project,
		Title:        p.Title(),
		SectionCount: len(p.Sections),
		LastAuthor:   last.Author,
		LastTS:       last.TS,
		CreatedTS:    p.CreatedTS,
		Protected:    p.Protected(),
	}
	for _, t := range p.Tasks() {
		if t.Open() {
			m.OpenTasks++
		}
	}
	return m
}

// List returns pad metadata for one project ("" = all projects), newest activity
// first. Password-protected pads are listed too — the password gates content, not
// existence. Unparseable files are skipped and reported as warnings, never fatal.
func (s *Store) List(project string) (pads []PadMeta, warnings []string, err error) {
	if project != "" {
		if err := ValidateProject(project); err != nil {
			return nil, nil, err
		}
	}
	projects, err := s.projectNames()
	if err != nil {
		return nil, nil, err
	}
	for _, p := range projects {
		if project != "" && p != project {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.projectsDir, p))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("project %s: %v", p, err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			parsed, err := s.readNoPassword(p, id)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s-%s: %v", p, id, err))
				continue
			}
			pads = append(pads, meta(parsed))
		}
	}
	sort.Slice(pads, func(i, j int) bool { return pads[i].LastTS > pads[j].LastTS })
	return pads, warnings, nil
}

// StuckEntry is one overdue assignment, with the pad it lives in. It answers the
// question a person actually opens the UI with — "what stalled overnight?" — which
// spans pads, so answering it per-pad would mean opening every pad to find the one
// that is stuck.
type StuckEntry struct {
	Ref string `json:"ref"`
	Owed
}

// Stuck lists every assignment across the store (or one project) that has gone
// unacknowledged for longer than olderThan, oldest first. Protected pads are skipped:
// their routing metadata is above the level a listing publishes.
func (s *Store) Stuck(project string, olderThan time.Duration) ([]StuckEntry, error) {
	pads, _, err := s.List(project)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-olderThan)
	var out []StuckEntry
	for _, m := range pads {
		if m.Protected {
			continue
		}
		pr, id, err := ParseRef(m.Ref)
		if err != nil {
			continue
		}
		parsed, err := s.readNoPassword(pr, id)
		if err != nil {
			continue
		}
		for _, o := range parsed.Owed() {
			if time.Unix(o.TS, 0).Before(cutoff) {
				out = append(out, StuckEntry{Ref: m.Ref, Owed: o})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

// Meta returns ONE pad's listing metadata, plus the title of its last section. Like
// List it applies no password gate: the password gates a pad's CONTENT, not its
// existence, and this is exactly the level List already publishes.
//
// The last section's title is the one thing beyond that level, so it is returned
// EMPTY for a protected pad — a change notification for a protected pad says no more
// than its listing entry does.
func (s *Store) Meta(ref string) (PadMeta, string, error) {
	project, id, err := ParseRef(ref)
	if err != nil {
		return PadMeta{}, "", err
	}
	p, err := s.readNoPassword(project, id)
	if err != nil {
		return PadMeta{}, "", err
	}
	lastTitle := ""
	if !p.Protected() {
		lastTitle = p.Last().Title
	}
	return meta(p), lastTitle, nil
}

// LastSection returns one pad's final section, metadata only. Change notifications need
// its routing and task fields to say something useful ("T3 → done" rather than "the pad
// changed"); the caller is responsible for withholding them on a protected pad.
func (s *Store) LastSection(ref string) (Section, error) {
	project, id, err := ParseRef(ref)
	if err != nil {
		return Section{}, err
	}
	p, err := s.readNoPassword(project, id)
	if err != nil {
		return Section{}, err
	}
	return p.Last(), nil
}

// readNoPassword parses a pad without the password gate — for metadata listings only,
// so it never materialises section bodies. That is what keeps List() over a directory
// of large pads proportional to their SIZE rather than to their prose, and what stops
// one huge pad from making every listing expensive.
func (s *Store) readNoPassword(project, id string) (*Pad, error) {
	ref := project + "-" + id
	f, err := openPad(s.padPath(project, id), ref, os.O_RDONLY, unix.LOCK_SH)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := s.readPadFile(f, ref)
	if err != nil {
		return nil, err
	}
	return pad.ParseMeta(project, id, data)
}

// ProjectInfo is one project's listing entry.
type ProjectInfo struct {
	Name     string `json:"name"`
	PadCount int    `json:"pad_count"`
}

// Projects lists every project directory with its pad count, sorted by name.
func (s *Store) Projects() ([]ProjectInfo, error) {
	names, err := s.projectNames()
	if err != nil {
		return nil, err
	}
	out := make([]ProjectInfo, 0, len(names))
	for _, name := range names {
		n, err := countPads(filepath.Join(s.projectsDir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, ProjectInfo{Name: name, PadCount: n})
	}
	return out, nil
}

// Delete removes a pad's file — the pad is gone, cleanly (no state lives elsewhere).
func (s *Store) Delete(ref string) error {
	project, id, err := ParseRef(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(s.padPath(project, id)); err != nil {
		if os.IsNotExist(err) {
			return coded(CodePadNotFound, "no pad %s — wrong ref, or it was deleted", ref)
		}
		return err
	}
	return nil
}

// projectNames lists the project directories under the store root, sorted.
func (s *Store) projectNames() ([]string, error) {
	entries, err := os.ReadDir(s.projectsDir)
	if err != nil {
		return nil, fmt.Errorf("read projects dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && projectNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// countPads counts the *.md files directly inside a project directory.
func countPads(projDir string) (int, error) {
	entries, err := os.ReadDir(projDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			n++
		}
	}
	return n, nil
}

// openPad opens a pad file and takes the requested flock on it. A missing file maps
// to the uniform pad_not_found error. The flock is released when the file is closed.
func openPad(path, ref string, flag int, lock int) (*os.File, error) {
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, coded(CodePadNotFound, "no pad %s — wrong ref, or it was deleted", ref)
		}
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), lock); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock pad %s: %w", ref, err)
	}
	return f, nil
}

// newPadID draws a random pad id from the unambiguous alphabet.
func newPadID() (string, error) {
	out := make([]byte, idLength)
	max := big.NewInt(int64(len(idAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = idAlphabet[n.Int64()]
	}
	return string(out), nil
}

// containsStr reports membership in a small slice.
func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
