// Package store is the shared storage layer for pads: one markdown file per pad under
// <dir>/projects/<project>/<padid>.md. BOTH the CLI and the MCP server go through this
// package, with the same flock discipline, so an agent on the CLI and an agent on MCP
// can safely interleave on one store — appends take an exclusive flock on the pad file,
// reads a shared one, and turn state is derived from the file's last section (there is
// no state outside the pad files; a deleted file simply is a deleted pad).
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

// validateAuthor rejects authors that would break the section-header format
// (`# <n> - <author> - <title>` splits on " - ").
func validateAuthor(author string) error {
	switch {
	case strings.TrimSpace(author) == "":
		return coded(CodeInvalidInput, "author is required (who is posting?)")
	case len(author) > 200:
		return coded(CodeInvalidInput, "author is too long (max 200 bytes)")
	case strings.ContainsAny(author, "\n\r"):
		return coded(CodeInvalidInput, "author must be a single line")
	case strings.Contains(author, " - "):
		return coded(CodeInvalidInput, "author must not contain \" - \" (it separates fields in the section header)")
	case author != strings.TrimSpace(author):
		return coded(CodeInvalidInput, "author must not start or end with whitespace")
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
	if err := validateAuthor(author); err != nil {
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
	body := renderHeader(now, hash) + "\n" + renderSection(1, author, title, now, content)

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
		pad, err := parsePad(project, id, []byte(body))
		if err != nil {
			return nil, "", err
		}
		return pad, password, nil
	}
	return nil, "", fmt.Errorf("could not allocate a unique pad id after 10 attempts")
}

// Post appends a new section to a pad, enforcing the turn rule under an exclusive
// flock: parse the last section, refuse when its author is posting again, then append.
// The lock makes check-and-append atomic against concurrent writers (CLI or server).
func (s *Store) Post(ref, author, title, content, password string) (*Pad, error) {
	if err := validateAuthor(author); err != nil {
		return nil, err
	}
	if err := s.validateTitle(title); err != nil {
		return nil, err
	}
	if err := s.validateContent(content); err != nil {
		return nil, err
	}
	project, id, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}

	f, err := openPad(s.padPath(project, id), ref, os.O_RDWR, unix.LOCK_EX)
	if err != nil {
		return nil, err
	}
	defer f.Close() // closing the fd releases the flock

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	pad, err := parsePad(project, id, data)
	if err != nil {
		return nil, fmt.Errorf("pad %s is corrupt: %w", ref, err)
	}
	if err := checkPassword(pad.PasswordHash, password); err != nil {
		return nil, err
	}
	last := pad.Last()
	if last.Author == author {
		return nil, coded(CodeNotYourTurn, "you (%q) posted section %d; wait for another agent to reply (use pad_wait)", author, last.N)
	}
	if len(pad.Sections) >= s.limits.MaxSectionsPerPad {
		return nil, coded(CodeLimitExceeded, "pad %s already holds %d sections (the limit)", ref, len(pad.Sections))
	}

	n := last.N + 1
	now := time.Now()
	chunk := renderSection(n, author, title, now, content)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		chunk = "\n" + chunk
	}
	if _, err := f.WriteString(chunk); err != nil {
		return nil, err
	}

	pad.Sections = append(pad.Sections, Section{
		N: n, Author: author, Title: title, TS: now.Unix(),
		Content: strings.TrimRight(content, "\n") + "\n",
	})
	return pad, nil
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
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	pad, err := parsePad(project, id, data)
	if err != nil {
		return nil, fmt.Errorf("pad %s is corrupt: %w", ref, err)
	}
	if err := checkPassword(pad.PasswordHash, password); err != nil {
		return nil, err
	}
	return pad, nil
}

// Wait blocks until the pad has a section numbered above since, the timeout elapses,
// or ctx is cancelled. It returns the freshly parsed pad and whether anything new
// appeared — a timeout is NOT an error (changed=false), so callers can cleanly loop.
// timeout <= 0 means wait until ctx is cancelled.
func (s *Store) Wait(ctx context.Context, ref, password string, since int, timeout time.Duration) (*Pad, bool, error) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		pad, err := s.Get(ref, password)
		if err != nil {
			return nil, false, err
		}
		if pad.Last().N > since {
			return pad, true, nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return pad, false, nil
		}
		select {
		case <-ctx.Done():
			return pad, false, ctx.Err()
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
}

// meta reduces a parsed pad to its listing entry.
func meta(p *Pad) PadMeta {
	last := p.Last()
	return PadMeta{
		Ref:          p.Ref(),
		Project:      p.Project,
		Title:        p.Sections[0].Title,
		SectionCount: len(p.Sections),
		LastAuthor:   last.Author,
		LastTS:       last.TS,
		CreatedTS:    p.CreatedTS,
		Protected:    p.Protected(),
	}
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
			pad, err := s.readNoPassword(p, id)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s-%s: %v", p, id, err))
				continue
			}
			pads = append(pads, meta(pad))
		}
	}
	sort.Slice(pads, func(i, j int) bool { return pads[i].LastTS > pads[j].LastTS })
	return pads, warnings, nil
}

// readNoPassword parses a pad without the password gate — for metadata listings only.
func (s *Store) readNoPassword(project, id string) (*Pad, error) {
	f, err := openPad(s.padPath(project, id), project+"-"+id, os.O_RDONLY, unix.LOCK_SH)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return parsePad(project, id, data)
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
