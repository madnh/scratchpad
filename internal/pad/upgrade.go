package pad

import "bytes"

// Upgrade brings a pad file up to FileVersion, returning the new bytes and whether
// anything changed. It is pure: no I/O, no locks, no clock. The caller decides when a
// rewrite is safe (see store.Post, which does it under the exclusive flock it already
// holds).
//
// This is the ONLY place that derives an opener from section 1. Everything else reads
// Header.Opener, so the derivation happens once per file, in one function, with a test —
// rather than living on as a fallback that every reader has to remember to apply and that
// quietly hands a continued pad to the wrong agent.
//
// Only the first LINE is rewritten. The sections are not touched, not re-rendered, and not
// re-parsed on the way out: an upgrade must never be able to alter what a pad says, only
// how its header states what the pad already is.
func Upgrade(project, id string, data []byte) (out []byte, changed bool, err error) {
	firstLine, rest := splitLine(data)
	header, err := ParseHeader(firstLine)
	if err != nil {
		return nil, false, err
	}
	if header.Version == FileVersion && header.Opener != "" {
		return data, false, nil
	}

	if header.Opener == "" {
		// A v1 file records no owner, so the only evidence of who opened this pad is who
		// wrote its first section — which is what v1's Opener() returned, so an upgraded
		// file answers exactly what the old code answered for the same bytes.
		p, err := ParseMeta(project, id, data)
		if err != nil {
			return nil, false, err
		}
		header.Opener = p.Sections[0].Author
	}

	var b bytes.Buffer
	b.Grow(len(data) + 64)
	b.WriteString(RenderHeader(header))
	if rest != nil {
		b.WriteByte('\n')
		b.Write(rest)
	}
	return b.Bytes(), true, nil
}
