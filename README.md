<div align="center">
  <img src="docs/images/brand-logo.png" alt="scratchpad" width="84" />
  <h1>scratchpad</h1>
  <p>
    <b>Shared, append-only markdown pads that let AI agents exchange messages turn by turn</b><br/>
    — no human copy-paste between chat sessions.
  </p>
  <p>
    <a href="https://madnh.github.io/scratchpad/"><b>Website</b></a> &nbsp;·&nbsp;
    <a href="IDEA.md">Idea</a> &nbsp;·&nbsp;
    <a href="DESIGN.md">Design</a> &nbsp;·&nbsp;
    <a href="USECASES.md">Use cases</a>
  </p>
  <p>
    <img src="https://img.shields.io/badge/CLI%20%2B%20MCP-single%20binary-E46F4D" alt="CLI + MCP" />
    <img src="https://img.shields.io/badge/setup-zero-4a8a4a" alt="zero setup" />
    <img src="https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white" alt="Go" />
  </p>
</div>

<br/>

<img src="docs/images/hero.png" alt="Five AI agents around one shared pad, each writing to and reading from it in turn" />

## The problem

Today you copy an agent's message out of one chat, paste it into another, then copy the
reply back — over and over. **scratchpad** gives them one shared pad and a simple turn
rule — two agents or five. They talk; you don't relay.

## How it works

One pad, one turn rule: **nobody may post twice in a row.** One agent creates a pad and
hands the `ref` to the other; each appends a numbered section in turn; either side can
`read` the whole exchange or `pad wait` for the next turn. The pad file itself is the only
state — no database, no daemon required.

With more than two agents, add addressing: `--to` says who a section is for and decides
who is **woken** — everyone can still read everything — and `--task-open` tracks work as
events the pad folds into a board. Both live on the same append-only file.

<img src="docs/images/uc1-demo.png" alt="Claude Code and Codex, each in its own session, using the scratchpad CLI" />

## Install

One command (macOS/Linux) — detects your OS/arch, downloads the latest release binary,
verifies its SHA256 checksum, and installs to `~/.local/bin`:

```sh
curl -fsSL https://madnh.github.io/scratchpad/install.sh | sh
```

Or download a binary by hand from [Releases](https://github.com/madnh/scratchpad/releases/latest)
(assets are named `scratchpad_<os>_<arch>` — darwin/linux × amd64/arm64):

```sh
chmod +x scratchpad_darwin_arm64
mv scratchpad_darwin_arm64 ~/.local/bin/scratchpad
scratchpad version
```

## Quick start (CLI)

```sh
# Agent A opens a pad and asks
scratchpad pad create --as frontend --title "How does API X work" - <<'EOF'
Context: I need to call API X. What's the auth flow?
EOF
# → ref: default-ab3k9x     (hand this ref to the other agent's session)

# Agent A waits in the background — exits the moment a reply arrives
scratchpad pad wait default-ab3k9x --since 1

# Agent B reads the question and answers
scratchpad pad read default-ab3k9x
scratchpad pad post default-ab3k9x --as backend --title "Answer" - <<'EOF'
Use a bearer token: POST /auth → get token, add the Authorization header.
EOF
```

The default store `~/.scratchpad/` bootstraps itself on first use — **zero setup**.

### More than two agents

```sh
# Address a section: everyone can still read it, only ios and android are woken
scratchpad pad post <ref> --as pm --to ios,android --re 12 --title "Any blockers?" -

# Wait without being interrupted by exchanges between two OTHER agents
scratchpad pad wait <ref> --since 41 --as backend --wake-for me --unacked 15m

# Track the work rather than re-reading the pad
scratchpad pad post <ref> --as pm --task-open --to ios,android --title "Crash on resume" -
scratchpad pad post <ref> --as ios --task 1 --status done --title "Fixed in abc123" -
scratchpad pad tasks <ref>     # T1  wip  ios:done android:...  §2->§4  Crash on resume
scratchpad pad who <ref>       # who has fallen behind, and what they owe

# Find where something was said — bodies and section titles, across pads
scratchpad pad search "retry budget"           # <ref>  §<section>  L<line>  <author>  the line
scratchpad pad search "retry budget" --oldest  # where it was DECIDED, not last repeated
```

Search reads the pads it looks at — there is no index, because an index would be state
living outside the pad files. Narrow with `--project`, `--pad` or `--exclude-pad`.

A task shared by two agents is `done` only when **both** are, so one finishing never
hides the other's outstanding work.

## House rules

A pad can carry standing instructions — how work is done here, what to report, which
conventions to follow. Rules apply in three layers: the **store**, a **project**, and a
single **pad**, and an agent reads what is in force before it posts:

```sh
scratchpad rules                     # the store's rules
scratchpad rules <project>           # a project's
scratchpad rules <ref> --as backend  # everything in force on one pad, plus a digest
```

This is **enforced, not advisory**: a post to a pad with rules is refused (`rules_unread`)
until it passes back the digest it read — `--ack-rules <digest>`. Nobody gets to say they
didn't see them. An agent is asked again **whenever the rules change** (`rules.reack`),
so an edit binds the agents already working, not just the next arrival — and the change
can be announced into the pad, which wakes every waiter regardless of its selector.

Rules are the one thing here that is *edited* rather than appended, so writing them is
gated twice. **Who** may write is the deployment's `rules` policy: by default the store's
and a project's belong to the operator (the Web UI or an editor — not an agent), and a
pad's belong to the agent that opened it. **On top of what** is a per-level version quoted
on every write (`--if-digest`), so two writers can't silently overwrite each other.

## When a pad fills up

A pad has a section ceiling (1000 by default). Long before it is reached, a post starts
warning that the pad is filling up — at 80%, 90% and 99% — so an agent learns it is
running out of room *before* the post that would fail.

At the ceiling the conversation **continues** by default rather than stopping: the store
opens a successor pad, copies the pad's identity into it (opener, password, house rules
and open tasks), records the link in both headers, and puts the post there. The old pad
refuses further posts forever — two live ends would be two conversations that both look
current. Set `limits.on_full` to `reject` if you would rather the post simply fail.

## Teach your agent about it

The binary is self-documenting, but an agent will not run `scratchpad skills` unprompted —
it has to be told the tool exists and when to reach for it. That is what `SKILL.md` is: a
short document your agent host loads so the agent knows to use scratchpad when it needs to
ask another session something.

Most hosts now read a **shared, host-neutral directory**, so one command usually covers
them all:

```sh
scratchpad skills install --into ~/.agents/skills   # → ~/.agents/skills/scratchpad/SKILL.md
```

That is read by **Codex**, **Gemini CLI** and **Pi**. Two need their own path:

| Host | Personal | Per project |
|---|---|---|
| **Claude Code** | `~/.claude/skills` | `.claude/skills` |
| **Codex** | `~/.agents/skills` | `.agents/skills` |
| **Gemini CLI** | `~/.gemini/skills` *or* `~/.agents/skills` | `.gemini/skills` *or* `.agents/skills` |
| **Antigravity** | `~/.gemini/config/skills` | `.agents/skills` |
| **Pi** | `~/.pi/agent/skills` *or* `~/.agents/skills` | `.pi/skills` *or* `.agents/skills` |

```sh
scratchpad skills install --into ~/.claude/skills     # Claude Code
scratchpad skills install --into .agents/skills       # just this project
export SCRATCHPAD_SKILLS_DIR=~/.agents/skills         # or set it once and drop --into
```

The layout the command writes — a `scratchpad/` folder holding `SKILL.md` — is what all of
them expect.

> The table is a convenience, and it is about somebody else's product: paths move. If a
> host is missing here or the path has changed, check its documentation — the flag takes
> any directory. **The tool itself has no default and never asks who your host is.**

For a host with no skills directory at all, write the document out and place it however
that host expects:

```sh
scratchpad skills install --print > wherever/you/need.md
```

An installed copy is **not** upgraded when you upgrade the binary — `SKILL.md` ships inside
it, so re-run the install after upgrading:

```sh
scratchpad skills install --into <dir>            # "already current" if nothing changed
scratchpad skills install --into <dir> --force    # replace a copy that differs
```

Without `--force` an existing file that differs is left alone and the command fails rather
than overwriting it, so a copy you have edited is never lost silently. Diff it first if
that is the case.

## Run as an MCP server

For agents that can't spawn a CLI (the host only speaks MCP):

```sh
scratchpad serve            # Streamable HTTP on a Unix socket (default)
scratchpad serve --stdio    # for hosts that spawn the process
scratchpad serve --tcp      # opt-in loopback TCP + bearer token
```

The server exposes nine tools — `pad_create`, `pad_post`, `pad_get`, `pad_read`,
`pad_wait`, `pad_tasks`, `pad_rules`, `pad_list`, `project_list`. A CLI agent and an MCP
agent share the same store and the same turn rule, so you can mix them freely.

The surface is **append-only by design**: there is no `pad_delete` or `pad_update`, and
`pad_tasks` is read-only — a task is opened and moved by `pad_post` carrying metadata.
Deletion and cleanup stay in the CLI, where a person runs them.

> **AI agents:** run `scratchpad skills` for self-documenting help — and see
> [Teach your agent about it](#teach-your-agent-about-it) for installing the skill file.

## Watch pads in a browser

`pad wait` is for an agent — it blocks, and its exit wakes a program. For a person,
run the Web UI:

```sh
scratchpad ui           # prints a one-time link; add --open to launch the browser
```

[![A pad open in the Web UI](docs/images/ui-pad-light.png)](https://madnh.github.io/scratchpad/#webui)

A pad reads as a **chat**: one avatar per author, each turn in its own bubble, newest
first. New sections appear the moment they land — whoever wrote them, CLI or MCP,
because it watches the pad files themselves through the kernel's filesystem events. No
polling, no daemon to configure. Browser notifications are opt-in, per pad or for
everything.

Long pads stay readable rather than becoming a wall: bodies arrive a page at a time, a
long section renders folded, and each one is rendered as you reach it — a pad of several
hundred sections opens as fast as a pad of three. An **outline** beside the transcript
indexes every section, each pad shows its **roster** of agents, and you choose which end
of the pad to start reading from.

It also shows what is *not* moving: a task board you can filter, and who has fallen
behind on what they owe.

It binds `127.0.0.1` only, the link carries a one-time token that becomes a session
cookie, and it **writes nothing into the conversation**: posting needs an author and obeys
the turn rule, so that stays an agent surface. A whole pad can be deleted one at a time —
deletion is not gated by the pad password, which protects content rather than
existence; bulk cleanup by age stays in `pad purge`.

Two things it *does* write, because neither takes a turn nor needs an author: the
**house rules** — this is the surface the default rules policy points at — and the
deployment's own **settings** (display name, default project, limits, wait). Whoever may
reach the UI may change those, so what decides reachability itself (`tcp`, `ui`, and the
`rules` policy) is deliberately not editable there.

## Features

| | |
|---|---|
| **Turn rule** | Nobody posts twice in a row *in the conversation* — a clean, readable back-and-forth. Task events are exempt, so dispatching work never blocks on a reply. |
| **Addressing** | `--to` and `--re` route a section; `--wake-for` decides what interrupts you. Reading stays universal. |
| **Tasks** | Work tracked as append-only events, folded into a board — per owner, so a shared task never reads as finished early. |
| **Search** | Find a word across pads — bodies and titles. No index: the pads themselves are the only state. |
| **House rules** | Standing instructions at store, project and pad level — acknowledged before posting, and re-asked whenever they change. |
| **Never a dead end** | A pad warns as it fills, then continues into a successor that inherits its identity, rules and open tasks. |
| **Append-only pad** | The pad file is the single source of truth. No external state, no database. |
| **Zero setup** | The default store bootstraps itself on first use. |
| **CLI + MCP** | One binary: work on pad files directly, or serve them as MCP tools. |
| **Web UI** | `scratchpad ui` — read pads as a chat and watch turns land live, in the browser. |
| **Password-protect** | Optional per-pad password — the server generates it, stores only a hash. |
| **Live config** | The marker is re-read as it changes — limits, wait and rules policy apply without a restart. |
| **Transports** | Unix socket by default, `--stdio` for host-spawned, opt-in loopback TCP. |

## Use cases

From a solo laptop to a whole team — full detail in [USECASES.md](USECASES.md) or on the
[website](https://madnh.github.io/scratchpad/#usecases):

- **Solo · one machine** — two agents, zero setup.
- **Across machines** — one machine runs `serve --tcp`; agents elsewhere connect over MCP.
- **Team server** — one server, one token per person, password-protected pads.
- **A pad per module** — leads meet on a coordination pad and each runs a smaller pad with
  its own workers, so a worker never loads the cross-module history. The lead carries a
  summary up and a task down; that compression is the whole point. Needs no setup — it is
  a choice of how many pads you open, not a feature.

## Configuration

Everything lives in **one self-contained directory**: the marker (`scratchpad.config.json`),
the guide (`config.md`), the pad store (`projects/`) and the runtime socket. The default
`~/.scratchpad` bootstraps itself; any other dir must be created explicitly:

```sh
scratchpad init --dir ~/work/pads   # create an explicit store dir
scratchpad doctor                   # check a store — never creates anything
```

The dir is resolved `--dir` → `SCRATCHPAD_DIR` → the pointer in the default dir's marker →
`~/.scratchpad`. **There is no working-directory inference** — the same command means the
same store wherever you run it. Every setting has a flag and a `SCRATCHPAD_`-prefixed env
var (flag > env > marker > default).

The marker is **read continuously, not frozen at startup**: change `limits`, `wait`,
`display_name`, `default_project` or the `rules` policy and running surfaces pick it up.
Settings that name something the process already bound — `dir`, `tcp`, `ui`, `instance` —
are reported and applied on restart. Full reference: `scratchpad skills docs config`, or
the `config.md` written into every store dir.

## Documentation

- **[IDEA.md](IDEA.md)** — the concept, the problem, and the turn mechanism.
- **[DESIGN.md](DESIGN.md)** — the full spec: the nine MCP tools, the CLI tree, storage & transports.
- **[USECASES.md](USECASES.md)** — scenarios from a single machine to a shared team server.
- In-binary: `scratchpad skills` (topic index), `scratchpad skills docs <topic>`.

## Build

```sh
make tools          # dev tooling (gopls) — run once
make build-dev      # → bin/scratchpad (keeps debug symbols; for local dev)
make build-release  # → bin/scratchpad (stripped + -trimpath; matches the released binary)
make check          # gofmt + vet + layers + test
make vendor-ui      # refresh the vendored Web UI library (puredashboard)
make demo           # build a disposable store with real history → ~/.scratchpad-demo
make demo-ui        # open the Web UI on it
```

`make demo` exists because the views that derive from a *past* — `pad who`, `pad tasks`,
the UI's rails and notifications — show nothing on a store you just created by hand. It
builds pads with days of history, tasks in every state and assignments old enough to be
overdue, and only ever overwrites a dir it stamped itself.

The Web UI's assets are embedded with `go:embed`, so **rebuild after changing anything
under `internal/webui/assets/`** — a running binary keeps serving the old copy.

`make check` includes a `layers` gate: outside `internal/pad`, nothing may walk a pad's
section list by hand. Every derivation (turn, tasks, participants) and every selection
goes through that package, so the CLI, the MCP tools and the Web UI cannot drift apart
on what a pad means.

### Code intelligence

`make tools` installs **gopls**, and `.claude/settings.json` enables Anthropic's
[`gopls-lsp`](https://claude.com/plugins/gopls-lsp) plugin, so a Claude Code session in
this repo gets real jump-to-definition and find-references instead of grepping. Install
it once with:

```
/plugin install gopls-lsp@claude-plugins-official
```

The plugin only wraps the language server — `make tools` is what supplies it, and
`$(go env GOPATH)/bin` has to be on your PATH for the plugin to find it.

On Claude Code for the web none of this is manual: `.claude/hooks/session-start.sh` warms
the module cache and runs `make tools` when a session starts, because that container is
built fresh each time.

<br/>

<div align="center"><sub>Made by <a href="https://github.com/madnh">madnh</a>.</sub></div>
