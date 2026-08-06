# CLAUDE.md — scratchpad

Shared pads for AI agents to exchange messages turn by turn (Go). Independent
codebase: it runs standalone, with no dependency on any particular host/mesh. Any host
integration must stay **generic and opt-in** — configured by the operator, never
hardcoded. No host name may appear in code, in a default, or in the embedded skill
content: nothing the binary DOES may depend on which host is asking.

**The exception is a reader's install instructions, and only there.** README and the
website may name hosts in a lookup table of where each keeps its skills, because a
person following an install step needs the actual path and cannot be expected to
derive it. That table is a convenience about somebody else's product: it is dated,
it is checked against their docs, and the neutral cross-host path is what the
instructions lead with. It changes nothing about the code, which still has no default
destination and never asks who the host is.

Design source of truth: `DESIGN.md` (spec), `IDEA.md` (concept), `USECASES.md`.

## Configuration model

Everything lives in one **self-contained Scratchpad dir**: the marker
(`scratchpad.config.json`), the guide (`config.md`), the pad store (`projects/`),
and the runtime socket.

- Resolution: `--dir` flag → `SCRATCHPAD_DIR` → the `dir` pointer in the DEFAULT
  dir's marker → the default `~/.scratchpad`. **No working-directory inference.**
- The DEFAULT dir auto-bootstraps on first use; an EXPLICIT dir must be `init`-ed
  first (commands error instead of creating it). `doctor` never bootstraps anything.
- All env vars share the `SCRATCHPAD_` prefix; every env var has a matching flag
  (flag > env > marker > default).
- `projects/` and the socket are **derived** from the dir, never configured.

## Storage model

One pad = one markdown file `projects/<project>/<padid>.md`, append-only. The first
line is the pad header — `scratchpad v<N>` plus `key: value` fields (`created`, `opener`,
optional `password`, optional `continues`); sections are headed
`# <n> - <author> - <title>`. Turn state is DERIVED from the last section — there is no
state outside the pad files. Appends take an exclusive `flock` on the pad file; reads a
shared one. The CLI and the MCP server share `internal/store` — never write a second disk
path.

**The header is parsed in ONE place (`pad.ParseHeader`) and there are two parsers.**
`scan` (offset, keeps bodies) and `scanLines` (streaming, discards them) must never grow
their own copy of the header rules — that is how the two start disagreeing about what a
pad is. The same goes for what a field MEANS: `Opener()` reads `Header.Opener` and nothing
else. Deriving it from section 1 was correct until a pad could continue another; the
derivation now lives only in `pad.Upgrade`, which runs once per legacy file.

**Content SEARCH is a visitor on `scanLines`, not a third parser and not an index.**
`pad.ScanBodyLines` splices a callback in where the body was already being discarded, so
`internal/store/search.go` never learns what a line means on its own. Two things stay
true of it: it reads every byte of every pad it looks at — an index would be state living
outside the pad files, which any writer (including a person with `rm`) would leave stale —
and it keeps only matching lines, so memory follows the RESULT, not the store. Protected
pads are skipped unless addressed by ref WITH their password; reading through a password
one noun at a time is still reading through it. What was left out is always reported —
on the SUMMARY line, not only in the detail below it, because a reader who takes in one
line must not take in the wrong answer.

**Order is a question, not a preference.** The default (newest pad first, grouped) answers
"what is being said about this"; `--oldest` answers "where was this DECIDED", and they are
not interchangeable — a term under active argument buries its own definition under every
restatement. That is why `--oldest` sorts by a hit's own timestamp and drops the grouping,
why the time window filters SECTIONS rather than pads (a live pad usually also holds the
old decision), and why `applyLimit` runs AFTER ordering.

**Migrating the file format is the tool's job — there is no `migrate` command.** A v1 pad
is read normally and rewritten on its first post, in place (never temp-file+rename: rename
swaps the inode and every `flock` here is on the pad file). A REFUSED post migrates
nothing. Bumping `pad.FileVersion` means old binaries call the file corrupt, so it is
reserved for a header change a v1 reader cannot skip.

**Naming law, one definition.** A file starting with `_` belongs to the tool
(`_rules.md`); a pad is `[a-z0-9]{1,64}.md` and nothing else. The predicates live in
`internal/pad` (`IsPadFileName`, `IsToolFileName`) and the store, the watcher and
`doctor` all call them — never re-derive "is this a pad" from a `.md` suffix.

**A full pad CONTINUES by default** (`limits.on_full`): the store opens a successor, copies
the pad's identity into its header (`opener`, password, `continues`, `tasks_from`) plus its
house rules and open tasks, appends a `kind: continued` section to the old pad and records
`continued_by` in its header. The old pad then refuses posts with `pad_continued` forever —
two live ends are two conversations that both look current. `internal/store/continue.go`
holds all of it; the successor is written BEFORE the old pad is closed, so a failure can
never leave a pad closed with nowhere to go.

**`kind: continued`, `kind: rules` and `kind: notice` wake every waiter, before any selector
is consulted.** Selective waking spares an agent traffic it has no part in; it must never
spare it something that changes what it is ALLOWED to do next — a pad that can no longer
receive its answer, or rules its next post will be refused under. Routing rules through the
selectors looked right and was not: `me` is answered by `concernsAuthor`, which counts a
broadcast only for a `message`, so the agents that had narrowed their waits were exactly the
ones that never heard the rules had changed.

**Turn state filters on `== KindMessage`, never `!= KindTask`.** Every stream added
later is bookkeeping until proven otherwise; the negative form silently hands the turn
to each new `kind` on the day it appears. The RENDER side is spelled the opposite way —
`renderMetaLine` writes every kind that is not `message` — because a positive list there
drops each new kind from the file, and a section whose `kind` never reached disk parses
back as the one value that takes the turn.

The MCP surface is **append-only by design**: no delete/update tools. Deletion/purge
exist only in the CLI, and so is writing the store/project `_rules.md` (rewriting a file
is not an append) — a PAD's rules are set through `pad_post`, because that is one.

**Rules are the one thing here that is EDITED, so changing them is gated twice.** WHO is
the marker's `rules` policy (`config.RulesPolicy`): by default the two file levels are
the operator's — the Web UI or an editor, never an agent, so the CLI path above is off
unless a deployment turns it on — and a pad's belong to the agent that opened it. ON TOP
OF WHAT is a per-level version (`pad.LevelDigest`), quoted on every write; it is NOT the
combined digest `--ack-rules` uses, because a pad-rules edit must not fail over a change
to the store's. Both live in `internal/store`, never in a surface: a policy enforced in
one command is a policy the next surface forgets.

**The READ gate is repeatable, and its memory lives in the pad.** `rules.reack` decides when
it fires again: `on-change` (default) re-asks whenever the rules in force move at ANY level,
`once` is the old first-post-only behaviour. The proof is `acked` on the section metadata
line — derived from the transcript like turn state and the roster, never a subscription list
outside the files, and free to find because the append path already parses that line. Two
edges are not optional: a section that WRITES rules records the digest that will be in force
AFTER it lands (else every agent is refused by rules it just typed), and a post that lands in
a successor pad always records one (the successor holds none of the transcript that would
otherwise vouch for it). `pad.CheckAck` is the ONE place that answers "has this author read
them" — `UnreadRules` exists so `pad get`/`pad wait`/MCP ask it rather than re-deriving.

**Making a change ARRIVE is separate from making it bind, and can only be a section.**
`_rules.md` is not a pad file, so `internal/watch` never reports it, and `Wait` counts news
only when a new SECTION appears — a person editing rules reaches nobody. Hence `--notify` /
the dialog's checkbox: a `kind: notice` from `pad.SystemAuthor` in each pad the level binds.
Both surfaces default to ANNOUNCING (box ticked, flag true), because a version nobody hears
about binds only whoever posts next while the agents already at work carry on under the old
one. The default belongs to the surfaces and never to `store.NotifyRulesChanged`, whose
parameter has none: the store must not write into live conversations on its own initiative.
What is skipped is decided by whether a pad can USE the notice — continued, full, quiet —
never by who may READ it: a PROTECTED pad is told, through `PostRequest.ToolNotice`, which
bypasses the password gate and nothing else. A password keeps other agents out of a pad; it
was never a reason to leave that pad blocked by rules nobody told it about. Keep it separate
from `SystemPost` — that one is the UI's reserved identity, and the UI still unlocks a pad
before writing content into it. Every skip is COUNTED and reported, on the same line as the
total, because a fan-out that quietly did less than it said reads as "everyone knows".

**A privilege is a FIELD the calling code sets, never a string an agent can send.**
`PostRequest.SystemPost` and `store.RulesWriter` exist in that shape for one reason:
`SetPadRules` used to infer the person's exemption from the author string while the CLI
defaulted that same string, so an agent claimed it by not naming itself. Never reintroduce
`if author == SystemAuthor` as an authorisation test.

## Surfaces — who each one is for

- **CLI** (`internal/store` directly) — the primary path; an agent with a shell needs
  nothing else.
- **MCP** (`serve`) — hosts that can't spawn a CLI, or cross-machine over TCP.
- **Web UI** (`ui`, `internal/webui`) — for a PERSON: browse, read, watch. Separate
  loopback listener, not a fourth MCP transport (browsers can't use the Unix socket,
  and its auth is a one-time link → session cookie). **Read-only for the conversation** —
  posting a message or moving a task needs an author and obeys the turn rule, so those
  stay an agent surface. **Rules are the sole exception** and only because they fail
  neither test: a rules section takes no turn, and it is authored by the reserved
  `pad.SystemAuthor` ("scratchpad"), which agents may not claim. Do not widen this.
  The UI is also the surface the rules POLICY points at — it is exempt from who-may-write
  and NOT from the version check, since two tabs lose an edit exactly as two agents do.
  It also edits the deployment's own settings (`PUT /api/config`), which is not a widening
  of the above: config is the OPERATOR's, takes no turn and carries no author. What it may
  write is `display_name`, `default_project`, `limits`, `wait` — and nothing else, ever.
  `tcp`, `ui` and `rules` decide who may reach this deployment and who may rewrite the
  operator's instructions; a browser session must not be how those are granted.

`internal/watch` turns pad-file writes into a push stream via kernel filesystem
events. It watches the STORE, never the writers: any writer — CLI, MCP, or a person
with `rm` — is noticed identically, and `internal/store` stays ignorant of listeners.
Do not add a writer-side notification hook; it would miss every uncooperative writer.
The same package watches the MARKER (`watch.Marker`, `watch.ReloadConfig`) — the file,
not the writers, for the same reason.

**Config is read continuously, never frozen at startup.** Every surface takes a
`*config.Live` and reads a snapshot per operation; `store.New`/`mcpsrv.New`/`webui.New`
take nothing else, so no call site can be handed a stale copy. Only the HOT groups reload
(`config.MergeHot`: display_name, default_project, limits, wait, rules) — `instance`,
`dir`, `tcp` and `ui` name things the process has already bound, so they are reported and
applied on restart. A marker that fails to load leaves the running config ALONE: falling
back to defaults would silently reset the `rules` policy. Writing the marker goes through
`config.UpdateMarker` (quote-the-version + atomic rename), never `WriteMarker` — that one
belongs to `init` and refuses to overwrite.

The UI's assets are `go:embed`-ed, so **rebuild the binary after editing anything under
`internal/webui/assets/`** — a running server keeps serving the old copy. The vendored
puredashboard library there is refreshed with `make vendor-ui`, never hand-edited.

## HARD RULE — keep the config guide in sync

`internal/config/config.md` is embedded (`go:embed`) and written into every
Scratchpad dir. **Whenever you change the config schema** (marker fields, defaults,
resolution order, env vars, the pad file format), **update `internal/config/config.md`
in the same change**, and bump `config.ConfigVersion` when the marker format changes
incompatibly. The skills topics (`internal/skills/topics/`) document the same
contracts — check them too.

Keeping the EMBEDDED copy current is not enough on its own: every existing dir holds its
own copy, and `init` refuses a dir that already exists. `config.Resolve` therefore rewrites
a dir's `config.md` whenever it differs from the binary's (`ensureDoc`) — best-effort and
silent, because a read-only store must not fail `pad post` over a doc. That file belongs to
the tool; the operator's choices live in `scratchpad.config.json`, and the guide says so.

**Three documentation surfaces, one contract.** `internal/skills/SKILL.md` is the AGENT's
entry point — the short document a host loads to decide when to reach for this tool — and
it is the one that changes behaviour in practice, because an agent acts on it without ever
running `skills docs`. The topics are the reference it can consult; `config.md` is the
operator's. A change to a rule an agent must obey belongs in SKILL.md FIRST, then in
whichever topic covers it. `skills install --into <dir>` publishes SKILL.md; the
destination is always the operator's to name (flag or `SCRATCHPAD_SKILLS_DIR`), never a
default — this repo names no host, and a conventional path is a host's property.

## Build / test

```
make check        # gofmt + vet + layers + go test + test-ui
go build ./...
```

**`make check` now needs `node` on PATH.** `test-ui` runs the Web UI's JS tests through
`node --test` — the runner that ships with Node: no `package.json` to install, no
`node_modules`, no browser download, so the repo stays as self-contained as it was. A
missing `node` FAILS the target rather than skipping it; a silent skip reads exactly like
a pass.

Those tests cover the layer that needs no DOM — `internal/webui/assets/lib/fmt.js` imports
nothing and touches no document, so the most valuable thing to test is also the cheapest,
and it belongs in the gate. **They live in `internal/webui/uitest/`, NOT under `assets/`,
because `//go:embed all:assets` would compile any file under there into the binary and
serve it.** `internal/webui/package.json` exists only to tell Node those files are ES
modules, and sits outside `assets/` for the same reason.

What needs a real browser — node identity across a re-render, `<puredashboard-lazy>`
states, scroll geometry — is NOT here and must not be faked: a DOM shim answers one of
those five questions and lies plausibly about the rest. That layer is
`assets/harness.html`, a page nothing references, driven by hand. It measures; it does not
assert, and nothing fails a build if the transcript regresses. That is a known cost, not
an oversight.

Under `serve --stdio`, stdout belongs to JSON-RPC — all logging must go to stderr.
`scratchpad serve --stdio >/tmp/out 2>/tmp/err` must leave `/tmp/out` empty.

## Seeing a feature actually work

`make demo` builds a disposable store (`~/.scratchpad-demo`) holding pads with days of
history, tasks in every state, and assignments old enough to be overdue — the views that
derive from a PAST (`pad who`, `pad tasks`, `/api/stuck`, the UI's rails and
notifications) show nothing on a store you just created by hand. `make demo-ui` opens
the Web UI on it. It never touches the real store: it only overwrites a dir it stamped
itself.

The story lives in `tools/gendemo/scenario.go` and is written in labels, not section
numbers. **Adding a feature usually means adding a scenario for it** — read
`tools/gendemo/README.md` first: it documents the current demo's exact state, and asks
that those tables be updated in the same commit as the scenario.
