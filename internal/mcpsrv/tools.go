package mcpsrv

import (
	"context"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
)

// tocOnly strips contents from sections so pad_get stays cheap to call.
func tocOnly(sections []store.Section) []store.Section {
	out := make([]store.Section, len(sections))
	for i, sec := range sections {
		sec.Content = ""
		out[i] = sec
	}
	return out
}

// --- pad_create ---

type createInput struct {
	Project string `json:"project,omitempty" jsonschema:"project to file the pad under (a-z0-9 only, auto-created); omit for the deployment's default project"`
	Author  string `json:"author" jsonschema:"your self-declared identity in this pad (e.g. 'frontend'); the turn rule keys off it"`
	Title   string `json:"title" jsonschema:"one-line title of the first section (doubles as the pad's display title in listings)"`
	Content string `json:"content" jsonschema:"markdown body of the first section (the opening question/message)"`
	Protect bool   `json:"protect,omitempty" jsonschema:"true to password-protect the pad; the server generates the password and returns it exactly once in this result"`
}

type createOutput struct {
	Ref      string     `json:"ref"`
	Project  string     `json:"project"`
	PadID    string     `json:"pad_id"`
	Section  int        `json:"section"`
	Next     int        `json:"next"`
	Password string     `json:"password,omitempty"`
	Turn     store.Turn `json:"turn"`
}

func (s *Server) padCreate(_ context.Context, _ *mcp.CallToolRequest, in createInput) (*mcp.CallToolResult, createOutput, error) {
	project := config.ResolveProject(s.cfg, in.Project)
	pad, password, err := s.store.CreatePad(project, in.Author, in.Title, in.Content, in.Protect)
	if err != nil {
		return nil, createOutput{}, err
	}
	return nil, createOutput{
		Ref:      pad.Ref(),
		Project:  pad.Project,
		PadID:    pad.ID,
		Section:  1,
		Next:     2,
		Password: password,
		Turn:     pad.TurnState(),
	}, nil
}

// --- pad_post ---

type postInput struct {
	Ref      string `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Author   string `json:"author" jsonschema:"your self-declared identity; must differ from the last section's author (turn rule)"`
	Title    string `json:"title" jsonschema:"one-line title of this section (shows up in the pad's table of contents)"`
	Content  string `json:"content" jsonschema:"markdown body of the section"`
	Password string `json:"password,omitempty" jsonschema:"the pad's password, required when it was created with protect:true"`
}

type postOutput struct {
	Ref     string     `json:"ref"`
	Section int        `json:"section"`
	Next    int        `json:"next"`
	Turn    store.Turn `json:"turn"`
}

func (s *Server) padPost(_ context.Context, _ *mcp.CallToolRequest, in postInput) (*mcp.CallToolResult, postOutput, error) {
	pad, err := s.store.Post(in.Ref, in.Author, in.Title, in.Content, in.Password)
	if err != nil {
		return nil, postOutput{}, err
	}
	n := pad.Last().N
	return nil, postOutput{Ref: pad.Ref(), Section: n, Next: n + 1, Turn: pad.TurnState()}, nil
}

// --- pad_get ---

type getInput struct {
	Ref      string `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Password string `json:"password,omitempty" jsonschema:"the pad's password, required when it is protected"`
}

type getOutput struct {
	Ref          string          `json:"ref"`
	Project      string          `json:"project"`
	CreatedTS    int64           `json:"created_ts"`
	SectionCount int             `json:"section_count"`
	LastAuthor   string          `json:"last_author"`
	LastTS       int64           `json:"last_ts"`
	Protected    bool            `json:"protected"`
	Turn         store.Turn      `json:"turn"`
	Sections     []store.Section `json:"sections"`
}

func (s *Server) padGet(_ context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, getOutput, error) {
	pad, err := s.store.Get(in.Ref, in.Password)
	if err != nil {
		return nil, getOutput{}, err
	}
	last := pad.Last()
	return nil, getOutput{
		Ref:          pad.Ref(),
		Project:      pad.Project,
		CreatedTS:    pad.CreatedTS,
		SectionCount: len(pad.Sections),
		LastAuthor:   last.Author,
		LastTS:       last.TS,
		Protected:    pad.Protected(),
		Turn:         pad.TurnState(),
		Sections:     tocOnly(pad.Sections),
	}, nil
}

// --- pad_read ---

type readInput struct {
	Ref      string `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Section  int    `json:"section,omitempty" jsonschema:"read exactly this one section number"`
	Since    int    `json:"since,omitempty" jsonschema:"read every section numbered above this; omit both section and since for the whole pad"`
	Password string `json:"password,omitempty" jsonschema:"the pad's password, required when it is protected"`
}

type readOutput struct {
	Ref      string          `json:"ref"`
	Sections []store.Section `json:"sections"`
}

func (s *Server) padRead(_ context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, readOutput, error) {
	sections, ref, err := s.readSections(in)
	if err != nil {
		return nil, readOutput{}, err
	}
	return nil, readOutput{Ref: ref, Sections: sections}, nil
}

// readSections applies pad_read's selection semantics: section = exactly one,
// since = everything after, neither = all (passing both is rejected).
func (s *Server) readSections(in readInput) ([]store.Section, string, error) {
	if in.Section != 0 && in.Since != 0 {
		return nil, "", &store.CodedError{Code: store.CodeInvalidInput, Msg: "pass either section or since, not both"}
	}
	pad, err := s.store.Get(in.Ref, in.Password)
	if err != nil {
		return nil, "", err
	}
	switch {
	case in.Section != 0:
		for _, sec := range pad.Sections {
			if sec.N == in.Section {
				return []store.Section{sec}, pad.Ref(), nil
			}
		}
		return nil, "", &store.CodedError{Code: store.CodeInvalidInput,
			Msg: "pad " + pad.Ref() + " has no section " + strconv.Itoa(in.Section) + " (last is " + strconv.Itoa(pad.Last().N) + ")"}
	case in.Since != 0:
		var out []store.Section
		for _, sec := range pad.Sections {
			if sec.N > in.Since {
				out = append(out, sec)
			}
		}
		return out, pad.Ref(), nil
	default:
		return pad.Sections, pad.Ref(), nil
	}
}

// --- pad_wait ---

type waitInput struct {
	Ref      string `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Since    int    `json:"since" jsonschema:"the last section number you have seen; the call returns when a higher-numbered section exists"`
	TimeoutS int    `json:"timeout_s,omitempty" jsonschema:"max seconds to wait (server-capped; see the deployment's wait config, default cap 300); omit for the default"`
	Password string `json:"password,omitempty" jsonschema:"the pad's password, required when it is protected"`
}

type waitOutput struct {
	Ref          string          `json:"ref"`
	Changed      bool            `json:"changed"`
	SectionCount int             `json:"section_count"`
	LastAuthor   string          `json:"last_author"`
	Sections     []store.Section `json:"sections,omitempty"`
}

func (s *Server) padWait(ctx context.Context, _ *mcp.CallToolRequest, in waitInput) (*mcp.CallToolResult, waitOutput, error) {
	timeout := time.Duration(in.TimeoutS) * time.Second
	if in.TimeoutS <= 0 {
		timeout = time.Duration(s.cfg.Wait.DefaultS) * time.Second
	}
	if max := time.Duration(s.cfg.Wait.MaxS) * time.Second; timeout > max {
		timeout = max // clamp, never error: the cap is a server property, not caller misuse
	}
	pad, changed, err := s.store.Wait(ctx, in.Ref, in.Password, in.Since, timeout)
	if err != nil {
		return nil, waitOutput{}, err
	}
	out := waitOutput{
		Ref:          pad.Ref(),
		Changed:      changed,
		SectionCount: len(pad.Sections),
		LastAuthor:   pad.Last().Author,
	}
	if changed {
		for _, sec := range pad.Sections {
			if sec.N > in.Since {
				out.Sections = append(out.Sections, sec)
			}
		}
	}
	return nil, out, nil
}

// --- pad_list ---

type listInput struct {
	Project string `json:"project,omitempty" jsonschema:"only list pads of this project; omit for all projects"`
}

type listOutput struct {
	Pads []store.PadMeta `json:"pads"`
}

func (s *Server) padList(_ context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, listOutput, error) {
	pads, _, err := s.store.List(in.Project)
	if err != nil {
		return nil, listOutput{}, err
	}
	if pads == nil {
		pads = []store.PadMeta{}
	}
	return nil, listOutput{Pads: pads}, nil
}

// --- project_list ---

// emptyInput is the argument type for tools that take no parameters. AddTool
// requires a struct so the inferred schema is an object.
type emptyInput struct{}

type projectListOutput struct {
	Projects []store.ProjectInfo `json:"projects"`
}

func (s *Server) projectList(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, projectListOutput, error) {
	projects, err := s.store.Projects()
	if err != nil {
		return nil, projectListOutput{}, err
	}
	if projects == nil {
		projects = []store.ProjectInfo{}
	}
	return nil, projectListOutput{Projects: projects}, nil
}
