// Package mcpsrv exposes the pad store as MCP tools — the entire agent-facing surface.
// It is deliberately append-only: there is no pad_delete/pad_update tool; cleanup is a
// human task done through the CLI (or plain rm). Identity is self-declared: every
// writing tool takes an `author` param — there is no host-provided identity mechanism,
// by design (this tool has no auth service behind it).
package mcpsrv

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
)

// Server holds the dependencies the tool handlers need. It is transport-agnostic: the
// same Server is registered onto an MCP server whether it runs over the Unix socket,
// stdio, or the opt-in TCP listener.
type Server struct {
	store *store.Store
	cfg   config.Config
}

// New builds a Server over the shared storage layer. cfg supplies the default
// project, the wait bounds, and the limits already applied by the store.
func New(st *store.Store, cfg config.Config) *Server {
	return &Server{store: st, cfg: cfg}
}

// AddTools registers the full tool surface: pad_create, pad_post, pad_get, pad_read,
// pad_wait, pad_tasks, pad_list, project_list. Names follow <entity>_<verb>; the server
// does NOT prefix a product name (an aggregating host may add its own prefix).
//
// The surface stays append-only: pad_tasks reads, and a task is opened, moved and
// closed through pad_post carrying metadata, so no mutating tool joins the set.
func (s *Server) AddTools(ms *mcp.Server) {
	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_create",
		Description: "Create a new pad (a shared, append-only markdown transcript for agents to exchange messages turn by turn) and post its first section. " +
			"Returns the pad's full ref (\"<project>-<padid>\") to hand to the other agent, plus the turn state. " +
			"Omit `project` to use the deployment's default project (auto-created; names are a-z0-9 only). " +
			"Set protect:true to password-protect the pad: the server GENERATES the password and returns it exactly once in this result — pass it along with the ref; every later call on the pad must include it. " +
			"After creating, wait for the reply with pad_wait.",
	}, s.padCreate)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_post",
		Description: "Post the next section to a pad. Turn-based: the author of the pad's last MESSAGE may not post again — a not_your_turn error means wait for another agent (use pad_wait). Task events are exempt, so a coordinator can open several tasks in a row. " +
			"Address it with `to` (everyone can still read it; `to` decides who is WOKEN) and anchor it with `re` so a reader knows what it answers. " +
			"Open work with task_open:true + `to` (a task must have an owner) and move it with `task` + `status`. " +
			"Returns the new section's number, any task number, the refreshed turn state, and warnings when an addressee has been silent. Include `password` when the pad is protected.",
	}, s.padPost)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_get",
		Description: "Compact status of a pad: table of contents WITH each section's routing metadata (to, re, kind, task), whose turn it is, and per-author last activity — WITHOUT section contents, so it is cheap. " +
			"The routing metadata makes the TOC a map of the conversation: after being away, pass `author` to get your inbox (what was addressed to you since your own last post) and read only those sections instead of the whole pad. " +
			"To wait for a new section use pad_wait instead of polling this in a loop.",
	}, s.padGet)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_read",
		Description: "Read section contents of a pad. Pass `section` for exactly one, `since` for every section numbered above it, `task` for one task's whole thread, or nothing for the entire pad. " +
			"Include `password` when the pad is protected.",
	}, s.padRead)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_wait",
		Description: "Long-poll until a section above `since` MATCHES your selectors, up to timeout_s seconds (capped server-side). Use this instead of polling pad_get in a loop. " +
			"By default any new section wakes you. In a pad with several agents pass `author` + wake_for:[\"me\"] so exchanges between two OTHER agents no longer interrupt you — you can still read them; they just stop waking you. Add \"mine\" or \"task:<n>\" to follow work. " +
			"Whatever wakes you, `skipped` always lists everything you missed, so filtering never leaves you with a silent gap. " +
			"Set unacked_s so the call also returns when something YOU addressed has gone unanswered that long — otherwise a wait can hang forever on an agent that was never listening. " +
			"changed:false means the timeout elapsed and is NOT an error; call again with the same `since`.",
	}, s.padWait)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_tasks",
		Description: "The pad's task board: every task with its title, aggregate status, and PER-OWNER status (a shared task is done only when every owner is). This is how to learn where the work stands without reading a long pad. " +
			"Pass `task` for one task plus its thread. Read-only — open, move and close tasks with pad_post.",
	}, s.padTasks)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_list",
		Description: "List pads (metadata only: ref, first-section title, section count, last author/timestamp, protected flag), newest activity first, optionally filtered to one project. " +
			"Protected pads are listed too — their password gates content, not existence.",
	}, s.padList)

	mcp.AddTool(ms, &mcp.Tool{
		Name:        "project_list",
		Description: "List all projects with their pad counts. Projects are namespaces for pads (folders on disk), auto-created on first use.",
	}, s.projectList)
}
