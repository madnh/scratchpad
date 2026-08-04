package webui

import (
	"encoding/json"
	"net/http"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

// The deployment settings, read and written from the Settings page.
//
// This is the second thing the UI may WRITE, after the rules, and it is here for the same
// reason: it is the operator's, not the conversation's. Changing a limit takes no turn and
// carries no author, so none of what keeps posting an agent-only act applies to it.
//
// What it may write is deliberately narrower than what the marker holds. `tcp` carries the
// bearer-token digests, `ui` carries no_auth, and `rules` decides whether an agent may
// rewrite the operator's standing instructions — a browser session must not be a way to
// grant any of those. They are shown, read-only, because "why can I not change this here"
// is a fair question to answer on the page itself. See config.UpdateMarker, which refuses
// them again at the file, so this list is not the only thing standing between a request
// and the marker.

// editableConfig is the writable half of the marker. A zero value means "use the built-in
// default" — the same thing it means in the file, so blanking a field in the form and
// deleting the line from the marker do the same thing.
type editableConfig struct {
	DisplayName    string        `json:"display_name"`
	DefaultProject string        `json:"default_project"`
	Limits         config.Limits `json:"limits"`
	Wait           config.Wait   `json:"wait"`
}

// coldConfig is what this page shows but cannot change: either a value the running
// process has already bound, or one whose whole point is that a UI session cannot grant
// it. Note the absence of tcp.token_digests — a secret does not become printable just
// because the page it would print on is read-only.
type coldConfig struct {
	Instance    string             `json:"instance"`
	RootDir     string             `json:"root_dir"`
	ProjectsDir string             `json:"projects_dir"`
	SocketPath  string             `json:"socket_path"`
	MarkerFile  string             `json:"marker_file"`
	UIPort      int                `json:"ui_port"`
	TCPPort     int                `json:"tcp_port"`
	Rules       config.RulesPolicy `json:"rules"`
}

// configResponse gives the form everything it needs without hardcoding a single default.
//
// Config is what the marker literally says; Effective is what the deployment is running
// after defaults; Defaults is what an empty field will fall back to. The three are
// separate so the form can leave a field the operator never set EMPTY, showing the
// default as a placeholder. Filling them all in would turn every default into an explicit
// setting on the first save, and a deployment that never chose a value would stop
// following the built-in one when it changes.
type configResponse struct {
	Config    editableConfig `json:"config"`
	Effective editableConfig `json:"effective"`
	Defaults  editableConfig `json:"defaults"`
	Cold      coldConfig     `json:"cold"`
	Digest    string         `json:"digest"`
}

// configBody is a save: the new values plus the version they replace.
type configBody struct {
	Config editableConfig `json:"config"`
	// IfDigest is the digest from the GET this edit started at. Required, for the reason
	// the rules require theirs: two tabs, or one tab left open while somebody edited the
	// file, lose an edit exactly the way two agents do.
	IfDigest string `json:"if_digest"`
}

func (s *Server) handleConfig(_ *http.Request, _ *session) (any, error) {
	return s.configView()
}

func (s *Server) handleSetConfig(r *http.Request, _ *session) (any, error) {
	var body configBody
	// A settings payload is a handful of numbers and two short strings; 64KB is already
	// generous and keeps a malformed request from being an allocation.
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64*1024)).Decode(&body); err != nil {
		return nil, badInput("expected a JSON body with config and if_digest")
	}
	dir := s.live.Get().RootDir
	if _, err := config.UpdateMarker(dir, body.IfDigest, config.OperatorEditable, func(c *config.Config) error {
		// Assigning exactly the four editable groups is what keeps the rest of the marker
		// out of reach: the request never gets to name a field, it only supplies values
		// for fields chosen here.
		c.DisplayName = body.Config.DisplayName
		c.DefaultProject = body.Config.DefaultProject
		c.Limits = body.Config.Limits
		c.Wait = body.Config.Wait
		return nil
	}); err != nil {
		return nil, err
	}

	// Adopt it here rather than waiting for our own watcher to notice. The watcher is
	// what makes an edit from an EDITOR arrive, and it would deliver this too — but a
	// person who pressed Save and then hit a limit that had not moved yet would be
	// looking at a UI that lied to them for the length of a debounce. Reloading through
	// LoadDir keeps one code path: defaults applied, paths derived, cold groups held.
	if fresh, err := config.LoadDir(dir); err == nil {
		s.live.Apply(fresh)
	}

	// The saved FILE is the answer, not the in-memory config: it is what every other
	// process reads, and showing the old values back would read as a failed save.
	return s.configView()
}

// configView reads the marker twice on purpose — once raw (what is written, plus the
// digest of those exact bytes) and once through the loader (what those bytes MEAN once
// defaults and derived paths are applied). The form needs both to tell "unset" from
// "set to the same value as the default".
func (s *Server) configView() (configResponse, error) {
	dir := s.live.Get().RootDir
	raw, digest, err := config.ReadMarker(dir)
	if err != nil {
		return configResponse{}, err
	}
	eff, err := config.LoadDir(dir)
	if err != nil {
		return configResponse{}, err
	}
	return configResponse{
		Config: editableConfig{
			DisplayName:    raw.DisplayName,
			DefaultProject: raw.DefaultProject,
			Limits:         raw.Limits,
			Wait:           raw.Wait,
		},
		Effective: editableConfig{
			DisplayName:    eff.DisplayName,
			DefaultProject: eff.DefaultProject,
			Limits:         eff.Limits,
			Wait:           eff.Wait,
		},
		Defaults: editableConfig{
			DisplayName:    config.DefaultDisplayName,
			DefaultProject: config.DefaultProject,
			Limits:         config.DefaultLimits,
			Wait:           config.DefaultWait,
		},
		Cold: coldConfig{
			Instance:    eff.Instance,
			RootDir:     eff.RootDir,
			ProjectsDir: eff.ProjectsDir,
			SocketPath:  eff.SocketPath,
			MarkerFile:  config.MarkerPath(dir),
			UIPort:      eff.UI.Port,
			TCPPort:     eff.TCP.Port,
			Rules:       eff.Rules,
		},
		Digest: digest,
	}, nil
}

// configStatusFor maps this file's write failures onto HTTP, alongside the store codes
// httpStatusFor already knows.
//
// A stale digest is 409 for the same reason a lost rules race is: nothing about the
// request was wrong, the world moved — re-read and apply on top. A blocked group is 403,
// because re-reading will not help; that group is not this surface's to write.
func configStatusFor(err error) (int, string, bool) {
	switch {
	case pad.HasCode(err, config.CodeConfigStale):
		return http.StatusConflict, config.CodeConfigStale, true
	case pad.HasCode(err, config.CodeConfigReadOnly):
		return http.StatusForbidden, config.CodeConfigReadOnly, true
	default:
		return 0, "", false
	}
}
