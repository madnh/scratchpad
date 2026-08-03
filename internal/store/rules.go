package store

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/madnh/scratchpad/internal/config"
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

// RulesWriter says which SURFACE is asking to write the rules — not which agent, which is
// a separate question the pad level asks on top of this one.
//
// It is a parameter rather than something inferred, for the same reason PostRequest.
// SystemPost is: the Web UI's standing permission to edit the operator's files must be
// something the calling code claims deliberately, never something an agent can arrange by
// choosing a name or sending a header.
type RulesWriter string

const (
	// ByAgent is the CLI and the MCP server — whoever is on the other end, the store
	// treats it as an agent, because it cannot tell a person at a terminal from the
	// agent that person is running.
	ByAgent RulesWriter = "agent"
	// ByUI is the Web UI: a person, on a loopback listener they authenticated to.
	ByUI RulesWriter = "ui"
)

// RulesWrite is one write to a FILE level of the rules.
type RulesWrite struct {
	Text    string
	Replace bool

	// IfDigest is the version being replaced (pad.LevelDigest of the current file, or
	// pad.NoRules for a level that is empty). It is required: writing rules without
	// saying which version you read is how one agent silently drops another's.
	IfDigest string

	// By is the surface asking. See RulesWriter.
	By RulesWriter
}

// SetStoreRules replaces the store-wide rules.
func (s *Store) SetStoreRules(w RulesWrite) error {
	if err := s.checkRulesPolicy(pad.LevelStore, w.By); err != nil {
		return err
	}
	return s.writeRulesLevel(pad.LevelStore, s.storeRulesPath(), w)
}

// SetProjectRules replaces one project's rules. The project directory is created if it
// does not exist yet — writing the way a project works before its first pad is a
// legitimate order of operations.
func (s *Store) SetProjectRules(project string, w RulesWrite) error {
	if err := ValidateProject(project); err != nil {
		return err
	}
	if err := s.checkRulesPolicy(pad.LevelProject, w.By); err != nil {
		return err
	}
	return s.writeRulesLevel(pad.LevelProject, s.projectRulesPath(project), w)
}

// checkRulesPolicy applies the deployment's policy to a FILE level. The refusal names both
// ways a person can make the change, because an agent told only "you may not" has nothing
// useful to say back to the person who asked it for the change.
func (s *Store) checkRulesPolicy(level pad.RuleLevel, by RulesWriter) error {
	if by == ByUI {
		return nil
	}
	policy, where := s.rules.Store, s.storeRulesPath()
	if level == pad.LevelProject {
		policy = s.rules.Project
		where = filepath.Join(s.projectsDir, "<project>", pad.RulesFileName)
	}
	if policy == config.RulesWriteAgent {
		return nil
	}
	return coded(CodeRulesReadOnly,
		"the %s rules are the operator's, not an agent's (rules.%s = %q): they are changed in the Web UI,"+
			" or by editing %s. Put your proposed text in your reply and let the person running this paste it in.",
		level, level, policy, where)
}

// writeRulesLevel is the compare-and-set: read what is there, refuse unless the writer
// quoted that version, then replace it.
//
// The check and the write are not one atomic step, and deliberately so — serialising them
// would mean a lock file in the store dir, a fourth thing `doctor` has to know about, to
// close a window of microseconds. What this defends against is the real case, which is
// measured in minutes: an agent that read the rules, went away to think, and came back to
// write over a version somebody else had put there meanwhile. Two writers landing inside
// the same microsecond end up exactly where they do today, which is no worse than before.
func (s *Store) writeRulesLevel(level pad.RuleLevel, path string, w RulesWrite) error {
	cur, curReplace, err := s.readRulesFile(path)
	if err != nil {
		return err
	}
	if err := pad.CheckVersion(level, w.IfDigest, pad.LevelDigest(cur, curReplace), cur); err != nil {
		return err
	}
	return s.writeRulesFile(path, w.Text, w.Replace)
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
// fullPad yields the pad re-parsed WITH bodies in that case, because the rules are a
// section body and the append path deliberately parses metadata only. The bytes are
// already in hand, so this costs no extra read.
func (s *Store) checkRules(project string, fullPad func() (*Pad, error), p *Pad, req PostRequest) error {
	// A person editing the rules through the UI is not an agent joining a conversation:
	// they are the one WRITING the rules, so there is nothing for them to have read.
	if req.SystemPost || p.HasPosted(req.Author) {
		return nil
	}
	full, err := fullPad()
	if err != nil {
		return err
	}
	rules, err := s.buildRules(project, full)
	if err != nil {
		return err
	}
	return pad.CheckAck(full, req.Author, req.AckRules, rules)
}

// PadRulesRequest is one write to a pad's own rules.
type PadRulesRequest struct {
	Ref      string
	Author   string // ignored when By is ByUI, which always writes as pad.SystemAuthor
	Title    string
	Text     string
	Password string
	Replace  bool

	// IfDigest is the version being replaced — pad.LevelDigest of the rules section in
	// force, or pad.NoRules when the pad has none yet. Required, like the file levels.
	IfDigest string

	// By is the surface asking. See RulesWriter.
	By RulesWriter
}

// SetPadRules appends a new rules section to a pad. It is an ordinary append — the rules
// are versioned the way everything else in a pad is, so the previous set stays readable
// instead of being overwritten.
//
// The reserved identity belongs to the Web UI ALONE. It used to be what the CLI wrote
// under when no --as was given, which quietly meant any agent could write any pad's rules
// by simply not naming itself — the exact hole the opener policy exists to close. An agent
// names itself; a person editing in the UI has no name to give, and gets pad.SystemAuthor.
func (s *Store) SetPadRules(req PadRulesRequest) (*PostResult, error) {
	title, author := req.Title, req.Author
	if title == "" {
		title = "Pad rules"
	}
	if req.By == ByUI {
		author = pad.SystemAuthor
	}
	return s.Post(PostRequest{
		Ref: req.Ref, Author: author, Title: title, Content: req.Text, Password: req.Password,
		Meta:        Meta{Kind: pad.KindRules, Replace: req.Replace},
		RulesDigest: req.IfDigest,
		SystemPost:  req.By == ByUI,
	})
}

// checkPadRulesWrite is the pad level's half of the same two questions the file levels
// ask: may this writer touch the rules, and is it replacing the version it read.
//
// It runs inside Post, under the exclusive flock — so unlike the file levels this
// compare-and-set really is atomic: the version it compares cannot move between the check
// and the append, because the append happens before the lock is released.
//
// The UI is exempt from the OWNER question and not from the version one. A person editing
// through the UI is not an agent overstepping; two browser tabs, or a tab left open while
// an agent posted new rules, is the same lost edit as anywhere else.
func (s *Store) checkPadRulesWrite(p *Pad, req PostRequest) error {
	if !req.SystemPost && s.rules.Pad == config.RulesWriteOpener {
		if opener := p.Opener(); req.Author != opener {
			return coded(CodeNotRulesOwner,
				"the rules of pad %s belong to the agent that opened it (%s), and you are %q"+
					" (rules.pad = %q). Ask %s to write them, or edit them in the Web UI.",
				p.Ref(), opener, req.Author, s.rules.Pad, opener)
		}
	}
	cur, _, ok := p.RulesSection()
	current, currentText := pad.NoRules, ""
	if ok {
		current, currentText = pad.LevelDigest(cur.Content, cur.Replace), cur.Content
	}
	return pad.CheckVersion(pad.LevelPad, req.RulesDigest, current, currentText)
}
