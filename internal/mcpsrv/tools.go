package mcpsrv

import (
	"context"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/store"
)

// Every tool here is a TRANSLATOR: it turns tool parameters into a pad.Selector or a
// store request, calls the shared logic, and serialises the answer. No tool walks a
// section list or decides what a pad means — that lives in internal/pad, so the CLI,
// the Web UI and these tools cannot drift apart on "whose turn is it" or "is T3 done".

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
	p, password, err := s.store.CreatePad(project, in.Author, in.Title, in.Content, in.Protect)
	if err != nil {
		return nil, createOutput{}, err
	}
	return nil, createOutput{
		Ref:      p.Ref(),
		Project:  p.Project,
		PadID:    p.ID,
		Section:  1,
		Next:     2,
		Password: password,
		Turn:     p.TurnState(),
	}, nil
}

// --- pad_post ---

type postInput struct {
	Ref      string   `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Author   string   `json:"author" jsonschema:"your self-declared identity; must differ from the last MESSAGE section's author (turn rule)"`
	Title    string   `json:"title" jsonschema:"one-line title of this section (shows up in the pad's table of contents)"`
	Content  string   `json:"content" jsonschema:"markdown body of the section"`
	Password string   `json:"password,omitempty" jsonschema:"the pad's password, required when it was created with protect:true"`
	To       []string `json:"to,omitempty" jsonschema:"authors this section is addressed to; everyone can still READ it, but only these are woken by wake_for:me. Omit to broadcast to the whole pad"`
	Re       int      `json:"re,omitempty" jsonschema:"the section number this one answers; it also addresses that section's author, so a reply needs no 'to'"`
	TaskOpen bool     `json:"task_open,omitempty" jsonschema:"open a NEW task: the server allocates its number and returns it. Requires 'to' — a task must have an owner"`
	Task     int      `json:"task,omitempty" jsonschema:"the number of an EXISTING task this section concerns; combine with status to move it, or use it alone on a message that merely references the task"`
	Status   string   `json:"status,omitempty" jsonschema:"move the task to open, wip, blocked, done or dropped. Setting this is what makes the section a task EVENT — exempt from the turn rule, part of the task's record, and the owner's answer for it. Only its owners (their own slice) or its opener (reassign/drop/force-close) may set it"`
}

type postOutput struct {
	Ref      string     `json:"ref"`
	Section  int        `json:"section"`
	Next     int        `json:"next"`
	Task     int        `json:"task,omitempty"`
	Turn     store.Turn `json:"turn"`
	Warnings []string   `json:"warnings,omitempty"`
}

func (s *Server) padPost(_ context.Context, _ *mcp.CallToolRequest, in postInput) (*mcp.CallToolResult, postOutput, error) {
	meta := pad.Meta{To: in.To, Re: in.Re, Task: in.Task, Status: pad.Status(in.Status)}
	// What makes a section part of a task's RECORD is that it moves the work: opening
	// the task, or reporting a status on it. A bare `task` is the other layer — a
	// message that merely cross-references the work — and it stays a message, so it
	// takes the turn like any other remark and never counts as the owner's answer.
	if in.TaskOpen || in.Status != "" {
		meta.Kind = pad.KindTask
	}
	res, err := s.store.Post(store.PostRequest{
		Ref: in.Ref, Author: in.Author, Title: in.Title, Content: in.Content,
		Password: in.Password, Meta: meta, OpenTask: in.TaskOpen,
	})
	if err != nil {
		return nil, postOutput{}, err
	}
	return nil, postOutput{
		Ref: res.Pad.Ref(), Section: res.Section, Next: res.Section + 1,
		Task: res.Task, Turn: res.Pad.TurnState(), Warnings: res.Warnings,
	}, nil
}

// --- pad_get ---

type getInput struct {
	Ref      string `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Password string `json:"password,omitempty" jsonschema:"the pad's password, required when it is protected"`
	Author   string `json:"author,omitempty" jsonschema:"your identity; adds an 'inbox' of what was addressed to you since your own last post, and what you owe or are owed"`
	Kind     string `json:"kind,omitempty" jsonschema:"limit the table of contents to one stream: 'message' or 'task'"`
}

type getOutput struct {
	Ref          string            `json:"ref"`
	Project      string            `json:"project"`
	CreatedTS    int64             `json:"created_ts"`
	SectionCount int               `json:"section_count"`
	Authors      []string          `json:"authors"`
	LastAuthor   string            `json:"last_author"`
	LastTS       int64             `json:"last_ts"`
	Protected    bool              `json:"protected"`
	Turn         store.Turn        `json:"turn"`
	Sections     []store.Section   `json:"sections"`
	Participants []pad.Participant `json:"participants"`
	Inbox        *pad.Inbox        `json:"inbox,omitempty"`
}

func (s *Server) padGet(_ context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, getOutput, error) {
	p, err := s.store.Get(in.Ref, in.Password)
	if err != nil {
		return nil, getOutput{}, err
	}
	last := p.Last()
	out := getOutput{
		Ref:          p.Ref(),
		Project:      p.Project,
		CreatedTS:    p.CreatedTS,
		SectionCount: len(p.Sections),
		Authors:      p.Authors(),
		LastAuthor:   last.Author,
		LastTS:       last.TS,
		Protected:    p.Protected(),
		Turn:         p.TurnState(),
		// The TOC carries each section's routing metadata, which turns a list of titles
		// into a map of the conversation: a returning agent sees who addressed whom and
		// which sections are its own thread, then reads only those.
		Sections:     pad.TOC(p.Select(pad.Selector{Kind: pad.Kind(in.Kind)}).Sections),
		Participants: p.Participants(),
	}
	if in.Author != "" {
		inbox := p.Inbox(in.Author)
		out.Inbox = &inbox
	}
	return nil, out, nil
}

// --- pad_read ---

type readInput struct {
	Ref      string `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Section  int    `json:"section,omitempty" jsonschema:"read exactly this one section number"`
	Since    int    `json:"since,omitempty" jsonschema:"read every section numbered above this; omit both section and since for the whole pad"`
	Kind     string `json:"kind,omitempty" jsonschema:"limit to one stream: 'message' or 'task'"`
	Task     int    `json:"task,omitempty" jsonschema:"read one task's thread: the section that opened it and everything referencing it, without the pad around it"`
	Password string `json:"password,omitempty" jsonschema:"the pad's password, required when it is protected"`
}

type readOutput struct {
	Ref      string          `json:"ref"`
	Sections []store.Section `json:"sections"`
}

func (s *Server) padRead(_ context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, readOutput, error) {
	if in.Section != 0 && in.Since != 0 {
		return nil, readOutput{}, &store.CodedError{Code: store.CodeInvalidInput, Msg: "pass either section or since, not both"}
	}
	p, err := s.store.Get(in.Ref, in.Password)
	if err != nil {
		return nil, readOutput{}, err
	}
	res := p.Select(pad.Selector{
		Section: in.Section, Since: in.Since, Kind: pad.Kind(in.Kind), Task: in.Task,
	})
	if in.Section != 0 && len(res.Sections) == 0 {
		return nil, readOutput{}, &store.CodedError{Code: store.CodeInvalidInput,
			Msg: "pad " + p.Ref() + " has no section " + strconv.Itoa(in.Section) + " (last is " + strconv.Itoa(p.Last().N) + ")"}
	}
	return nil, readOutput{Ref: p.Ref(), Sections: res.Sections}, nil
}

// --- pad_wait ---

type waitInput struct {
	Ref      string   `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Since    int      `json:"since" jsonschema:"the last section number you have seen; the call returns when a higher-numbered section MATCHES your selectors"`
	TimeoutS int      `json:"timeout_s,omitempty" jsonschema:"max seconds to wait (server-capped; see the deployment's wait config, default cap 300); omit for the default"`
	Password string   `json:"password,omitempty" jsonschema:"the pad's password, required when it is protected"`
	Author   string   `json:"author,omitempty" jsonschema:"your identity; required by the me/mine selectors, and it also stops your own post from waking you"`
	WakeFor  []string `json:"wake_for,omitempty" jsonschema:"what should WAKE you (you can always read everything either way): 'any' (default), 'me' (addressed to you, replying to you, or broadcast), 'mine' (task events on tasks you own), 'tasks' (any task event), 'task:<n>' (one task, whoever owns it). They union"`
	UnackedS int      `json:"unacked_s,omitempty" jsonschema:"also return when something YOU addressed has gone unanswered this long, so a wait cannot hang forever on an agent that was never listening"`
}

type waitOutput struct {
	Ref          string          `json:"ref"`
	Changed      bool            `json:"changed"`
	Reason       string          `json:"reason,omitempty"`
	SectionCount int             `json:"section_count"`
	LastAuthor   string          `json:"last_author"`
	Sections     []store.Section `json:"sections,omitempty"`
	Skipped      []store.Section `json:"skipped,omitempty"`
	Unacked      []store.Owed    `json:"unacked,omitempty"`
}

func (s *Server) padWait(ctx context.Context, _ *mcp.CallToolRequest, in waitInput) (*mcp.CallToolResult, waitOutput, error) {
	timeout := time.Duration(in.TimeoutS) * time.Second
	if in.TimeoutS <= 0 {
		timeout = time.Duration(s.cfg.Wait.DefaultS) * time.Second
	}
	if max := time.Duration(s.cfg.Wait.MaxS) * time.Second; timeout > max {
		timeout = max // clamp, never error: the cap is a server property, not caller misuse
	}
	wake, err := pad.ParseWake(in.WakeFor)
	if err != nil {
		return nil, waitOutput{}, err
	}
	res, err := s.store.Wait(ctx, store.WaitRequest{
		Ref: in.Ref, Password: in.Password, Since: in.Since, Author: in.Author,
		Wake: wake, Timeout: timeout, Unacked: time.Duration(in.UnackedS) * time.Second,
	})
	if err != nil {
		return nil, waitOutput{}, err
	}
	return nil, waitOutput{
		Ref:          res.Pad.Ref(),
		Changed:      res.Changed,
		Reason:       res.Reason,
		SectionCount: len(res.Pad.Sections),
		LastAuthor:   res.Pad.Last().Author,
		Sections:     res.Matched,
		// Always the full table of contents of what did NOT match: waking is selective,
		// catch-up is not. An agent that wakes with a silent gap answers from stale
		// context, which is the failure the selectors exist to prevent.
		Skipped: res.Skipped,
		Unacked: res.Unacked,
	}, nil
}

// --- pad_tasks ---

type tasksInput struct {
	Ref      string `json:"ref" jsonschema:"pad reference, e.g. 'projectx-abc123'"`
	Task     int    `json:"task,omitempty" jsonschema:"one task's number; also returns its thread (the opening section and everything referencing it)"`
	OpenOnly bool   `json:"open_only,omitempty" jsonschema:"list only tasks that still need attention"`
	Password string `json:"password,omitempty" jsonschema:"the pad's password, required when it is protected"`
}

type tasksOutput struct {
	Ref    string          `json:"ref"`
	Tasks  []store.Task    `json:"tasks"`
	Thread []store.Section `json:"thread,omitempty"`
}

// padTasks answers "where is the team at" without reading the pad. It is a fold over
// metadata, so it never materialises a body — which is what makes it affordable to ask
// for on a 600-section pad, and therefore worth having at all.
//
// There is no pad_task_update: a task moves by pad_post, so the agent surface stays
// append-only.
func (s *Server) padTasks(_ context.Context, _ *mcp.CallToolRequest, in tasksInput) (*mcp.CallToolResult, tasksOutput, error) {
	p, err := s.store.Get(in.Ref, in.Password)
	if err != nil {
		return nil, tasksOutput{}, err
	}
	out := tasksOutput{Ref: p.Ref(), Tasks: []store.Task{}}
	if in.Task > 0 {
		t, ok := p.Task(in.Task)
		if !ok {
			return nil, tasksOutput{}, &store.CodedError{Code: store.CodeNoSuchTask,
				Msg: "pad " + p.Ref() + " has no task T" + strconv.Itoa(in.Task)}
		}
		out.Tasks = append(out.Tasks, t)
		out.Thread = p.Thread(in.Task)
		return nil, out, nil
	}
	for _, t := range p.Tasks() {
		if in.OpenOnly && !t.Open() {
			continue
		}
		out.Tasks = append(out.Tasks, t)
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
