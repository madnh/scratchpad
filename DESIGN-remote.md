# Scratchpad — remote access, authentication, contexts

**Status: proposal, with step 1 of the suggested order now built.** This document records
the reasoning behind a set of decisions so they can be argued with on paper rather than
rediscovered in code. See `DESIGN.md` for the spec of what exists, `IDEA.md` for the
concept.

What has since been done, and where to read it instead of here:

- **§2.4 / §2.5 / §2.6 — named credentials, hot reload, a separate `auth` group.** Built.
  `auth.clients` carries `{ name, digest, created }`, `tcpGuard` takes `*config.Live` and
  reads it per request, and `GroupAuth` is kept out of `OperatorEditable` by a test rather
  than by anyone remembering. `DESIGN.md` and `config.md` are the spec now; this section is
  the argument for it. The `authors` allow-list in §2.4 was NOT built — see open question 2,
  still open: enforcing it means plumbing the request's author into the guard, which is a
  larger change than naming credentials.
- **Open question 3 — ssh contexts.** Answered outside this repo. A shell wrapper (`spat
  <context>`) has run the full CLI over ssh since August, which is the evidence this
  document asked for before anyone builds an HTTP client. Nothing in the binary changed.

Everything else below remains a proposal: no remote HTTP client, no `token` command, no
contexts in the CLI, no Web UI revoke page.

Scope: reaching one deployment from a machine that is not the one it runs on, proving who
is reaching it, and naming which deployment a command means when there is more than one.

---

## The symptom

A deployment on a server, reached from a laptop. Today that leaves exactly one usable
surface — MCP — and MCP is the wrong shape for the job. An MCP call costs the agent a
turn and holds it there; `pad wait` from a shell does not, because the harness can run it
in the background and re-invoke the agent when it returns. Waiting is the operation this
tool exists for, and it is the one MCP is worst at.

## What is actually in the way

Four facts, each verified against the code rather than assumed:

- **The CLI has no client.** Every `pad *` command calls `store.New(live)` and reads the
  filesystem directly (`cmd/scratchpad/pad.go:280`, `:699`). There is no network path;
  `grep -ri remote` over the tree returns one comment.
- **`serve --tcp` is not cross-machine.** It binds `127.0.0.1` (`internal/server/server.go:101`)
  and `tcpGuard` rejects any `Host` that is not loopback (`hostIsLoopback`, `:194`). A port
  exposed on a LAN or tailnet address answers `403 forbidden host`. This is deliberate
  anti-DNS-rebinding, not an oversight.
- **`exec` cannot be remote.** The relay needs two things on one machine: the agent's pty
  (it is the parent process) and `internal/watch` (fsnotify over the store directory).
- **The MCP surface is a subset of the CLI.** Nine tools, no `search`, no `who`, no
  `delete`, no `purge`, no `_rules.md` write.

## What is NOT in the way

Two things assumed hard that turn out not to be:

- **`store.Wait` is already a poll, not a push** — 750 ms over the pad file
  (`internal/store/store.go:65-67`, `:797`). A remote wait therefore needs no new
  primitive; it is a poll loop over any read API. `internal/watch` exists only for the two
  callers that follow *many* pads at once — the Web UI's SSE and the relay — which is
  precisely where polling stops scaling.
- **There is no distributed-state problem.** Client/server keeps `flock` and keeps turn
  state derived from one file with one writer. The hard part of "two machines" never
  arises, because only one of them ever touches the store.

---

# Part 1 — Transport

## 1.1 Tunnel is mandatory; the loopback guard stays

The repo has no TLS anywhere (`grep -r crypto/tls` is empty). The choice is between
acquiring TLS + certificate handling, or requiring the operator to bring a tunnel
(`ssh -L`, WireGuard, Tailscale) and keeping the loopback guard exactly as it is.

**Decision: require the tunnel.** The guard is what makes it impossible for this binary to
expose a door to the Internet through a mistyped config, and that property is worth more
than the convenience of a direct bind. It matches how `exec` and `--tcp` are already
opt-in, and it means the security of the hop is handled by software that does that for a
living.

A consequence worth stating plainly: the tunnel authenticates the *machine*, usually
better than a static string in a JSON file ever will. Tokens are then a second layer, not
the only one.

## 1.2 Client polls; the server does not long-poll

Local, a `pad wait` that dies exits and the harness sees the code. Remote, a dropped
connection mid-long-poll either hangs forever or returns indistinguishably from a normal
timeout. Both read as *"nothing new yet"*.

This is the failure mode this project already has a rule about: an answer that means
"nothing found" and an answer that means "the check never ran" must not print the same
thing.

**Decision: the remote client polls.** Each round is a request; a network error is an
error. A remote wait must be able to end in three distinguishable ways — new section /
genuine timeout / lost contact — and the third exits non-zero.

Cost: 750 ms is too aggressive over a network. A remote client polls slower (2–5 s).
Slower than local, still far faster than one agent turn.

## 1.3 The surface gap: reads may be added, writes may not

`MCP is append-only by design` is a law about **writing**. It says nothing about reading,
and `search` / `who` being absent looks more like an unfilled gap than a decision. A
remote CLI that cannot search is a remote CLI that will be explained to every user
forever.

**Decision:** add the missing *read* tools (`pad_search`, `pad_who`) rather than teach
users a second, smaller CLI. `delete`, `purge` and `_rules.md` writes stay out — they
belong to the operator, and the operator has a shell on the server they deployed.

## 1.4 Rejected: synchronising the store

Git, Syncthing, NFS, sshfs. Rejected outright, not deferred:

- Two live ends are two conversations that both look current — exactly what
  `pad_continued` and `continued_by` exist to prevent.
- NFS/sshfs break `flock` and defeat fsnotify, so the two mechanisms the store is built on
  both fail quietly.

---

# Part 2 — Authentication

## 2.1 Inventory: no door currently asks who you are

| Door | Condition | Identity |
|---|---|---|
| Unix socket | same uid (peercred), dir 0700 / sock 0600 | not asked |
| TCP loopback | bearer token matching any digest | not asked |
| Web UI | one-time URL token → session cookie | "some person" |
| Protected pad | bcrypt password, server-generated | not asked |

`ValidateAuthor` (`internal/pad/meta.go:163`) checks **format** only — no `" - "`, no `;`
or `,`, not the reserved `scratchpad`. It does not check entitlement to a name. `--as
backend` is a claim, not a proof.

**That is correct today.** The operating system answers "who" before the request arrives.
Within one uid on one machine, an agent name is a routing label, not a security boundary,
and leaving it self-declared is right.

Remote deletes that answer. That is the entire problem, and it is only that.

## 2.2 Three questions, currently conflated

- **Authentication** — may this client talk to this deployment?
- **Authorization** — what may it do?
- **Attribution** — what name lands in the transcript?

The repo answers (2) **inside the pad** — turn rule, task owner, rules policy, pad
password — and that is a good structure. Only (1) is missing for remote. Do not let the
absence of (1) become an excuse to rebuild (2).

## 2.3 Rejected: binding a token to an author

The reflex is one token per agent. It does not survive contact with how agents actually
run: one machine hosts several (Codex, Claude, Gemini in adjacent panes) sharing one
install and inheriting one environment. An agent has nowhere private to keep a credential;
it has the environment its parent gave it. Enforcing token = author means building a
per-session credential-issuing mechanism — `exec`, but for secrets.

**The natural unit of a token is a machine or a session, not an agent.**

## 2.4 Give tokens a name, and an optional allow-list

Today: an anonymous array.

```json
"tcp": { "token_digests": ["sha256:abc…", "sha256:def…"] }
```

Proposed:

```json
"auth": { "clients": [
  { "name": "work-laptop", "digest": "sha256:abc…", "created": "2026-08-20", "authors": ["backend", "frontend"] },
  { "name": "ci-runner",   "digest": "sha256:def…", "created": "2026-08-20" }
]}
```

- `name` — for audit and for revoking *one* thing. Today you cannot revoke selectively,
  because you cannot tell which digest belongs to which machine. That is an operational
  defect independent of anything remote.
- `authors` — optional allow-list. **Empty means today's behaviour** (the name is a
  label). Present means `--as` becomes a claim that is checked.

The point of the optional form is that one deployment with one operator keeps the simple
model, and a deployment with two teams tightens it, on the same binary under the same law.

## 2.5 Hot reload restores an existing law rather than adding one

`DESIGN.md`/`CLAUDE.md` already state: *config is read continuously, never frozen at
startup; every surface takes a `*config.Live`*. `tcpGuard` is the exception that froze —
it takes `TCPOptions` **by value** and builds its digest map once into a closure
(`internal/server/server.go:142-150`). Taking `*config.Live` and reading per request puts
it back under the rule everything else already follows.

Consequence today: revoking a token requires a restart, and a restart severs every
in-flight `pad wait` on the deployment. The emergency operation is the one that costs the
most.

## 2.6 Why a new `auth` group instead of splitting `tcp`

Hot/cold currently splits **by group** (`MergeHot`, `internal/config/live.go:68`); every
hot group is hot whole. Tokens sit in `tcp`, but `tcp.port` must stay cold — the listener
is bound. Splitting *within* a group would be the first of its shape here, and it makes
`ColdChanges` lie: it reports `"tcp"` while the thing that changed applied immediately.

Moving credentials to their own top-level `auth` group keeps the split by group and is
also conceptually right: a credential list is not a property of a TCP listener. When a
remote event stream is added later for relays, it reuses `auth.clients` instead of growing
a second token list.

> ⚠️ **Migration trap.** Tokens are currently protected from the Web UI *because* they
> live in `tcp`, which is absent from `OperatorEditable` (`internal/config/update.go:63`).
> Moving them to `auth` without adding `GroupAuth` to that exclusion **disables the guard
> as a side effect of a refactor** — the exact "one forgetful edit away" failure the
> comment there warns about. The move and the exclusion belong in one commit, with a test
> that fails if the group is ever missing from the list.

## 2.7 Who may manage tokens: CLI grants, UI revokes

`CLAUDE.md` is explicit: *"a browser session must not be how those are **granted**."* The
protection is real — the UI's URL token is fixed for the process lifetime
(`internal/webui/auth.go:36`), so anyone who ever sees the URL can mint sessions forever,
and `ui.no_auth` makes every local process able to reach the port at all.

But the word is *granted*. Revoking is not granting, and revoking only ever narrows.

**Decision:**

- **Create → CLI on the server.** `scratchpad token add work-laptop` prints the token
  exactly once and forgets it — the same semantics as `store.GeneratePassword`
  (`internal/store/password.go:16-18`).
- **List and revoke → Web UI.** This is the operation that is urgent and wanted in a
  browser: a laptop is lost at 2am and someone wants one button, not an ssh session.
- **Revoke is disabled when `ui.no_auth` is set.** Without auth, any local process could
  cut off the whole team.
- **Do not widen `PUT /api/config`.** A dedicated `DELETE /api/auth/clients/{name}` keeps
  the documented allow-list literally intact; a narrow new door is more honest than a
  wider old one.

## 2.8 The client holds a plaintext secret — a first for this repo

The server stores digests and *never* plaintext (`internal/config/config.go:150-152`). A
client must hold the real token; it has to send it.

The marker is already written 0600 (`config.go:478`, `update.go:346`), so the ground is
prepared. Even so, support `token_command` from the start — the credential-helper shape —
so the token can live in Keychain/pass and the marker holds only the command that
retrieves it. The motivation is mundane rather than paranoid: client config files get
committed to dotfile repos, land in backups, and get `cat`-ed while screen-sharing.

Both forms allowed (`token`, `token_command`); the documentation recommends the latter.

---

# Part 3 — Contexts

## 3.1 The inversion: there is no `context use`

Docker's footgun is not contexts, it is `docker context use` — ambient state, global,
outliving the memory of having set it. The failure is the right command against the wrong
instance, and it is silent by construction.

**Decision: there is no current context.** The target is stated every time, or the
deployment has only one and the question does not exist. This is the same law as *"No
working-directory inference"*, applied to a second axis.

## 3.2 The presence of the `contexts` block flips the mode

- **No block** — exactly today's behaviour: `--dir` / `SCRATCHPAD_DIR` / marker pointer /
  default. A user who never wants contexts never learns the word.
- **Block present** — every command that touches a store must name its target. No default,
  no fallback.

The ambiguity is born the moment there is a second thing to point at, so the rule should
be born then too.

```json
{
  "contexts": {
    "local": { "dir": "~/.scratchpad" },
    "team":  { "url": "http://127.0.0.1:6710", "token_command": "security find-generic-password -s scratchpad-team -w" }
  }
}
```

Commands borrow docker's vocabulary minus the dangerous verb: `context create`,
`context ls`, `context rm`. **No `context use`.**

> ⚠️ **Migration must not create the block.** An earlier draft proposed generalising the
> existing `dir` pointer into `contexts.default`. That is wrong: it would make every
> existing command require a flag *because the user upgraded a binary*. The block is
> created only by an explicit `context create`, and that command prints, the first time,
> that every command on this dir now needs `--context`.

## 3.3 Environment variables: where two laws collide

The config model states that every env var has a matching flag. So `--context` implies
`SCRATCHPAD_CONTEXT`. But an env var *is* ambient state — worse than `use`, because it is
invisible on the command line and inherited by children.

**Decision: keep the env var, but destructive commands do not accept it.** `--context`
must appear on the command line for anything that destroys. The environment is fine for
the 95% (read, post, wait) and fatal for the rest. One sentence, narrow, and it puts the
guard exactly where the damage is.

A useful side effect: an agent's delete command always says out loud which deployment it
deleted from, and that stays in the transcript.

## 3.4 `--dir` and `--context` are mutually exclusive

Not a precedence order — an error. Two flags that name the same thing, with a winner, is
the mechanism by which people are surprised.

## 3.5 "Wrong context" is four different errors

They need distinct messages and distinct exits, or they collapse into the
"nothing-found looks like never-ran" failure:

| | Meaning | Must say |
|---|---|---|
| a | `--context nosuch` | unknown name; list the known ones |
| b | ref absent from that context | **"not in `team`"** — today it reports "pad not found", which is indistinguishable from "it was deleted" |
| c | unreachable | network/server down — *not* (b) |
| d | 401 | credential rejected — *not* (c) |

For (b) there is a version that costs zero I/O: **context-qualified refs**, written as
`team:projectx-ab3k9x`. `ParseRef` cuts on the first `-` and both halves accept `a-z0-9`
only (`internal/store/store.go:218-223`), so `:` collides with nothing. A ref that names
its own instance turns "wrong context" from a late error into a parse error — and, more
importantly, makes a ref pasted from one agent to another mean the same pad on the same
deployment.

This changes the ref grammar, which reaches the skill, the docs and every surface. It is
recorded here as a proposal on its own, not bundled with the rest.

## 3.6 Confirmation: the infrastructure exists; the test does not

The CLI already has the right shape (`cmd/scratchpad/pad.go:915-943`, `:994-1018`): a
`--yes` flag, a printed list of victims before asking, and — the important part — a
**refusal rather than a prompt** when stdin is not a TTY. That last one already handles the
case that matters most here, an agent that cannot answer a question. Do not touch it.

What is missing is that `y/N` does not test the thing at risk. The danger is *right
command, wrong instance*, and typing "y" proves nothing about knowing which instance you
are on. Escalate by blast radius:

- **local context** → `y/N`, as today.
- **remote context** → type the context name, the `terraform destroy` shape. Deleting on a
  shared deployment destroys other people's work, not your own.

`--yes` will be used unconditionally by agents, since they cannot prompt. The remaining
guard for them is §3.3: remote + destructive means `--context` is written on the line.

"Destructive" should be read wider than delete/purge: **writing rules** (the one thing in
this system that is *edited* — an overwritten rule is simply gone, with nothing in the
transcript recording that it ever said otherwise), `token revoke`, `context rm`.

## 3.7 Contexts are a client concept

The MCP server serves one store. The Web UI serves one store. Contexts exist only in the
CLI, and nowhere else should grow a switcher.

Consequence for `exec`: registration travels over the socket of the relay's own process
(`internal/relay/relay.go:49`), so a relay is inherently bound to one deployment. An agent
working across `local` and `team` needs two relays. Nothing is wrong with that, but it
belongs in the documentation before somebody discovers it by losing a nudge.

---

# Not doing

- **No user table, no roles, no RBAC.** The foundational rule is that no state lives
  outside the pad files — turn state, the roster and rules acknowledgement are all derived
  from the transcript. A user table is a second state that will drift. The marker is the
  only legitimate exception, because it belongs to the operator rather than to the
  conversation. Authenticate at the **edge**, authorise inside the **pad**, and have no
  third layer.
- **No store synchronisation** (§1.4).
- **No token granting from a browser session** (§2.7).

# Open questions

1. **Context-qualified refs (§3.5)** — worth changing the ref grammar for? The argument
   for is that refs are shared between agents by copy-paste and become ambiguous the
   moment a second deployment exists.
2. **`authors` allow-list (§2.4)** — build now or when two teams actually share a store?
   Building the field now and enforcing it later is cheap; retrofitting the concept is not.
3. **ssh contexts as well as url contexts.** `{ "ssh": "user@host" }` re-execs the CLI on
   the server and needs no client, no token and no new tools — the full CLI, today. It is
   also a working prototype of the whole feature: if an ssh context is used comfortably for
   weeks, that is the evidence that the HTTP client is worth building; if it is not, the
   feature was never needed.

# Suggested order

1. ~~`auth.clients` (named) + hot reload + `tcpGuard` reading `*config.Live` + `GroupAuth`
   added to the `OperatorEditable` exclusion.~~ **Done.** Worth doing even if remote access
   is abandoned entirely: a token could not be revoked without severing every waiting agent.
   The migration trap this document warned about was real and is now held shut by
   `TestSensitiveGroupsAreNotOperatorEditable`, which fails if the group is ever made
   writable from a surface.
2. `scratchpad token add/ls/revoke`, plus the view-and-revoke page in the Web UI.
3. Contexts — and only once a remote client exists, since before that `--context` is an
   alias for `--dir`.
