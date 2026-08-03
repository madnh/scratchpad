package pad

// Selector is the ONE vocabulary for "which sections do I want".
//
// Before this existed the same question had three answers: the MCP tools understood
// `section | since | all`, the Web UI understood `section | before+limit`, and the CLI
// understood `since` — three implementations that could drift, for one concept. Each
// surface now only translates its own parameter names into this struct.
//
// Zero fields mean "no constraint", so the zero Selector selects the whole pad.
type Selector struct {
	Kind    Kind // "" = either stream
	Task    int  // >0 = sections concerning this task (the opener and every reference)
	Section int  // >0 = exactly this section number
	Since   int  // >0 = sections numbered above this
	Before  int  // >0 = sections numbered below this (walking backwards through history)
	Limit   int  // >0 = keep at most this many, taking the NEWEST of the matches
}

// Result is one selection: the sections, plus what was left out. HasOlder is what lets
// a pager render "load older" without a second request, and Total is the match count
// before Limit applied.
type Result struct {
	Sections []Section
	HasOlder bool
	Total    int
}

// Select applies the selector. Order is always file order (oldest first); a surface
// that displays newest-first reverses at the point of display, so paging arithmetic
// stays in one direction here.
func (p *Pad) Select(sel Selector) Result {
	matches := make([]Section, 0, len(p.Sections))
	for _, sec := range p.Sections {
		if !sel.matches(sec) {
			continue
		}
		matches = append(matches, sec)
	}
	res := Result{Sections: matches, Total: len(matches)}
	if sel.Limit > 0 && len(matches) > sel.Limit {
		res.Sections = matches[len(matches)-sel.Limit:]
		res.HasOlder = true
	}
	return res
}

// matches reports whether one section satisfies every constraint that is set.
func (sel Selector) matches(sec Section) bool {
	switch {
	case sel.Section > 0 && sec.N != sel.Section:
		return false
	case sel.Since > 0 && sec.N <= sel.Since:
		return false
	case sel.Before > 0 && sec.N >= sel.Before:
		return false
	case sel.Kind != "" && sec.Kind != sel.Kind:
		return false
	// Task matches a section's REFERENCE to a task, not only task events: a message
	// carrying `task: 3` is part of that task's story even though it changes nothing.
	case sel.Task > 0 && sec.Task != sel.Task:
		return false
	}
	return true
}

// TOC strips bodies from a selection. A table of contents is the same sections with
// the prose left behind, and every surface publishes one.
func TOC(sections []Section) []Section {
	out := make([]Section, len(sections))
	for i, sec := range sections {
		sec.Content = ""
		out[i] = sec
	}
	return out
}
