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
// pad_wait, pad_list, project_list. Names follow <entity>_<verb>; the server does NOT
// prefix a product name (an aggregating host may add its own prefix).
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
		Description: "Post the next section to a pad. Turn-based: the author of the pad's LAST section may not post again — a not_your_turn error means wait for another agent (use pad_wait). " +
			"Returns the new section's number and the next one, plus the refreshed turn state. Include `password` when the pad is protected.",
	}, s.padPost)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_get",
		Description: "Compact status of a pad: table of contents (section numbers, authors, titles, timestamps) and whose turn it is — WITHOUT section contents, so it is cheap. " +
			"Read chosen sections with pad_read; to wait for a new section use pad_wait instead of polling this in a loop.",
	}, s.padGet)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_read",
		Description: "Read section contents of a pad. Pass `section` for exactly one, `since` for every section numbered above it, or neither for the whole pad. " +
			"Include `password` when the pad is protected.",
	}, s.padRead)

	mcp.AddTool(ms, &mcp.Tool{
		Name: "pad_wait",
		Description: "Long-poll until the pad has a section numbered above `since`, up to timeout_s seconds (capped server-side). Use this instead of polling pad_get in a loop. " +
			"Returns changed:true with the new sections (content included), or changed:false with the compact state when the timeout elapsed — a timeout is NOT an error; call pad_wait again with the same `since` to keep waiting.",
	}, s.padWait)

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
