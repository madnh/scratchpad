package pad

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FileVersion is the pad FILE format this binary writes. It is independent of the config
// marker's schema version (config.ConfigVersion): one describes a pad on disk, the other
// describes a deployment's settings, and they move for different reasons.
//
// v1 -> v2 added `opener` to the header. The bump is not cosmetic: v1's header was parsed
// by cutting the line at "; password: ", so a v1 reader meeting any new field either fails
// on the timestamp or folds the field into the password hash — silently unlocking nothing
// and refusing the right password. A version an old binary REFUSES is worth more than one
// it misreads.
const FileVersion = 2

// headerOpen and headerClose bracket the pad header. The version follows headerOpen as a
// bare integer, so a reader can tell "newer than me" from "not a pad at all" — the first
// deserves an upgrade message, the second does not.
const (
	headerOpen  = "<!-- scratchpad v"
	headerClose = " -->"
	headerSep   = "; "
)

// Header is the pad file's first line: everything true about the pad that is not one of
// its sections.
//
// It is deliberately small, and the bar for adding a field is high. Anything derivable
// from the sections belongs there instead (see Authors) — a header field is a second place
// for the same fact to live, and the two disagree the first time someone edits the file by
// hand. What earns a place here is what the sections CANNOT say.
type Header struct {
	// Version is the file format version this header was written in. ParseHeader refuses
	// anything above FileVersion rather than guessing.
	Version int

	Created time.Time

	// PasswordHash is bcrypt, empty when the pad is unprotected.
	PasswordHash string

	// Opener is the agent that owns this pad — who the `opener` rules policy trusts.
	//
	// It is a FIELD rather than "the author of section 1" because a continued pad's first
	// section is written by whichever agent happened to fill the previous one, which is not
	// its owner. Deriving ownership from position would hand the pad to a passer-by.
	//
	// Nothing an agent sends can set it: it is copied from the previous pad's header by the
	// continuation path, or from section 1 when a v1 file is upgraded. Both are decisions
	// made by this package, not values named in a request.
	Opener string

	// Continues is the ref of the pad this one took over from, empty on an original pad.
	// It is what makes a continuation visible instead of two unrelated transcripts.
	Continues string

	// TasksFrom is the highest task number already used by the pads this one continues,
	// so task numbering carries on instead of restarting.
	//
	// Task numbers are how agents refer to work in prose — "T3 is blocked" is written in
	// section bodies that no longer live in this file. Restarting at 1 would make a second
	// T3 that means something else, and every earlier sentence about T3 silently wrong.
	// It is a header field rather than a derived value for the plain reason that the
	// evidence is in another file: nothing in THIS pad records a number it never used.
	TasksFrom int

	// ContinuedBy is the ref of the pad that took over from this one, empty while this pad
	// is still the live one. It is written when the pad fills up, at the same moment the
	// closing section is appended.
	ContinuedBy string
}

// ParseHeader reads a pad header line.
//
// The format is `<!-- scratchpad v<N>; key: value; key: value -->`. Fields are keyed
// rather than positional, so a field added later cannot shift the meaning of the ones
// before it — which is exactly how v1's "cut at the password" parsing would have failed.
// Unknown keys are ignored: within a version this binary understands, a key it does not
// model is a hand-edit or a field it has no business rewriting, and neither is a reason to
// refuse the file.
func ParseHeader(line []byte) (Header, error) {
	s := strings.TrimSpace(string(line))
	if !strings.HasPrefix(s, headerOpen) || !strings.HasSuffix(s, headerClose) {
		return Header{}, fmt.Errorf("not a scratchpad file: line 1 must be %q…%q", headerOpen, headerClose)
	}
	body := s[len(headerOpen) : len(s)-len(headerClose)]

	versionStr, rest, _ := strings.Cut(body, headerSep)
	version, err := strconv.Atoi(strings.TrimSpace(versionStr))
	if err != nil || version < 1 {
		return Header{}, fmt.Errorf("not a scratchpad file: %q is not a file format version", versionStr)
	}
	if version > FileVersion {
		return Header{}, fmt.Errorf(
			"pad file is format version %d and this build reads up to %d — upgrade before opening it",
			version, FileVersion)
	}

	h := Header{Version: version}
	for _, field := range strings.Split(rest, headerSep) {
		key, value, ok := strings.Cut(field, ": ")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "created":
			if h.Created, err = time.Parse(time.RFC3339, strings.TrimSpace(value)); err != nil {
				return Header{}, fmt.Errorf("bad created timestamp in pad header: %w", err)
			}
		case "password":
			h.PasswordHash = strings.TrimSpace(value)
		case "opener":
			h.Opener = strings.TrimSpace(value)
		case "continues":
			h.Continues = strings.TrimSpace(value)
		case "continued_by":
			h.ContinuedBy = strings.TrimSpace(value)
		case "tasks_from":
			// A number this build cannot read is worse than absent: absent restarts task
			// numbering, which is wrong but visible, while a silent zero is the same thing
			// pretending to be deliberate.
			n, convErr := strconv.Atoi(strings.TrimSpace(value))
			if convErr != nil || n < 0 {
				return Header{}, fmt.Errorf("bad tasks_from in pad header: %q", value)
			}
			h.TasksFrom = n
		}
	}
	if h.Created.IsZero() {
		return Header{}, fmt.Errorf("pad header has no created timestamp")
	}
	return h, nil
}

// RenderHeader builds the header line, always at the CURRENT file version: a file this
// binary writes is a file it claims to have written. Optional fields are omitted rather
// than written empty, so an unprotected original pad's header stays the two fields it was.
func RenderHeader(h Header) string {
	var b strings.Builder
	b.WriteString(headerOpen)
	b.WriteString(strconv.Itoa(FileVersion))
	b.WriteString(headerSep)
	b.WriteString("created: ")
	b.WriteString(h.Created.UTC().Format(time.RFC3339))
	// Order is fixed so an upgrade produces a predictable line, and so a diff of two pads
	// written by different builds compares field for field.
	fields := []struct{ key, value string }{
		{"opener", h.Opener},
		{"password", h.PasswordHash},
		{"continues", h.Continues},
		{"continued_by", h.ContinuedBy},
	}
	if h.TasksFrom > 0 {
		fields = append(fields, struct{ key, value string }{"tasks_from", strconv.Itoa(h.TasksFrom)})
	}
	for _, f := range fields {
		if f.value != "" {
			b.WriteString(headerSep)
			b.WriteString(f.key)
			b.WriteString(": ")
			b.WriteString(f.value)
		}
	}
	b.WriteString(headerClose)
	return b.String()
}
