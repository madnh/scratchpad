package webui

import (
	"encoding/json"
	"net/http"
	"strconv"

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
type tocEntry struct {
	N      int    `json:"n"`
	Author string `json:"author"`
	Title  string `json:"title"`
	TS     int64  `json:"ts"`
	Bytes  int    `json:"bytes"`
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

	toc := make([]tocEntry, 0, len(pad.Sections))
	for _, sec := range pad.Sections {
		toc = append(toc, tocEntry{N: sec.N, Author: sec.Author, Title: sec.Title, TS: sec.TS, Bytes: len(sec.Content)})
	}
	turn := pad.TurnState()
	return padResponse{
		Ref: pad.Ref(), Project: pad.Project, PadID: pad.ID,
		Title: pad.Sections[0].Title, CreatedTS: pad.CreatedTS,
		Protected: pad.Protected(), Locked: false,
		SectionCount: len(pad.Sections), Turn: &turn, Sections: toc,
	}, nil
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
		for _, sec := range pad.Sections {
			if sec.N == n {
				return map[string]any{"sections": []store.Section{sec}, "has_older": n > 1}, nil
			}
		}
		return nil, badInput("no section " + v + " in this pad")
	}

	// The window ends just below `before` (exclusive), or at the newest section.
	end := len(pad.Sections)
	if v := q.Get("before"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil {
			return nil, badInput("before must be an integer")
		}
		end = 0
		for i, sec := range pad.Sections {
			if sec.N < n {
				end = i + 1
			}
		}
	}
	start := max(end-limit, 0)

	window := append([]store.Section(nil), pad.Sections[start:end]...)
	return map[string]any{
		"sections":  window,
		"has_older": start > 0,
		"total":     len(pad.Sections),
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
