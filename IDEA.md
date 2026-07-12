# Scratchpad

A CLI + MCP application that helps AI Agents communicate with each other by providing "files" through which the AIs pass content turn by turn.

## Problem

When AI Agents want to communicate with each other, they have to go through the user: the user copies questions/answers between chat sessions — tedious, error-prone, and time-consuming. Scratchpad stands in for the user, providing a platform for AIs to communicate directly:

1. **Fast**: no need to wait for the user to copy/paste, the AIs ask each other directly
2. **Accurate**: the AI asks for exactly what it wants and receives the answer directly, without the user's interpretation
3. **Continuous**: multiple back-and-forth rounds without waiting for the user

## How it feels in practice

Scratchpad is an "instant" product — a standalone binary, no complex setup, minimally letting 2 agents communicate right away. The user's role is only to pass the file ID between the two sessions:

1. The user, in one agent's session (frontend): *"use Scratchpad to ask Backend"* → the agent creates a file and returns the ID
2. The user, over in another agent's session (backend): *"Frontend is asking you in file `projectx-abc123`, use Scratchpad to view and answer it"*
3. The user's job is done — the two agents continue communicating on their own.

## Concepts

### Project

- Separates different projects to avoid confusion. Each project is one folder on disk.
- There is a default project named `default`. The AI does not need to declare a project beforehand — it is created automatically when creating a file if it does not exist yet.
- Project names may only contain `a-z`, `0-9` — **no `-` or `_`**, because `-` is the separator in the full identifier (see below).

### File

1. Plain text (markdown), not a DB. On disk: `<project>/<fileid>.md`
2. **Append-only, no edits.** The file is the transcript of the conversation.
3. Divided into sequentially numbered **sections**. The AI can list sections (a table of contents) and read whichever section it wants. When posting, it receives back the id of the section just posted and the next id.
4. Files have no name, only a random ID. The ID uses only `a-z0-9`, avoiding characters that are easy to confuse when the user reads/types them by hand (`l`/`1`, `o`/`0`).
5. **Full identifier** for copy-pasting between sessions: `<project>-<fileid>`, e.g. `projectx-abc123`. Since the project name contains no `-`, parsing is unambiguous.
6. The `list` command displays the ID together with the title of the first section as context (the file has no name but is still recognizable).

### Section

Each post = one section, numbered in increasing order. On-disk format:

```markdown
# 1 - frontend - How does API X work
<!-- ts: 2026-07-11T10:30:00Z -->

The question content...

# 2 - backend - Answer about API X
<!-- ts: 2026-07-11T10:42:15Z -->

The answer content...
```

- The `# <number> - <author> - <title>` header is the only thing the machine needs to parse. The title is set by the AI when posting — it helps the section-listing command return a meaningful table of contents.
- The timestamp sits inside an HTML comment to keep it out of the reader's way.
- Strict parsing rule: only a line matching the exact pattern `# <number> - ` counts as a section header. Content containing other `# ...` lines causes no confusion (we accept the risk of a pattern collision — very rare in practice).

## Turn mechanism (turn-based)

- Each AI has an identity within the file, **self-declared** when posting (sufficient for v1 — running locally between the user's own agents).
- Rule: **no one may post twice in a row.** The author of the last section is the one currently blocked; every other AI may post.
- **The file itself is the source of truth for turn state** — inferred from the last section, with no separate state. Consequence: restarting the server loses nothing; a user manually deleting the last section automatically "returns the turn."
- The `.lock` file (flock) only serves as a **write mutex** when appending (the CLI and the MCP server may write concurrently), carrying no turn semantics.

## Wait mechanism

After posting, an AI waits for another AI to reply. Scratchpad supports 2 kinds of waiting:

1. **MCP tool `wait`**: takes a `timeout` + `since_id` parameter. Returns one of two outcomes: *a new section exists* (with its content) or *timeout, call again to keep waiting*. The AI repeats on its own — avoiding a dead tool call when the MCP client times out while the other AI is thinking for a long time.
2. **Background CLI**: the AI invokes a CLI command that runs in the background; the command finishes when a reply arrives → triggering the AI to wake up (a good fit for harnesses like Claude Code's `run_in_background`).

## Password

- A file may have a password — **used only for access control** (the server checks the password before allowing read/write). Content is stored in plaintext; the file on disk is still readable by eye.
- The user/AI does not set the password themselves: when creating a file that requests protection (`protect`), Scratchpad **generates the password itself and returns it once** in the creation result.
- The password is exchanged through the user's channel: the user (the coordinator) pastes the password over to the other AI along with the file ID.
- **No content encryption at this stage.** The file format leaves room for the future (e.g. an `encrypted: true` flag per section) in case client-side encryption is needed later.

## Usage interface

- The AI uses Scratchpad via **CLI** or **MCP** (http / socket / stdio).
- Scratchpad is a small CLI, **with no UI**. Because there is a CLI, the user can also view and clean up.
- The default directory `~/.scratchpad/` **bootstraps itself on first use** — usable immediately with no setup. The `init` command is only needed when you want a custom directory (specified by flag/env).
- Over http: v1 runs plaintext in a local/trusted network; if SSL is needed, put a reverse proxy (Caddy/nginx) in front — Scratchpad does not handle transport encryption itself.
