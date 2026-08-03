package pad

import (
	"strconv"
	"strings"
	"time"
)

// The section metadata line, directly beneath every section header:
//
//	<!-- ts: 2026-08-02T10:30:00Z; kind: task; task: 3; to: ios, android; status: open -->
//
// It uses the `key: value; key: value` shape the pad header line has always used. `ts`
// is mandatory and always first; every other key is optional, and a section written
// before this format existed — a bare `ts` — parses as a broadcast message, so no pad
// migrates.
//
// Nothing goes on the section HEADER line (`# <n> - <author> - <title>`). That is the
// one line whose exact shape defines a section boundary, and a binary that fails to
// parse it does not lose a timestamp, it loses the boundary — turning a 600-section pad
// into a handful and getting turn state wrong. Growth belongs on this line, where the
// worst case is a lost timestamp and a visible comment.
const (
	metaPrefix = "<!-- ts: "
	metaSuffix = " -->"
	metaSep    = "; "
)

// metaPrefixBytes lets the scanner pre-filter lines without allocating.
var metaPrefixBytes = []byte(metaPrefix)

// MaxToTargets bounds one section's `to` list. Every resource in this format is
// bounded; a routing key is no exception.
const MaxToTargets = 20

// parseMetaLine parses a section's metadata line. ok is false when the line is not a
// well-formed metadata line — including when `ts` fails to parse — and the caller then
// treats it as ordinary body text, which is exactly how an unrecognised line has always
// been handled.
//
// Unknown keys are IGNORED, never an error: a key added by a later version must not
// stop an older reader, and since pads are append-only there is nothing to round-trip.
func parseMetaLine(line string) (ts time.Time, m Meta, ok bool) {
	if !strings.HasPrefix(line, metaPrefix) || !strings.HasSuffix(line, metaSuffix) {
		return time.Time{}, Meta{}, false
	}
	body := line[len(metaPrefix) : len(line)-len(metaSuffix)]

	// The timestamp is the first field. Cutting on "; " BEFORE parsing is what makes
	// the extra keys additive: parsing the whole remainder as a timestamp is precisely
	// what an older binary does, and why it degrades to treating this as body text.
	tsField, rest, _ := strings.Cut(body, metaSep)
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(tsField))
	if err != nil {
		return time.Time{}, Meta{}, false
	}

	m.Kind = KindMessage
	for rest != "" {
		var field string
		field, rest, _ = strings.Cut(rest, metaSep)
		key, val, found := strings.Cut(field, ": ")
		if !found {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "kind":
			switch Kind(val) {
			case KindTask:
				m.Kind = KindTask
			case KindRules:
				m.Kind = KindRules
			}
		case "rules":
			// The only value that means anything today. An unknown one is ignored like
			// an unknown key, so `rules: something-later` degrades to "extend".
			m.Replace = val == "replace"
		case "to":
			m.To = splitTargets(val)
		case "re":
			if n, convErr := strconv.Atoi(val); convErr == nil && n > 0 {
				m.Re = n
			}
		case "task":
			if n, convErr := strconv.Atoi(val); convErr == nil && n > 0 {
				m.Task = n
			}
		case "status":
			if validStatus(Status(val)) {
				m.Status = Status(val)
			}
		}
	}
	return ts, m, true
}

// renderMetaLine builds the metadata line. Defaults are omitted rather than spelled
// out: a plain broadcast message writes exactly the bare `ts` line it always has, so
// this change adds no bytes to the common case and leaves existing pads byte-identical
// in shape.
func renderMetaLine(ts time.Time, m Meta) string {
	var b strings.Builder
	b.WriteString(metaPrefix)
	b.WriteString(ts.UTC().Format(time.RFC3339))
	if m.Kind == KindTask || m.Kind == KindRules {
		b.WriteString(metaSep + "kind: " + string(m.Kind))
	}
	if m.Kind == KindRules && m.Replace {
		b.WriteString(metaSep + "rules: replace")
	}
	if m.Task > 0 {
		b.WriteString(metaSep + "task: " + strconv.Itoa(m.Task))
	}
	if len(m.To) > 0 {
		b.WriteString(metaSep + "to: " + strings.Join(m.To, ", "))
	}
	if m.Re > 0 {
		b.WriteString(metaSep + "re: " + strconv.Itoa(m.Re))
	}
	if m.Status != "" {
		b.WriteString(metaSep + "status: " + string(m.Status))
	}
	b.WriteString(metaSuffix)
	return b.String()
}

// splitTargets parses a comma-separated `to` list, dropping empties so a trailing
// comma or a stray space cannot produce a target nobody can ever match.
func splitTargets(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ValidateAuthor rejects authors that would break the section-header format
// (`# <n> - <author> - <title>` splits on " - ") or the metadata line (which splits on
// "; " and ": "), plus the reserved SystemAuthor.
//
// Reserving the name here covers `to` as well, since a `to` target is validated as an
// author — which is right: nobody holds a conversation with the tool.
func ValidateAuthor(author string) error {
	if strings.EqualFold(strings.TrimSpace(author), SystemAuthor) {
		return Coded(CodeInvalidInput,
			"%q is reserved for changes a person makes through the UI; post under your own agent name", SystemAuthor)
	}
	return ValidateAuthorAllowSystem(author)
}

// ValidateAuthorAllowSystem is ValidateAuthor without the reservation. The one caller
// allowed to use it is the rules-writing path, which posts as SystemAuthor on a person's
// behalf; everything else must go through ValidateAuthor.
func ValidateAuthorAllowSystem(author string) error {
	switch {
	case strings.TrimSpace(author) == "":
		return Coded(CodeInvalidInput, "author is required (who is posting?)")
	case len(author) > 200:
		return Coded(CodeInvalidInput, "author is too long (max 200 bytes)")
	case strings.ContainsAny(author, "\n\r"):
		return Coded(CodeInvalidInput, "author must be a single line")
	case strings.Contains(author, " - "):
		return Coded(CodeInvalidInput, "author must not contain \" - \" (it separates fields in the section header)")
	case strings.ContainsAny(author, ";,"):
		return Coded(CodeInvalidInput, "author must not contain ';' or ',' (they separate fields in the section metadata)")
	case author != strings.TrimSpace(author):
		return Coded(CodeInvalidInput, "author must not start or end with whitespace")
	}
	return nil
}

// ValidateMeta checks a section's metadata before it is written: the `to` list holds
// usable author names and is bounded, and the task fields are coherent.
func ValidateMeta(m Meta) error {
	if len(m.To) > MaxToTargets {
		return Coded(CodeLimitExceeded, "a section may address at most %d authors (got %d)", MaxToTargets, len(m.To))
	}
	for _, t := range m.To {
		if err := ValidateAuthor(t); err != nil {
			return Coded(CodeInvalidInput, "invalid `to` target %q: %s", t, err.(*CodedError).Msg)
		}
	}
	if m.Status != "" && !validStatus(m.Status) {
		return Coded(CodeInvalidInput, "unknown status %q (want open, wip, blocked, done or dropped)", m.Status)
	}
	if m.Status != "" && m.Kind != KindTask {
		return Coded(CodeInvalidInput, "status belongs to a task event; pass a task number too")
	}
	// A rules section says how the pad works; it is not about a piece of work and not
	// addressed at anyone in particular — the rules reach everyone by being the rules.
	if m.Kind == KindRules && (m.Task > 0 || m.Status != "" || len(m.To) > 0) {
		return Coded(CodeInvalidInput, "a rules section takes no task, status or `to` — it applies to the whole pad")
	}
	if m.Replace && m.Kind != KindRules {
		return Coded(CodeInvalidInput, "`replace` belongs to a rules section")
	}
	if m.Kind == KindTask && m.Task == 0 {
		return Coded(CodeInvalidInput, "a task event must name its task")
	}
	if m.Kind == KindTask && m.Status == StatusOpen && len(m.To) == 0 {
		return Coded(CodeTaskNeedsOwner, "a task must have an owner: pass `to` naming who is to do it")
	}
	return nil
}
