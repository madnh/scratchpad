package pad

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// Rules are the house style of a pad: how long a message should be, whether work belongs
// in a task, when to address rather than broadcast. A pad that runs for hundreds of
// sections without them ends up unreadable, and the drift is invisible while it happens —
// every agent is behaving reasonably by its own lights.
//
// They are PROSE, deliberately: what makes a pad unreadable is not something a validator
// can measure, and a rule an agent understands beats a limit it merely trips over. The
// one thing enforced mechanically is that the rules were READ (see CheckAck).
//
// Three levels, each optional, applying in order — store, project, pad:
//
//	<dir>/_rules.md                       every pad in this store
//	<dir>/projects/<p>/_rules.md          every pad in that project
//	a `kind: rules` section               that one pad
//
// A level EXTENDS the ones above it. `replace` cuts the chain, which is the escape hatch
// for a pad whose way of working is genuinely different rather than merely more specific.

// RulesFileName is the file that holds a store's or a project's rules. The leading
// underscore is the store's naming rule for "this file belongs to the tool" (see
// IsPadFileName); it is what keeps rules out of the pad namespace by law rather than by
// coincidence.
const RulesFileName = "_rules.md"

// ToolFilePrefix marks a file as the tool's own rather than a pad. Pad ids are drawn from
// [a-z0-9] and ParseRef refuses anything else, so a name starting with '_' can never
// collide with a pad — in either direction.
const ToolFilePrefix = "_"

// replaceMarker is how a rules FILE cuts the inheritance chain: the first line of the
// file, mirroring `rules: replace` on a rules section's metadata line.
const replaceMarker = "<!-- rules: replace -->"

// padFileRe is the whole rule for "this file is a pad": the pad id, then .md. Everything
// else in a project directory — the tool's own files, a person's stray notes, an editor's
// swap file — is not a pad and is never parsed as one.
var padFileRe = regexp.MustCompile(`^[a-z0-9]{1,64}\.md$`)

// IsPadFileName reports whether a file name inside a project directory names a pad.
func IsPadFileName(name string) bool { return padFileRe.MatchString(name) }

// IsToolFileName reports whether a file name belongs to the tool (the '_' prefix). It is
// what separates "not a pad, and that is expected" from "not a pad, and someone should
// know" — the first is silent, the second is what doctor reports.
func IsToolFileName(name string) bool { return strings.HasPrefix(name, ToolFilePrefix) }

// RuleLevel names where one layer of the rules came from.
type RuleLevel string

const (
	LevelStore   RuleLevel = "store"
	LevelProject RuleLevel = "project"
	LevelPad     RuleLevel = "pad"
)

// Layer is one level's rules, with enough provenance for a reader to see which line came
// from where — the reason the levels are never flattened into one blob for display.
type Layer struct {
	Level   RuleLevel `json:"level"`
	Source  string    `json:"source"`            // "_rules.md", "projects/x/_rules.md", "§43"
	Text    string    `json:"text"`              // the rules themselves, marker stripped
	Author  string    `json:"author,omitempty"`  // pad level only
	Section int       `json:"section,omitempty"` // pad level only
	TS      int64     `json:"ts,omitempty"`      // pad level only

	// Replace is set on the layer that cut the chain; Superseded marks the layers a
	// lower `replace` switched off. Both are kept rather than dropped, so a person can
	// see that a store rule exists and is currently not in force.
	Replace    bool `json:"replace,omitempty"`
	Superseded bool `json:"superseded,omitempty"`
}

// Rules is the effective rule set for one pad, plus where each part came from.
type Rules struct {
	Layers  []Layer `json:"layers,omitempty"`
	Text    string  `json:"text,omitempty"`    // the layers in force, joined, as an agent reads them
	Digest  string  `json:"digest,omitempty"`  // "" when there are no rules at all
	History []int   `json:"history,omitempty"` // earlier pad-level versions, newest first
}

// Empty reports whether this pad has no rules in force.
func (r Rules) Empty() bool { return r.Digest == "" }

// ParseRulesFile splits a rules file into its text and its replace marker. A file that is
// blank (or only the marker) carries no rules, which is the same as not existing — that
// is what lets an operator empty the file to turn rules off without deleting it.
func ParseRulesFile(data []byte) (text string, replace bool) {
	s := strings.TrimSpace(string(data))
	if rest, ok := strings.CutPrefix(s, replaceMarker); ok {
		s, replace = strings.TrimSpace(rest), true
	}
	return s, replace
}

// RenderRulesFile is the inverse: what SetStoreRules/SetProjectRules write to disk.
func RenderRulesFile(text string, replace bool) string {
	body := strings.TrimSpace(text)
	if body == "" {
		return ""
	}
	if replace {
		return replaceMarker + "\n" + body + "\n"
	}
	return body + "\n"
}

// BuildRules assembles the effective rules from the store and project files plus the
// pad's own rules section. The pad argument may be nil — the project view has no pad, and
// asking "what would apply here" is a fair question before any pad exists.
//
// The pad's rules always come last: the most specific level has the final word, exactly
// as a repo's CLAUDE.md follows the global one.
func BuildRules(storeText string, storeReplace bool, projectName, projectText string, projectReplace bool, p *Pad) Rules {
	var r Rules
	add := func(l Layer) {
		if strings.TrimSpace(l.Text) == "" {
			return
		}
		r.Layers = append(r.Layers, l)
	}
	add(Layer{Level: LevelStore, Source: RulesFileName, Text: strings.TrimSpace(storeText), Replace: storeReplace})
	if projectName != "" {
		add(Layer{
			Level: LevelProject, Source: "projects/" + projectName + "/" + RulesFileName,
			Text: strings.TrimSpace(projectText), Replace: projectReplace,
		})
	}
	if p != nil {
		if sec, history, ok := p.RulesSection(); ok {
			add(Layer{
				Level: LevelPad, Source: "§" + strconv.Itoa(sec.N),
				Text: strings.TrimSpace(sec.Content), Author: sec.Author, Section: sec.N,
				TS: sec.TS, Replace: sec.Replace,
			})
			r.History = history
		}
	}

	// A `replace` switches off everything above it. Marking the layers instead of
	// dropping them is what lets a reader see that a store rule exists and is currently
	// not in force — silently omitting it looks like the store has no rules at all.
	cut := -1
	for i, l := range r.Layers {
		if l.Replace {
			cut = i
		}
	}
	var inForce []string
	for i := range r.Layers {
		if i < cut {
			r.Layers[i].Superseded = true
			continue
		}
		inForce = append(inForce, r.Layers[i].Text)
	}
	r.Text = strings.Join(inForce, "\n\n")
	if r.Text != "" {
		sum := sha256.Sum256([]byte(r.Text))
		r.Digest = hex.EncodeToString(sum[:])[:DigestLen]
	}
	return r
}

// DigestLen is how much of the sha256 an ack carries. Eight hex characters is short
// enough for an agent to copy into a flag and a person to compare by eye, and this is a
// "have you read this" token, not a security boundary — like every other rule here.
const DigestLen = 8

// CheckAck is the one mechanical part of rules: an author posting to a pad for the FIRST
// time must quote the digest of the rules in force.
//
// It is deliberately narrow. It fires once per author per pad, because that is when an
// agent is about to write its first message with no idea how this pad works; after that
// it never gets in the way of a conversation. Later rule changes travel the way anything
// else does — the rules section is a broadcast, so it wakes the pad.
//
// Being unable to know whether an agent truly READ them, this checks the one thing that
// is observable: the digest can only be quoted by something that fetched the rules.
func CheckAck(p *Pad, author, ack string, r Rules) error {
	if r.Empty() || (p != nil && p.HasPosted(author)) {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(ack), r.Digest) {
		return nil
	}
	// A pad being created has no pad to have posted in yet, so the rules being quoted are
	// the project's. Saying "this pad" there would point at something that does not exist.
	where := "this pad has rules and you have not posted here before"
	if p == nil {
		where = "this project has rules"
	}
	return Coded(CodeRulesUnread,
		"%s — read them, then repeat with the rules digest %s:\n\n%s", where, r.Digest, r.Text)
}
