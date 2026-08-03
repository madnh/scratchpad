package store

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/madnh/scratchpad/internal/pad"
)

// Rules on disk. Two of the three levels are FILES, and that is the whole reason they are
// not pads: a rule set is edited — the current text replaces the old one — while a pad is
// append-only. The third level IS a section, because a pad's rules belong to the pad and
// must vanish with `rm` like everything else about it.
//
//	<dir>/_rules.md                  store-wide
//	<dir>/projects/<p>/_rules.md     one project
//	a `kind: rules` section          one pad
//
// The '_' prefix is the store's naming law (pad.IsPadFileName): a pad id can never start
// with it, so a rules file can never be mistaken for a pad, nor a pad for a rules file.

// storeRulesPath is where the store-wide rules live.
func (s *Store) storeRulesPath() string { return filepath.Join(s.dir, pad.RulesFileName) }

// projectRulesPath is where one project's rules live — inside the project, so deleting
// the project directory takes its rules with it and leaves nothing behind.
func (s *Store) projectRulesPath(project string) string {
	return filepath.Join(s.projectsDir, project, pad.RulesFileName)
}

// readRulesFile reads one rules file. A missing file and an empty one mean the same
// thing — no rules at that level — so an operator can blank the file instead of having to
// remember to delete it.
func (s *Store) readRulesFile(path string) (text string, replace bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()
	// Shared flock, like every other read here: a rules file is rewritten by rename, but
	// the lock also makes a concurrent CLI write visible as all-or-nothing.
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH); err != nil {
		return "", false, err
	}
	limit := int64(s.limits.MaxContentKB) * 1024
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", false, err
	}
	if int64(len(data)) > limit {
		return "", false, coded(CodeContentTooLarge,
			"%s is larger than %d KB; rules are meant to be read by an agent on every join", path, s.limits.MaxContentKB)
	}
	text, replace = pad.ParseRulesFile(data)
	return text, replace, nil
}

// writeRulesFile replaces a rules file atomically (temp file + rename), so a reader never
// sees a half-written rule set. Writing an empty text REMOVES the file: "no rules" has
// one representation on disk, not two.
func (s *Store) writeRulesFile(path, text string, replace bool) error {
	body := pad.RenderRulesFile(text, replace)
	if body == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if len(body) > s.limits.MaxContentKB*1024 {
		return coded(CodeContentTooLarge,
			"rules are %d bytes; the limit is %d KB", len(body), s.limits.MaxContentKB)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "_rules-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// StoreRules returns the store-wide rules.
func (s *Store) StoreRules() (text string, replace bool, err error) {
	return s.readRulesFile(s.storeRulesPath())
}

// ProjectRules returns one project's rules.
func (s *Store) ProjectRules(project string) (text string, replace bool, err error) {
	if err := ValidateProject(project); err != nil {
		return "", false, err
	}
	return s.readRulesFile(s.projectRulesPath(project))
}

// SetStoreRules replaces the store-wide rules.
func (s *Store) SetStoreRules(text string, replace bool) error {
	return s.writeRulesFile(s.storeRulesPath(), text, replace)
}

// SetProjectRules replaces one project's rules. The project directory is created if it
// does not exist yet — writing the way a project works before its first pad is a
// legitimate order of operations.
func (s *Store) SetProjectRules(project, text string, replace bool) error {
	if err := ValidateProject(project); err != nil {
		return err
	}
	return s.writeRulesFile(s.projectRulesPath(project), text, replace)
}

// ProjectRuleSet returns what would apply to a pad in this project — the two file levels,
// with no pad. It is what the UI shows on a project page, and what an agent sees before
// creating its first pad there.
func (s *Store) ProjectRuleSet(project string) (pad.Rules, error) {
	return s.buildRules(project, nil)
}

// PadRules returns the full three-level rule set in force for one pad, reading it from
// disk. Callers that ALREADY hold a parsed pad must use RulesOf instead.
func (s *Store) PadRules(ref, password string) (pad.Rules, error) {
	p, err := s.Get(ref, password)
	if err != nil {
		return pad.Rules{}, err
	}
	return s.RulesOf(p)
}

// RulesOf is PadRules over a pad the caller has already parsed — the two file levels are
// read, the pad is not.
//
// It exists because the surfaces reach for the rules exactly where they have just loaded
// the pad (the UI's pad view, pad_get, pad_wait), and PadRules there would re-open the
// file and rebuild every section body a second time. On a pad of hundreds of sections
// that doubles the cost and the allocation of the one request that is already the most
// expensive.
func (s *Store) RulesOf(p *Pad) (pad.Rules, error) {
	return s.buildRules(p.Project, p)
}

// buildRules reads the two files and folds them together with the pad's own rules
// section. The fold itself lives in internal/pad; this function is only the I/O around
// it, so the CLI, the MCP server and the Web UI cannot disagree about what is in force.
func (s *Store) buildRules(project string, p *Pad) (pad.Rules, error) {
	storeText, storeReplace, err := s.StoreRules()
	if err != nil {
		return pad.Rules{}, err
	}
	projectText, projectReplace := "", false
	if project != "" {
		projectText, projectReplace, err = s.ProjectRules(project)
		if err != nil {
			return pad.Rules{}, err
		}
	}
	return pad.BuildRules(storeText, storeReplace, project, projectText, projectReplace, p), nil
}

// checkRules is the rules gate on the append path, called under the exclusive flock Post
// already holds, beside the turn rule — the same home every other rule has.
//
// It does its work only for an author who has never posted in this pad, which is both the
// point (an agent about to write its first message here) and what keeps it free: the
// common append pays one map-free scan of authors and no file I/O at all.
//
// data is re-parsed WITH bodies in that case, because the rules are a section body and
// the append path deliberately parses metadata only. The bytes are already in hand, so
// this costs no extra read.
func (s *Store) checkRules(project, id string, data []byte, p *Pad, req PostRequest) error {
	// A person editing the rules through the UI is not an agent joining a conversation:
	// they are the one WRITING the rules, so there is nothing for them to have read.
	if req.SystemPost || p.HasPosted(req.Author) {
		return nil
	}
	full, err := pad.Parse(project, id, data)
	if err != nil {
		return err
	}
	rules, err := s.buildRules(project, full)
	if err != nil {
		return err
	}
	return pad.CheckAck(full, req.Author, req.AckRules, rules)
}

// SetPadRules appends a new rules section to a pad. It is an ordinary append — the rules
// are versioned the way everything else in a pad is, so the previous set stays readable
// instead of being overwritten.
//
// author is normally pad.SystemAuthor, for a person editing the rules through a surface
// that has no identity of its own (the Web UI). An agent setting the rules of a pad it
// works on passes its own name.
func (s *Store) SetPadRules(ref, author, title, text, password string, replace bool) (*PostResult, error) {
	if title == "" {
		title = "Pad rules"
	}
	return s.Post(PostRequest{
		Ref: ref, Author: author, Title: title, Content: text, Password: password,
		Meta:       Meta{Kind: pad.KindRules, Replace: replace},
		SystemPost: author == pad.SystemAuthor,
	})
}
