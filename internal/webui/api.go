package webui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/madnh/scratchpad/internal/buildinfo"
	"github.com/madnh/scratchpad/internal/store"
)

// defaultSectionLimit is how many sections one page of /api/pads/{ref}/sections
// returns. Pads run to hundreds of sections of long agent prose, so content is never
// shipped wholesale: the UI takes the newest page and walks backwards on demand.
const defaultSectionLimit = 20

// maxSectionLimit bounds an explicit `limit`, so a hand-written request cannot ask
// the server to serialise an entire large pad in one response.
const maxSectionLimit = 100

// apiFunc is a JSON handler: it returns a value to encode, or an error to map.
type apiFunc func(r *http.Request, sess *session) (any, error)

// api wraps a JSON handler with the session gate and uniform encoding/error mapping.
func (s *Server) api(fn apiFunc) http.HandlerFunc {
	return s.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// From the context, not a second cookie lookup: on the request that mints the
		// session the cookie does not exist yet, and looking again would hand the
		// handler a nil session.
		sess := sessionFrom(r)
		out, err := fn(r, sess)
		if err != nil {
			status, code := httpStatusFor(err)
			msg := err.Error()
			if ce, ok := err.(*store.CodedError); ok {
				msg = ce.Msg
			}
			writeError(w, status, code, msg)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	}))
}

// httpStatusFor maps a store error code onto an HTTP status, keeping the store's
// stable code in the body so the UI branches on the code, not on prose.
func httpStatusFor(err error) (int, string) {
	switch {
	case store.HasCode(err, store.CodePadNotFound):
		return http.StatusNotFound, store.CodePadNotFound
	case store.HasCode(err, store.CodeUnauthorized):
		return http.StatusForbidden, store.CodeUnauthorized
	case store.HasCode(err, store.CodeInvalidRef):
		return http.StatusBadRequest, store.CodeInvalidRef
	case store.HasCode(err, store.CodeInvalidProjectName):
		return http.StatusBadRequest, store.CodeInvalidProjectName
	case store.HasCode(err, store.CodeInvalidInput):
		return http.StatusBadRequest, store.CodeInvalidInput
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// writeError emits the uniform error body: a stable code plus one plain sentence.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "error": msg})
}

// statusResponse is what the UI shows in its footer/settings: which store it is
// looking at and whether change detection is running on kernel events or the
// degraded rescan (so "why am I not seeing updates?" is answerable in the UI).
type statusResponse struct {
	DisplayName string `json:"display_name"`
	Instance    string `json:"instance"`
	ProjectsDir string `json:"projects_dir"`
	Version     string `json:"version"`
	Watcher     string `json:"watcher"` // "push" | "rescan"
	ReadOnly    bool   `json:"read_only"`
}

func (s *Server) handleStatus(_ *http.Request, _ *session) (any, error) {
	mode := "push"
	if s.watcher.Degraded() {
		mode = "rescan"
	}
	return statusResponse{
		DisplayName: s.cfg.DisplayName,
		Instance:    s.cfg.Instance,
		ProjectsDir: s.cfg.ProjectsDir,
		Version:     buildinfo.Get().Version,
		Watcher:     mode,
		ReadOnly:    true,
	}, nil
}

// projectEntry adds last-activity to the store's project listing so the projects page
// can sort by "what is alive" rather than alphabetically.
type projectEntry struct {
	Name     string `json:"name"`
	PadCount int    `json:"pad_count"`
	LastTS   int64  `json:"last_ts"`
}

func (s *Server) handleProjects(_ *http.Request, _ *session) (any, error) {
	projects, err := s.store.Projects()
	if err != nil {
		return nil, err
	}
	pads, _, err := s.store.List("")
	if err != nil {
		return nil, err
	}
	last := make(map[string]int64, len(projects))
	for _, p := range pads {
		if p.LastTS > last[p.Project] {
			last[p.Project] = p.LastTS
		}
	}
	out := make([]projectEntry, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectEntry{Name: p.Name, PadCount: p.PadCount, LastTS: last[p.Name]})
	}
	return map[string]any{"projects": out}, nil
}

func (s *Server) handlePads(r *http.Request, _ *session) (any, error) {
	pads, warnings, err := s.store.List(r.URL.Query().Get("project"))
	if err != nil {
		return nil, err
	}
	if pads == nil {
		pads = []store.PadMeta{}
	}
	return map[string]any{"pads": pads, "warnings": warnings}, nil
}

// tocEntry is one line of a pad's table of contents: everything needed to render the
// index and decide what to fetch, WITHOUT the section body.
//
// The routing fields are what turn the transcript from a list of posts into a readable
// conversation: `to` chips, `re` back-links with a reply count on the parent, and a
// visual distinction for task events. They cost nothing extra to serve — this response
// already carries the pad's ENTIRE table of contents, so the page computes reply links
// and filters from data it already holds, with no second request.
type tocEntry struct {
	N      int      `json:"n"`
	Author string   `json:"author"`
	Title  string   `json:"title"`
	TS     int64    `json:"ts"`
	Bytes  int      `json:"bytes"`
	Kind   string   `json:"kind,omitempty"`
	To     []string `json:"to,omitempty"`
	Re     int      `json:"re,omitempty"`
	Task   int      `json:"task,omitempty"`
	Status string   `json:"status,omitempty"`
}

// previewChars and previewLines bound the opening excerpt of a section: a few lines
// is enough to answer "what is in here?" without the response becoming the section.
const (
	previewChars = 280
	previewLines = 4
)

// sectionPreview reduces a section body to the opening excerpt that introduces it.
//
// It collects the first lines carrying actual prose — skipping blank lines, fence
// markers, and the leading punctuation of headings, quotes and bullets, none of which
// says anything about the section — and cuts the result to previewChars RUNES, so a
// multi-byte character is never split in half.
//
// `title` is the section's own title, and is dropped from the front of the excerpt
// when the body opens by repeating it as a heading — which agents commonly do. The
// popup shows the title directly above, and saying it twice wastes the two lines that
// would have told the reader something new.
//
// Lines are taken one at a time rather than via strings.Split: a section can be
// thousands of lines and the answer is always in the first few.
func sectionPreview(content, title string) string {
	var lines []string
	total := 0
	for rest := content; rest != "" && len(lines) < previewLines && total < previewChars; {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~") {
			continue
		}
		// Markdown's own furniture, not content. A rule ("---") trims away to nothing
		// and is skipped with everything else that turns out to be empty.
		s = strings.TrimSpace(strings.TrimLeft(s, "#>-*+ \t"))
		if s == "" {
			continue
		}
		// Only the FIRST line can be the repeated title; the same words appearing
		// later in the prose are part of what the section says.
		if len(lines) == 0 && strings.EqualFold(s, strings.TrimSpace(title)) {
			continue
		}
		lines = append(lines, s)
		total += len([]rune(s))
	}

	out := strings.Join(lines, "\n")
	r := []rune(out)
	if len(r) <= previewChars {
		return out
	}
	return strings.TrimRight(string(r[:previewChars-1]), " \n") + "…"
}

// handleSectionPreview answers with the opening excerpt of ONE section.
//
// The outline in the UI shows a popup when a person hovers an entry, and this is what
// fills it. It is a separate, tiny response rather than a field on the TOC because the
// TOC is fetched for every pad view while a preview is wanted for the handful of
// entries someone actually points at — putting it in the TOC would make every pad
// load pay for excerpts nobody reads.
func (s *Server) handleSectionPreview(r *http.Request, sess *session) (any, error) {
	ref := r.PathValue("ref")
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		return nil, badInput("section must be an integer")
	}
	pad, err := s.store.Get(ref, sess.unlocked(ref))
	if err != nil {
		return nil, err
	}
	sec, ok := pad.Find(n)
	if !ok {
		return nil, badInput("no section " + strconv.Itoa(n) + " in this pad")
	}
	return map[string]any{
		"n":       sec.N,
		"author":  sec.Author,
		"title":   sec.Title,
		"bytes":   len(sec.Content),
		"preview": sectionPreview(sec.Content, sec.Title),
	}, nil
}

// padResponse is the compact pad view: header, turn state, and the full TOC. For a
// protected pad that has not been unlocked, the TOC is omitted along with the content
// it describes — the listing-level metadata still comes through.
type padResponse struct {
	Ref          string      `json:"ref"`
	Project      string      `json:"project"`
	PadID        string      `json:"pad_id"`
	Title        string      `json:"title"`
	CreatedTS    int64       `json:"created_ts"`
	Protected    bool        `json:"protected"`
	Locked       bool        `json:"locked"`
	SectionCount int         `json:"section_count"`
	Turn         *store.Turn `json:"turn,omitempty"`
	Sections     []tocEntry  `json:"sections"`

	// Participants rides along with the pad rather than living behind its own endpoint:
	// the strip is always on screen, and a second round-trip for something never hidden
	// is pure latency. The task board, which sits behind a tab, is a separate endpoint
	// for the mirror-image reason.
	Participants []store.Participant `json:"participants,omitempty"`
}

func (s *Server) handlePad(r *http.Request, sess *session) (any, error) {
	ref := r.PathValue("ref")
	pad, err := s.store.Get(ref, sess.unlocked(ref))
	if err != nil {
		// A protected pad the person has not unlocked yet is not a failure: answer
		// with the listing-level metadata and a `locked` flag so the UI can render
		// the pad's identity and prompt for the password.
		if store.HasCode(err, store.CodeUnauthorized) {
			m, _, mErr := s.store.Meta(ref)
			if mErr != nil {
				return nil, mErr
			}
			return padResponse{
				Ref: m.Ref, Project: m.Project, PadID: padIDOf(m.Ref, m.Project),
				Title: m.Title, CreatedTS: m.CreatedTS, Protected: true, Locked: true,
				SectionCount: m.SectionCount, Sections: []tocEntry{},
			}, nil
		}
		return nil, err
	}

	all := pad.Select(store.Selector{}).Sections
	toc := make([]tocEntry, 0, len(all))
	for _, sec := range all {
		toc = append(toc, tocEntry{
			N: sec.N, Author: sec.Author, Title: sec.Title, TS: sec.TS, Bytes: len(sec.Content),
			Kind: string(sec.Kind), To: sec.To, Re: sec.Re, Task: sec.Task, Status: string(sec.Status),
		})
	}
	turn := pad.TurnState()
	return padResponse{
		Ref: pad.Ref(), Project: pad.Project, PadID: pad.ID,
		Title: pad.Title(), CreatedTS: pad.CreatedTS,
		Protected: pad.Protected(), Locked: false,
		SectionCount: len(pad.Sections), Turn: &turn, Sections: toc,
		Participants: pad.Participants(),
	}, nil
}

// handleTasks serves the derived task board, or one task with its thread.
//
// The fold itself lives in internal/pad and is exposed here rather than recomputed in
// JavaScript. A second implementation of "what is T3's status" would drift from the
// CLI's, and a board that disagrees with `pad tasks` is worse than no board.
func (s *Server) handleTasks(r *http.Request, sess *session) (any, error) {
	ref := r.PathValue("ref")
	pad, err := s.store.Get(ref, sess.unlocked(ref))
	if err != nil {
		return nil, err
	}
	if v := r.URL.Query().Get("task"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil {
			return nil, badInput("task must be an integer")
		}
		t, ok := pad.Task(n)
		if !ok {
			return nil, badInput("no task T" + v + " in this pad")
		}
		return map[string]any{"ref": ref, "tasks": []store.Task{t}, "thread": pad.Thread(n)}, nil
	}
	tasks := pad.Tasks()
	if tasks == nil {
		tasks = []store.Task{}
	}
	return map[string]any{"ref": ref, "tasks": tasks}, nil
}

// defaultStuckThreshold is how long an assignment may sit unanswered before the
// overview calls it out. It is the "did anything stall overnight?" horizon, not a
// precise SLA, so it is a constant rather than another config field.
const defaultStuckThreshold = 30 * time.Minute

// handleStuck answers the question a person actually opens the UI with — "what stalled?"
// — which spans pads. Answering it per-pad would mean opening every pad to find the one
// that is stuck, which is exactly the work the overview is supposed to save.
func (s *Server) handleStuck(r *http.Request, _ *session) (any, error) {
	threshold := defaultStuckThreshold
	if v := r.URL.Query().Get("older_than_s"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, badInput("older_than_s must be a non-negative integer")
		}
		threshold = time.Duration(n) * time.Second
	}
	stuck, err := s.store.Stuck(r.URL.Query().Get("project"), threshold)
	if err != nil {
		return nil, err
	}
	if stuck == nil {
		stuck = []store.StuckEntry{}
	}
	return map[string]any{"stuck": stuck, "older_than_s": int(threshold.Seconds())}, nil
}

// handleSections returns ONE PAGE of section bodies. The default page is the newest
// `limit` sections; `before` walks backwards through the history from there. This is
// the whole reason the pad view stays cheap on a pad with hundreds of long sections.
func (s *Server) handleSections(r *http.Request, sess *session) (any, error) {
	ref := r.PathValue("ref")
	pad, err := s.store.Get(ref, sess.unlocked(ref))
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()

	limit := defaultSectionLimit
	if v := q.Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n <= 0 {
			return nil, badInput("limit must be a positive integer")
		}
		limit = min(n, maxSectionLimit)
	}

	// One section by number — the "jump to #N" case.
	if v := q.Get("section"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil {
			return nil, badInput("section must be an integer")
		}
		hit := pad.Select(store.Selector{Section: n})
		if len(hit.Sections) == 0 {
			return nil, badInput("no section " + v + " in this pad")
		}
		return map[string]any{"sections": hit.Sections, "has_older": n > 1}, nil
	}

	// `before` walks backwards through the history; `limit` keeps the newest page of
	// what is left. Both are this surface's spelling of the ONE selection vocabulary —
	// the MCP tools spell the same thing `since`, and the CLI `--since`. Expressing it
	// as a Selector is what stops the three from drifting apart.
	sel := store.Selector{Limit: limit, Kind: store.Kind(q.Get("kind"))}
	if v := q.Get("before"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil {
			return nil, badInput("before must be an integer")
		}
		sel.Before = n
	}
	if v := q.Get("task"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil {
			return nil, badInput("task must be an integer")
		}
		sel.Task = n
	}
	res := pad.Select(sel)
	return map[string]any{
		"sections":  res.Sections,
		"has_older": res.HasOlder,
		"total":     res.Total,
	}, nil
}

// handleUnlock verifies a protected pad's password once and remembers it for this
// browser session, so later reads cost no extra bcrypt round and the browser never
// has to keep the secret itself.
func (s *Server) handleUnlock(r *http.Request, sess *session) (any, error) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(&body); err != nil {
		return nil, badInput("expected a JSON body with a password field")
	}
	ref := r.PathValue("ref")
	if _, err := s.store.Get(ref, body.Password); err != nil {
		return nil, err
	}
	sess.remember(ref, body.Password)
	return map[string]any{"ref": ref, "unlocked": true}, nil
}

// handleDelete removes ONE pad. There is deliberately no bulk endpoint: wiping a
// batch of transcripts is irreversible, and `pad purge` already does it in the place
// where a person states an age threshold and reads the victim list before confirming.
func (s *Server) handleDelete(r *http.Request, _ *session) (any, error) {
	ref := r.PathValue("ref")
	if err := s.store.Delete(ref); err != nil {
		return nil, err
	}
	return map[string]any{"ref": ref, "deleted": true}, nil
}

// badInput builds the store-shaped error for a malformed request parameter, so the
// UI sees the same code vocabulary the CLI and MCP use.
func badInput(msg string) error {
	return &store.CodedError{Code: store.CodeInvalidInput, Msg: msg}
}

// padIDOf recovers a pad id from a ref when only the metadata is at hand.
func padIDOf(ref, project string) string {
	if len(ref) > len(project)+1 {
		return ref[len(project)+1:]
	}
	return ""
}
