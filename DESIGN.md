# Scratchpad — CLI & MCP tools design

See IDEA.md for the overall concept.

## Terminology

- The primary entity is named **`pad`** (a "file" in IDEA.md) — this avoids clashing with the word "file", which is overloaded in the tool context. On disk it is still a file `<project>/<padid>.md`.
- **`section`**: a single post within a pad, numbered incrementally starting from 1.
- **Ref** (the fully-qualified identifier): `<project>-<padid>`, e.g. `projectx-abc123`.

## MCP tools

Following the convention: names are `<entity>_<verb>` in snake_case, and **the server does not prefix the product name itself** (the aggregator/proxy attaches `scratchpad_*`). Input is a Go struct with a `jsonschema` tag, and descriptions are written thoroughly for agents to read. Every mutation returns the refreshed object in a single round-trip.

### List

| Tool | Role |
|---|---|
| `pad_create` | Create a new pad + post section 1 |
| `pad_post` | Post a new section (turn-based) |
| `pad_get` | Compact state: TOC + turn — does NOT return content, cheap to poll |
| `pad_read` | Read section content |
| `pad_wait` | Long-poll waiting for a new section (timeout capped) |
| `pad_list` | List pads |
| `project_list` | List projects + pad count |

There is no `pad_delete` / `pad_update` over MCP: a pad is append-only, and deletion/cleanup is the user's job via the CLI.

### `pad_create`

```
input:  { project?, author, title, content, protect? }
output: { ref, project, pad_id, section: 1, next: 2, password?, turn: {...} }
```

- Omitting `project` → the default project (env `SCRATCHPAD_PROJECT_NAME` → config `default_project` → `"default"`). The project is auto-created if it does not exist (validated against `a-z0-9`).
- **There is no password input.** To protect a pad, use `protect: true` — **the server generates the password itself** and returns it exactly once in the create result; the user/agent does not have to think up a password. Every subsequent call on this pad must include that password (the user passes it to the other agent along with the ref).
- The server generates a random `pad_id` (`a-z0-9`, avoiding easily-confused characters) and returns the complete `ref` for the user to copy-paste into another session.

### `pad_post`

```
input:  { ref, author, title, content, password? }
output: { ref, section, next, turn: { last_author, blocked: [author], waiting_for: "any other" } }
```

- Enforces the turn rule: if the author of the last section == `author` → a clear error
  `"not your turn: you posted section N; wait for another agent (use pad_wait)"`. A timeout or other error does not consume a turn.
- Returns the section id just posted + the next id (matching IDEA.md).

### `pad_get`

```
input:  { ref, password? }
output: { ref, project, created_ts, section_count, last_author, last_ts,
          sections: [ { n, author, title, ts } ] }   # TOC only, no content
```

Compact by design: cheap, transfers no content. The agent looks at the TOC and then decides which section to read.

### `pad_read`

```
input:  { ref, password?, section?, since? }
output: { ref, sections: [ { n, author, title, ts, content } ] }
```

- `section` = read exactly 1 section; `since` = every section with n > since; omitting both = the entire pad.
- Content is capped at 64KB/section (see Limits), so returning it through the tool result is valid (text, bounded — not out-of-band file bytes).

### `pad_wait`

```
input:  { ref, since, timeout_s?, password? }
output: { ref, changed: bool, section_count, last_author,
          sections?: [ { n, author, title, ts, content } ] }   # the new sections if changed
```

Following a standard long-poll pattern:

- `timeout_s` defaults to 60, **capped server-side at 300s** (safe with respect to an MCP client's per-request timeout); ≤0 or exceeding the cap → clamped, not an error.
- Internally it re-checks periodically (~750ms), it does not push.
- **A timeout is not an error**: it returns `changed: false` + compact state — the agent distinguishes "time ran out" from "broken", and calls again itself if it wants to keep waiting.
- The description teaches the agent: "Use this instead of polling pad_get in a loop".

### `pad_list`

```
input:  { project? }
output: { pads: [ { ref, project, title, section_count, last_author, last_ts, protected: bool } ] }
```

`title` = the title of section 1 (a pad has no name, so it borrows context from the opening question). A pad with a password still appears in the list (metadata), but its content cannot be read without the password.

### Identity (author)

The author is always **self-declared, from a single source**: the `author` param (MCP) or `--as`/env `SCRATCHPAD_AUTHOR` (CLI). There is no identity mechanism from the host (the `session_meta_key` and the `whoami` tool have been dropped): both the CLI and MCP independently need the author specified, and adding a second identity source from session-meta only causes confusion and redundancy — that mechanism belongs to tools that have an auth service, not Scratchpad.

### Common error semantics

- `not_your_turn` — includes who is currently blocked, and suggests `pad_wait`.
- `pad_not_found` — the ref is wrong / it was deleted by the user.
- `unauthorized` — the pad has a password that is missing or wrong (a single unified message, not distinguishing the two cases).
- `content_too_large`, `invalid_project_name`, `invalid_ref` — validation, with a message that states the rule clearly.

### Limits (every resource is bounded)

| Limit | Value |
|---|---|
| `title` | 4KB |
| `content` per section | 64KB |
| Sections / pad | 1000 |
| Pads / project | 1000 |
| `timeout_s` of `pad_wait` | cap 300s |

## CLI

- **Binary: `scratchpad`**; the name in help/error is **derived from the executable** (appinfo), not baked into the message. Framework: **cobra** (HARD RULE).
- **Unified env prefix: `SCRATCHPAD_`**, with each env having a corresponding flag.
- Name-neutral: no host names baked in; host integration goes through generic flags.

### Command tree

```
scratchpad
├── init                 # initialize a CUSTOM dir (flag/env); the default dir self-bootstraps, so init is not required
├── serve                # MCP server: UDS by default; --stdio; --tcp opt-in
├── doctor               # diagnostics, strictly read-only (see the Doctor section)
├── skills               # self-documenting docs (go:embed); skills docs <topic>; -o json
├── version
└── pad
    ├── create   --project <p> --as <author> --title <t> [--protect] [content | -]
    ├── post     <ref> --as <author> --title <t> [--password] [content | -]
    ├── get      <ref>                    # TOC + turn (compact)
    ├── read     <ref> [--section N | --since N]
    ├── wait     <ref> --since N [--timeout 10m]   # for a background CLI wait
    ├── list     [--project <p>]
    ├── delete   <ref>                    # confirm with a human, --yes for automation
    └── purge    [--project <p>] --older-than <dur>   # bulk cleanup, confirm/--yes
```

Notes:

- `content` is taken via an arg or stdin (`-`) — this avoids shell-escaping issues with long content.
- `--as` defaults from env `SCRATCHPAD_AUTHOR` (convenient to set once for a whole agent session).
- **`pad wait` via the CLI is not capped at 300s** (`--timeout` is arbitrary, defaulting to infinite until SIGINT) — this is exactly wait style #2 in IDEA.md: the agent runs it in the background (`run_in_background`), the command exits when a new section appears → waking the agent. Exit codes: 0 = a new section exists (printed to stdout), 3 = timed out. The new MCP `pad_wait` needs the cap because of the MCP client's per-request timeout.
- `delete`/`purge` follow the interactivity convention: prompt with a human (TTY), fail-fast with a process, `--yes`/`--non-interactive` to override; there is a root flag `--non-interactive` + env `SCRATCHPAD_NONINTERACTIVE`.
- The `pad *` commands operate **directly on disk** through a shared storage layer (flock when writing) — no running server is needed. The server and CLI share the same storage package, so they share the same lock discipline.

### Scratchpad directory

A single self-contained directory holds everything: config, pads, socket. **Default: `~/.scratchpad/`** — when nothing is specified, every command uses it and **self-bootstraps on first use** (creating the dir + marker + config.md + `projects/`). Self-bootstrapping the default is safe because the path is fixed relative to home, not dependent on cwd — cwd-relative paths are a classic source of stray stores, and here that is eliminated at the root: **there is no cwd-based inference**.

Specifying somewhere else (precedence high → low):

1. Flag `--dir <path>`
2. Env `SCRATCHPAD_DIR` — for projects that need their own storage (e.g. set in a repo's `.envrc`)
3. The `dir` field in the config file at `~/.scratchpad/` — a fixed machine-wide storage relocation
4. Default `~/.scratchpad/`

An explicitly-specified dir (flag/env/config) that has not been initialized → **a clear error pointing to `init`**, not auto-creation — a typo in an env must not be allowed to spawn a stray store. Only the default path is self-bootstrapped; `init` is therefore only needed for a custom dir or provisioning.

The standard need for "running the CLI from any folder in a repo lands in the same place" is met by the default dir; separation between projects uses a **project** rather than a separate store — a repo that sets `SCRATCHPAD_PROJECT_NAME` has every pad it creates land in the right project.

### Environment variables

| Env | Meaning | Default |
|---|---|---|
| `SCRATCHPAD_DIR` | Where all of Scratchpad's files live | `~/.scratchpad/` |
| `SCRATCHPAD_PROJECT_NAME` | The default project when a command/tool does not pass `project` | `default` |
| `SCRATCHPAD_AUTHOR` | The default author for `--as` | — |
| `SCRATCHPAD_NONINTERACTIVE` | Disable prompts (automation) | — |

Every env has a corresponding flag; on conflict, **flag > env > config file > default**.

```
~/.scratchpad/                   # the Scratchpad directory, self-contained, 0700
├── scratchpad.config.json      # marker + settings (see Marker file contents)
├── config.md                       # config guide, go:embed from a separate source file in the repo
├── scratchpad.sock             # unix socket, 0600 — derived from dir, not configured separately
└── projects/
    ├── default/
    │   └── ab3k9x.md
    └── projectx/
        └── abc123.md
```

### Marker file contents

```json
{
  "type": "scratchpad",
  "version": 1,

  "display_name": "Scratchpad",
  "instance": "scratchpad",

  "dir": "",
  "default_project": "default",

  "limits": {
    "max_title_kb": 4,
    "max_content_kb": 64,
    "max_sections_per_pad": 1000,
    "max_pads_per_project": 1000
  },
  "wait": { "default_s": 60, "max_s": 300 },

  "tcp": {
    "port": 6710,
    "token_digests": ["sha256:..."],
    "allowed_origins": []
  }
}
```

- **Required header**: `type` (fixed, a recognition guard) + `version` (the marker's schema version).
- **Identity group**: `display_name` (the human-facing display name — deliberately *not* `project_name`, because "project" already means something different in Scratchpad), `instance` (a technical label: the socket name).
- **Storage/behavior group**: `dir` (optional — relocate storage elsewhere, meaningful only in a config at the default location; this is a deliberate exception to the "do not store paths" rule, acting as a pointer that the user requested be configurable via the config file), `default_project` (the default project, overridden by env `SCRATCHPAD_PROJECT_NAME`/flag).
- **Optional group, omit = default**: `limits`, `wait`, `tcp`. `init` writes only the header + identity; the optional groups are added by the operator when needed (the defaults are explained in `config.md`). `tcp.token_digests` stores only the SHA-256 digest, never the raw token; once `tcp` is in the file, `serve --tcp` does not need the flag repeated (flags still win over the file on conflict).

**Not stored in config**: paths (`projects/`, socket — derived from dir); author (per-agent, belonging to each session's env `SCRATCHPAD_AUTHOR`); a pad's password (belonging to each pad file's header — a pad is self-contained, `rm` cleans it, leaving no cruft in config).

- The marker has `type` + `version`: it rejects unknown files and refuses a version newer than the binary. The fixed identifiers (marker name, `type` value, candidate dir name) **do not change with the binary name**; conversely, every "run `X …`" message derives the command name from the running executable.
- Data (`projects/`) and the socket are all **derived from dir** — moving the dir moves everything.
- `init` is used for a custom dir (`--dir`/env) or provisioning; interactively it confirms before creating, and refuses to clobber an existing marker. The default dir needs no `init` (it self-bootstraps).
- Docs discipline: `config.md` is a separate source file in the repo (embedded into the binary), its content is purely user-facing — it explains each field of the marker, the resolution order, and the env vars, so that whoever opens the dir 6 months later understands it on their own. The rule "changing the config schema means updating the guide + bumping `version` in the same change" lives in the repo's `CLAUDE.md` (maintainer-facing), not in the deploy guide.

### Doctor

Following `doctor-command.md`: **strictly no side effects** — stat before opening, open read-only, never create the thing it is checking. Unlike every other command, `doctor` **does not error when the config dir cannot be resolved** — that is exactly what is being diagnosed; it reports the resolution process (the flag/env values, the candidate dirs it probed, and which dir has a marker).

Reports:

- **Resolution**: the running binary (its real path, symlinks resolved), version, `on PATH` (resolving to this exact file), a **PATH shadow** on its own line if the command name resolves to a different file (compared by inode/`os.SameFile`, **never executing** the file found); cwd, the config dir + the winning source, the marker path, and the derived `projects/` and socket paths.
- **Store**: does `projects/` exist? is it writable? the number of projects/pads (counted by stat/list, changing nothing); can the last pad file be parsed (read read-only).
- Opt-in: `--content` (lists each pad's ref + section count), `--verdict` (a conclusion + next steps, walking through failure modes from the outside in), `--json`.

### Output streams (hard rules)

- Under `serve --stdio`: **stdout belongs to JSON-RPC**, and all logs/diagnostics go to stderr. The startup line "using config dir …" also goes to stderr. Test: `scratchpad serve --stdio >/tmp/out 2>/tmp/err` → `/tmp/out` must be empty.
- Operator commands (`pad *`, `init`, `doctor`): the **result** goes to stdout (clean, pipeable — important for `pad read`/`pad wait` when an agent parses the output), and **chatter/warnings** go to stderr.
- Never execute a file discovered on the filesystem (not even to ask its version); only `Stat`/`LookPath`/`SameFile`.

### Transports (`serve`)

1. **Default: Streamable HTTP over a Unix Domain Socket** in the config dir (ADR-008) — no TCP port is opened, protected by filesystem permissions + peer-credential (uid == getuid, fail-closed).
2. **`--stdio`**: a compatibility mode for hosts that spawn it themselves (Claude Code, Codex…). stdout belongs to JSON-RPC, and all logs/diagnostics go to stderr.
3. **`--tcp`** opt-in: loopback-only, a bearer token is mandatory (stored as a SHA-256 digest), Origin/Host guard, port in the 67xx range. This is the path for the "2 agents on 2 machines" case — for TLS, put a reverse proxy in front.

Lifecycle: supervised by the node/host (it does not daemonize itself), absolute paths via args/env, SIGTERM stops cleanly.

## CLI vs MCP — what lives where

Positioning: **the CLI is the primary path, self-sufficient for local use** — an agent with a shell only needs the binary, operating directly on disk, with no running server needed. **MCP is for special use cases**: embedding into an MCP host (via UDS), an agent/host that cannot spawn the CLI, cross-machine access via TCP, and tool discovery (the agent sees the schema + description right away, with no need to read docs).

| Capability | CLI | MCP |
|---|---|---|
| Create / post / view TOC / read / list (`create` `post` `get` `read` `list`, project list) | ✅ | ✅ |
| Wait for a new section | ✅ `pad wait` — **not capped**, runs in the background, exit code wakes the agent | ✅ `pad_wait` — **capped at 300s**, the agent loops itself using `since` |
| Delete / cleanup (`delete`, `purge`) | ✅ (confirm with a human, `--yes` for automation) | ❌ — the agent surface is append-only |
| Operations (`init`, `serve`, `doctor`, `skills`, `version`) | ✅ | ❌ |
| Identity | `--as` / env `SCRATCHPAD_AUTHOR` | param `author` (self-declared, mandatory) |
| Needs a running server | ❌ — reads/writes disk directly (flock) | ✅ — needs `serve` (UDS / stdio / TCP) |
| Long content | via stdin (`-`), no shell-escaping worries | param `content`, capped at 64KB |
| Self-bootstrap the default dir | ✅ | ✅ (`serve` bootstraps when it resolves to the default) |
| Typical use case | a local agent with a shell; a user viewing, managing, cleaning up | an embedding MCP host; a host without a shell; remote via TCP |

Design consequence: the CLI and MCP use **one shared storage layer** (the same flock discipline), so they can be mixed — one agent uses the CLI, another uses MCP on the same store, and the turns still stay correct.

## Storing a pad on disk

Each pad is one markdown file, with the metadata header as an HTML comment on the first line, followed by the sections (formatted as in IDEA.md):

```markdown
<!-- scratchpad v1; created: 2026-07-11T10:29:00Z; password: $2b$12$... -->

# 1 - frontend - How does API X work
<!-- ts: 2026-07-11T10:30:00Z -->

Content...
```

- `password` (a bcrypt hash) appears only when the pad is protected. The file remains one-file-per-pad, the user can `cat` it, and cleanup can be done with `rm` (Scratchpad treats a vanished file as a deleted pad — there is no state outside it).
- Writing: open the file with an exclusive `flock` → parse the last section → check the turn → append → release. The turn state is not stored anywhere other than the file itself.

## Key decisions vs IDEA.md

- Wait style 1 (MCP) = `pad_wait` capped at 300s, the agent loops itself; wait style 2 (background CLI) = `pad wait` uncapped, exits when there is a reply.
- The password is only access control (a bcrypt hash in the header), the content is plaintext. The user does not set the password themselves: `protect: true` at create time → the server generates it and returns it once.
- Delete/cleanup is only via the CLI (or a manual `rm`) — the agent's MCP surface is append-only in the true sense.
