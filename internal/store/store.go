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

// SystemAuthor is re-exported for the same reason the types below are: a surface that
// writes rules on a person's behalf already imports store.
const SystemAuthor = pad.SystemAuthor

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

// maxReadablePadBytes is the floor under the read ceiling, and it does NOT move with
// policy. 256 MiB of markdown is on the order of forty million words: past that a file
// has stopped being a transcript and is corruption, or something written by hand.
//
// It is a named constant rather than a multiple of DefaultLimits ON PURPOSE. Deriving it
// from policy — even from the DEFAULT policy — reintroduces the bug it exists to fix:
// this tool's own documentation tells an agent to ask for a raised limit when it hits a
// bound, so "raise, grow, lower again" is a path we recommend, and any ceiling computed
// from the current defaults collapses back onto pads written while the limit was higher.
//
// Whoever changes DefaultLimits: leave this alone. Moving it retroactively withdraws read
// access to pads that were valid when they were written, which is the one thing an
// append-only store must never do.
const maxReadablePadBytes = 256 << 20

// maxPadBytes is the largest pad file this store will read into memory.
//
// It bounds an allocation triggered by anyone who can append: without it, one oversized
// pad takes down every listing that walks the store, not just its own page.
//
// The bound is the LARGER of what current policy could have produced and the fixed floor
// above. Policy raises it — a deployment configured for huge pads reads huge pads — but
// lowering policy no longer lowers it. Limits govern WRITES; a pad that was valid when it
// was written stays readable, whatever the operator changes afterwards.
func (s *Store) maxPadBytes(lim config.Limits) int64 {
	perSection := int64(lim.MaxContentKB+lim.MaxTitleKB)*1024 + padSizeSlack
	derived := perSection*int64(lim.MaxSectionsPerPad) + padSizeSlack
	return max(derived, maxReadablePadBytes)
}

// MaxPadBytes is the current read ceiling, for a caller that wants to ask about a pad
// WITHOUT reading it — today that is `doctor`, comparing file sizes so it can say which
// pads this deployment will refuse to open.
//
// It is exported because the listing no longer discovers this. Listings stream, so they
// never meet the ceiling, and nothing on that path can report it; a diagnosis that used to
// fall out of reading has to be asked for deliberately now.
func (s *Store) MaxPadBytes() int64 { return s.maxPadBytes(s.limits()) }

// readPadFile reads a pad file with that ceiling enforced, so an oversized file fails
// with a clear error instead of an OOM. The size is checked twice: once from the
// file's own metadata (cheap, catches the normal case) and once by reading one byte
// past the limit (authoritative, and immune to the file growing between the two).
//
// io.ReadAll grows by doubling and copying, so this holds roughly two copies of the file
// at its peak — 605 MB measured on a 244 MiB pad. Two obvious fixes have been tried and
// rejected, both measured; if you are about to try one, read this first:
//
//   - bytes.Buffer.Grow(size+1) + ReadFrom(LimitReader) is WORSE, not better. Once the
//     buffer is full ReadFrom must read again to learn the reader is done, finds no room,
//     and doubles: one run measured 1302 MB. The probe past the limit is not incidental —
//     it is how the second size check works — so the buffer can never be sized exactly.
//   - make([]byte, size) + io.ReadFull does measure smaller (272 MB), and drops the
//     property in the paragraph above: a file that grew after the stat is silently
//     truncated instead of caught. Keeping both means allocating size+1, filling the
//     first size bytes, then probing for one more.
//
// Neither was judged worth it: the metadata path — the one that ran on every listing —
// no longer comes through here at all (it streams), and what is left is reading a pad
// large enough that opening it is already unusual. Measure before deciding otherwise.
func (s *Store) readPadFile(lim config.Limits, f *os.File, ref string) ([]byte, error) {
	limit := s.maxPadBytes(lim)
	if st, err := f.Stat(); err == nil && st.Size() > limit {
		return nil, tooLarge(ref, st.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, tooLarge(ref, int64(len(data)), limit)
	}
	return data, nil
}

// tooLarge words the refusal for what it now actually means.
//
// The ceiling never drops below maxReadablePadBytes, so anything refused here is a file
// past a quarter of a gigabyte — which no honest sequence of Posts produces. "Raise the
// limits" was the right advice while the ceiling followed policy; it is misleading now,
// because the operator most likely did nothing wrong and the file most likely is not a
// transcript any more. Raising limits is still mentioned, second, because a deployment
// configured above the floor genuinely does read such a pad.
func tooLarge(ref string, size, limit int64) error {
	return coded(CodeContentTooLarge,
		"pad %s is %d bytes, past the %d byte read ceiling: a file this size is damaged or was"+
			" written by hand, not appended through this tool. Inspect it — it is plain markdown."+
			" If it is genuinely this large, raising limits raises the ceiling with them.",
		ref, size, limit)
}

// Store reads and writes pads under a projects directory, enforcing the deployment's
// limits. It holds no open handles and caches no pad content — every operation goes to
// disk, which is what lets separate processes (CLI, server) share one store safely.
//
// The limits and the rules policy are read from a *config.Live rather than copied in,
// for the same reason: a copy taken at startup is a cache, and it was the one thing that
// made two processes over one store enforce two different sets of rules.
type Store struct {
	dir         string // the Scratchpad dir; the store-wide rules file lives here
	projectsDir string
	live        *config.Live
}

// New builds a Store over a loaded deployment config: its dir, its projects directory,
// its limits and its rules policy (all already defaulted by the loader).
//
// It takes the whole config rather than the three fields it reads today, because a policy
// that a call site can forget to pass is a policy that a call site WILL forget to pass —
// and the one it would silently drop is the one deciding who may rewrite the rules.
func New(live *config.Live) *Store {
	cfg := live.Get()
	return &Store{
		dir:         cfg.RootDir,
		projectsDir: cfg.ProjectsDir,
		live:        live,
	}
}

// limits is the deployment's current bounds. Take ONE snapshot per operation and use it
// throughout: a Post that re-read them mid-flight could check a section against one limit
// and the pad size against another.
func (s *Store) limits() config.Limits { return s.live.Get().Limits }

// rulesPolicy is the deployment's current who-may-write-rules policy.
func (s *Store) rulesPolicy() config.RulesPolicy { return s.live.Get().Rules }

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
func (s *Store) validateTitle(lim config.Limits, title string) error {
	maxKB := lim.MaxTitleKB
	switch {
	case strings.TrimSpace(title) == "":
		return coded(CodeInvalidInput, "title is required")
	case strings.ContainsAny(title, "\n\r"):
		return coded(CodeInvalidInput, "title must be a single line")
	case len(title) > maxKB*1024:
		return coded(CodeContentTooLarge, "title is %d bytes; the limit is %d KB", len(title), maxKB)
	}
	return nil
}

// validateContent enforces the per-section content size limit.
func (s *Store) validateContent(lim config.Limits, content string) error {
	maxKB := lim.MaxContentKB
	switch {
	case strings.TrimSpace(content) == "":
		return coded(CodeInvalidInput, "content is required (pass the message body)")
	case len(content) > maxKB*1024:
		return coded(CodeContentTooLarge, "content is %d bytes; the limit is %d KB per section", len(content), maxKB)
	}
	return nil
}

// CreateRequest is one pad creation. Like PostRequest it is a struct rather than a
// parameter list: the call already carried five positional arguments before rules added a
// sixth, and two adjacent strings are how call sites start passing them the wrong way
// round.
type CreateRequest struct {
	Project string
	Author  string
	Title   string
	Content string
	Protect bool

	// AckRules quotes the digest of the rules that apply in this project. A pad's first
	// section is its author's first post, so the same gate applies here — with the two
	// file levels, since there is no pad yet.
	AckRules string
}

// CreatePad creates a new pad with its first section and returns the parsed pad plus,
// when protect is set, the freshly generated password (returned exactly once — only
// its bcrypt hash is stored, in the pad file's header). The project directory is
// auto-created; the pad id is random and uniqueness comes from O_EXCL creation.
func (s *Store) CreatePad(req CreateRequest) (*Pad, string, error) {
	project, author, title, content, protect := req.Project, req.Author, req.Title, req.Content, req.Protect
	if err := ValidateProject(project); err != nil {
		return nil, "", err
	}
	if err := pad.ValidateAuthor(author); err != nil {
		return nil, "", err
	}
	lim := s.limits() // one snapshot for the whole operation — see Post
	if err := s.validateTitle(lim, title); err != nil {
		return nil, "", err
	}
	if err := s.validateContent(lim, content); err != nil {
		return nil, "", err
	}
	// The rules of the project this pad is about to join. There is no pad to check
	// against yet, so this is the two file levels — the same reading an agent would get
	// from `project rules` before it starts.
	rules, err := s.buildRules(lim, project, nil)
	if err != nil {
		return nil, "", err
	}
	if err := pad.CheckAck(nil, author, req.AckRules, rules); err != nil {
		return nil, "", err
	}

	projDir := filepath.Join(s.projectsDir, project)
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		return nil, "", err
	}
	if n, err := countPads(projDir); err != nil {
		return nil, "", err
	} else if n >= lim.MaxPadsPerProject {
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
	// The opener is written into the header AT CREATION, from the author this call already
	// validated — never taken from a request field and never re-derived later. A pad's owner
	// is decided once, by the code that opens it.
	body := pad.RenderHeader(pad.Header{Created: now, PasswordHash: hash, Opener: author}) + "\n" +
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

	// AckRules quotes the digest of the rules the author has read. It is required only
	// on an author's FIRST post to a pad that has rules (see pad.CheckAck).
	AckRules string

	// RulesDigest quotes the version of the PAD's rules this section replaces, and is
	// required only when Meta.Kind is rules (see checkPadRulesWrite). It is a different
	// token from AckRules and answers a different question: AckRules is "I have read what
	// binds me", this is "I am replacing the version I saw".
	RulesDigest string

	// SystemPost marks the one append a person makes through a surface with no identity
	// of its own: editing the pad's rules in the Web UI, written as pad.SystemAuthor.
	// It is a field rather than an inference from the author name, so claiming that
	// identity has to be a deliberate act of the calling code, never a string an agent
	// can send.
	SystemPost bool
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
	validateAuthor := pad.ValidateAuthor
	if req.SystemPost {
		validateAuthor = pad.ValidateAuthorAllowSystem
	}
	if err := validateAuthor(req.Author); err != nil {
		return nil, err
	}
	// ONE snapshot for the whole operation, threaded down from here. Re-reading it per
	// check is how a post ends up validated against two different configurations: the
	// marker reloads under a running process, so the gap between two reads is a real
	// window, not a theoretical one.
	lim := s.limits()
	if err := s.validateTitle(lim, req.Title); err != nil {
		return nil, err
	}
	if err := s.validateContent(lim, req.Content); err != nil {
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

	data, err := s.readPadFile(lim, f, req.Ref)
	if err != nil {
		return nil, err
	}
	// Bring the file up to the current format IN MEMORY, so every check below reads a
	// current header — pad rules ownership among them, which is answered from Header.Opener
	// and would refuse everyone on a file that predates the field.
	//
	// The rewrite itself is deferred to the append (see needsUpgrade at the write), because
	// a post that is about to be REFUSED must not leave the file changed. Migration is the
	// tool's own business and needs no command, but it is still not something a rejected
	// request should do.
	data, needsUpgrade, err := pad.Upgrade(project, id, data)
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
	if err := checkPassword(p.PasswordHash(), req.Password); err != nil {
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
	// Both rules gates need section BODIES, which the append path deliberately does not
	// parse. Parse them at most once, and only when a gate actually fires: the common
	// append — an author who has posted here before, writing a message — pays nothing.
	var full *Pad
	fullPad := func() (*Pad, error) {
		if full == nil {
			var err error
			if full, err = pad.Parse(project, id, data); err != nil {
				return nil, fmt.Errorf("pad %s is corrupt: %w", req.Ref, err)
			}
		}
		return full, nil
	}
	if err := s.checkRules(lim, project, fullPad, p, req); err != nil {
		return nil, err
	}
	// Writing the pad's rules is checked AFTER the read gate: an agent that has not read
	// what binds it has no business replacing it, and the read gate is the one that hands
	// back the text it was supposed to have.
	if meta.Kind == pad.KindRules {
		fp, err := fullPad()
		if err != nil {
			return nil, err
		}
		if err := s.checkPadRulesWrite(fp, req); err != nil {
			return nil, err
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
	if len(p.Sections) >= lim.MaxSectionsPerPad {
		return nil, coded(CodeLimitExceeded, "pad %s already holds %d sections (the limit)", req.Ref, len(p.Sections))
	}

	warnings := p.SilenceWarnings(meta.To, time.Now())
	// Counted AFTER this post, because that is the number the author just caused and the
	// one it can act on. The limit is read from the snapshot this call took at the top, so
	// a marker edited mid-post cannot make the warning and the refusal disagree.
	if w := pad.CapacityWarning(len(p.Sections)+1, lim.MaxSectionsPerPad, lim.WarnAtPercent); w != "" {
		warnings = append(warnings, w)
	}

	n := p.Last().N + 1
	now := time.Now()
	chunk := pad.RenderSection(n, req.Author, req.Title, now, meta, req.Content)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		chunk = "\n" + chunk
	}
	if needsUpgrade {
		// The header grew, so the file is rewritten rather than appended to — the one
		// operation here that is not O(chunk). It happens at most once per pad, on the
		// first post after the upgrade, under the exclusive flock this call already holds.
		//
		// Rewritten IN PLACE, never through a temp file and rename: rename swaps the inode,
		// and a concurrent reader holding a shared flock on the old one would go on reading
		// a file nobody can see. Keeping the inode keeps the lock meaningful.
		if err := writeWholePad(f, data, chunk); err != nil {
			return nil, err
		}
	} else if _, err := f.WriteString(chunk); err != nil {
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
	data, err := s.readPadFile(s.limits(), f, ref)
	if err != nil {
		return nil, err
	}
	p, err := pad.Parse(project, id, data)
	if err != nil {
		return nil, fmt.Errorf("pad %s is corrupt: %w", ref, err)
	}
	if err := checkPassword(p.PasswordHash(), password); err != nil {
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
//
// Authors sits at the same level as LastAuthor, which listings have always published:
// it says WHO has been talking on a pad, never WHAT was said. A protected pad
// therefore lists its authors like it lists its title and section count — the password
// gates content, not the pad's existence or shape.
type PadMeta struct {
	Ref          string   `json:"ref"`
	Project      string   `json:"project"`
	Title        string   `json:"title"`
	SectionCount int      `json:"section_count"`
	Authors      []string `json:"authors"`
	// LastAuthor wrote the most recent SECTION, whatever its kind; TurnAuthor wrote the
	// most recent MESSAGE and therefore holds the turn. They are different questions and
	// both are published, because collapsing them gets one of the two wrong: a change
	// notification must name who actually just wrote, while "whose move is it" must not
	// be answered by whoever filed a task event or edited the rules — neither takes the
	// turn. (A pad ending in a task event had the same problem before rules existed; it
	// merely looked plausible, since a task event is at least written by an agent.)
	LastAuthor string `json:"last_author"`
	TurnAuthor string `json:"turn_author,omitempty"`
	LastTS     int64  `json:"last_ts"`
	CreatedTS  int64  `json:"created_ts"`
	Protected  bool   `json:"protected"`
	OpenTasks  int    `json:"open_tasks,omitempty"`
	Overdue    int    `json:"overdue,omitempty"`

	// Unreadable carries why this pad could not be opened, and is empty for every pad
	// that could. A row with it set has nothing else filled in but Ref and Project.
	//
	// It exists because dropping the row was worse than an incomplete one: a pad that
	// vanishes from `pad list` reads as deleted, and the operator goes looking for data
	// loss instead of at the one line explaining it. The pad is still there; only this
	// process cannot read it.
	Unreadable string `json:"unreadable,omitempty"`
}

// meta reduces a parsed pad to its listing entry.
func meta(p *Pad) PadMeta {
	last := p.Last()
	m := PadMeta{
		Ref:          p.Ref(),
		Project:      p.Project,
		Title:        p.Title(),
		SectionCount: len(p.Sections),
		Authors:      p.Authors(),
		LastAuthor:   last.Author,
		TurnAuthor:   p.TurnState().LastAuthor,
		LastTS:       last.TS,
		CreatedTS:    p.CreatedTS(),
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
// existence.
//
// A pad that cannot be read is still LISTED, with PadMeta.Unreadable carrying the reason,
// and warned about. It used to be skipped, which meant a pad the store still holds simply
// disappeared from the table — indistinguishable from one that had been deleted.
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
			// Only a pad file is a pad. The store's own files (`_rules.md`) and anything
			// a person happened to drop in here are skipped rather than parsed and
			// reported as broken pads — `doctor` is where the unexpected ones surface.
			if e.IsDir() || !pad.IsPadFileName(e.Name()) {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			parsed, err := s.readNoPassword(p, id)
			if err != nil {
				// Both: the warning is what a person scanning stderr sees, the row is what
				// stops the pad looking deleted to anyone reading the table or the API.
				warnings = append(warnings, fmt.Sprintf("%s-%s: %v", p, id, err))
				pads = append(pads, PadMeta{
					Ref: p + "-" + id, Project: p, Unreadable: err.Error(),
				})
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
// so it never materialises section bodies.
//
// It STREAMS the file rather than reading it whole. Skipping the bodies used to keep the
// cost proportional to a pad's size rather than to its prose, but the file still had to be
// resident to be scanned, so one 250 MiB pad set what every listing cost in memory
// (measured: 605 MB peak RSS for `pad list`). Reading line by line removes the
// proportionality altogether.
func (s *Store) readNoPassword(project, id string) (*Pad, error) {
	ref := project + "-" + id
	f, err := openPad(s.padPath(project, id), ref, os.O_RDONLY, unix.LOCK_SH)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// STREAMED, not read whole. This is the path every listing walks, and it wants
	// metadata only — so the pad's size stops deciding what `pad list` costs in memory.
	// No read ceiling applies here for the same reason: the ceiling bounds an allocation,
	// and there is no longer an allocation proportional to the file.
	return pad.ScanMeta(project, id, f)
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

// StrayFiles lists entries inside the projects tree that are neither pads nor files
// belonging to the tool, as paths relative to the projects dir.
//
// Skipping such a file silently is right for every command that lists pads — it is not a
// pad, and pretending otherwise is how a stray note becomes a "corrupt pad" warning. But
// silence everywhere would hide a real mistake: a pad renamed by hand, or a rules file
// spelled `rules.md`, simply disappears. `doctor` reports what this returns, so the two
// behaviours are one decision made in one place.
func (s *Store) StrayFiles() ([]string, error) {
	names, err := s.projectNames()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range names {
		entries, err := os.ReadDir(filepath.Join(s.projectsDir, name))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || pad.IsPadFileName(e.Name()) || pad.IsToolFileName(e.Name()) {
				continue
			}
			out = append(out, filepath.Join(name, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
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
		if !e.IsDir() && pad.IsPadFileName(e.Name()) {
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

// writeWholePad replaces a pad file's contents with body+chunk, in place.
//
// In place is the whole point: the alternative — write a temp file, rename it over this
// one — swaps the inode, and every flock in this package is taken on the pad file itself.
// A reader that acquired a shared lock a moment earlier would keep reading the old inode
// while a writer holds a lock on the new one, so the lock stops meaning anything precisely
// when two callers are present. See DESIGN.md on why the same reasoning rules out a
// rewrite-based section format.
//
// The cost of that choice is honest: a crash midway leaves a truncated pad, where rename
// would have left the old file intact. It is accepted because this runs on an upgrade path
// that touches only the first line, at most once per pad.
func writeWholePad(f *os.File, body []byte, chunk string) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	if _, err := f.WriteString(chunk); err != nil {
		return err
	}
	// The header only ever grows, so there is nothing to cut today. Truncating anyway costs
	// one syscall and means a future header that SHRINKS cannot leave a tail of the old file
	// behind — a failure that would look like corruption and be invisible in review.
	return f.Truncate(int64(len(body) + len(chunk)))
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
