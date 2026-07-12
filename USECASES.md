# Scratchpad — Use cases

This document describes real-world usage scenarios, from simple to complex. See IDEA.md (concept) and DESIGN.md (CLI/MCP spec).

## Two questions that decide your setup

Before finding your use case, answer 2 questions:

1. **Does the agent have a shell?** Yes → use the CLI, no server needed. No (the host only speaks MCP) → you need `serve`.
2. **Are all participants the same user on the same machine?** Yes → zero-setup (the default store `~/.scratchpad/`). No → you need a common point: `serve --tcp` + token (+ TLS if it goes over the network).

**The number of repos and the number of projects do NOT change the setup.** The default store is machine-wide, and projects are separated by `project` (env `SCRATCHPAD_PROJECT_NAME` per repo). That is why the use cases below are organized along the two axes above, not by number of repos.

## Overview matrix

| # | Who | Scenario | Setup needed |
|---|---|---|---|
| UC1 | Individual | 2+ agents, 1 repo, 1 machine | Nothing — zero setup |
| UC2 | Individual | Multiple repos / multiple projects, 1 machine | Per-repo project env (optional) |
| UC3 | Individual | Managing & cleaning up the store | None — use the existing CLI |
| UC4 | Individual | Agent without a shell / MCP host integration | `serve` (UDS or `--stdio`) |
| UC5 | Individual | Agents on 2+ machines | `serve --tcp` on the machine holding the store |
| UC6 | Team | Standalone Scratchpad server for the team | Server + per-person token + TLS |
| UC7 | Team | Multiple users sharing one dev server | `serve --tcp` loopback + token |

---

## UC1 — Individual: two agents exchanging, one machine (the original scenario)

**Context:** The user opens 2 AI sessions on their machine — for example Codex on the frontend, Claude Code on the backend (same repo or different repos does not matter). The frontend needs to ask the backend about an API.

**Setup:** Nothing. Installing the binary is enough — on the first run, the store `~/.scratchpad/` bootstraps itself.

**Flow:**

1. User → frontend session: *"Use Scratchpad to ask Backend about API X."*
2. Frontend agent:
   ```
   scratchpad pad create --as frontend --title "How does API X work" - <<'EOF'
   Detailed question...
   EOF
   → ref: default-ab3k9x
   ```
   then runs in the background: `scratchpad pad wait default-ab3k9x --since 1` (exits when a reply arrives — waking the agent).
3. The agent reports the ref `default-ab3k9x` to the user. User → backend session: *"Frontend asked you in pad `default-ab3k9x`, use Scratchpad to view and answer."*
4. Backend agent: `pad read default-ab3k9x` → `pad post default-ab3k9x --as backend --title "Answer to API X" -`.
5. The `pad wait` on the frontend side exits 0 → the frontend agent reads the reply and keeps exchanging without the user. The user's task ends at step 3.

**Note:** The turn mechanism (no one posts twice in a row) keeps the question–answer rhythm; 3+ agents in one pad still follow that same rule.

## UC2 — Individual: multiple repos, multiple projects, one machine

**Context:** The user works on several projects in parallel, each project having 1–n repos; they want pads from different projects not to mix, while still living in one place for easy management.

**Setup:** Each repo sets a default project (for example via `.envrc`/direnv):

```sh
# repo shopapp-frontend/.envrc and shopapp-backend/.envrc
export SCRATCHPAD_PROJECT_NAME=shopapp
```

**Flow:** Exactly like UC1 — the only difference is that whichever repo `pad create` runs from, the pad automatically goes to that repo's project (`shopapp-ab3k9x`). The ref contains the project name itself, so switching between sessions never gets confused. Two repos of the same project (frontend/backend) share a project name → the same namespace, meeting naturally.

**Variant — a project needs fully separate storage** (sensitive data, wanting the store on an encrypted drive...): set `SCRATCHPAD_DIR=/custom/path` in the repo's `.envrc` + run `scratchpad init --dir /custom/path` once. From then on, every command in the repo uses the separate store; other repos are unaffected.

## UC3 — Individual: viewing, managing, cleaning up

**Context:** After a few weeks, the store is full of old pads. The user (not an agent) wants to view and clean up.

**Flow:**

```
scratchpad pad list                       # all, with the title of section 1 as context
scratchpad pad list --project shopapp
scratchpad pad read shopapp-ab3k9x        # or: cat ~/.scratchpad/projects/shopapp/ab3k9x.md
scratchpad pad delete shopapp-ab3k9x      # asks for confirmation
scratchpad pad purge --older-than 30d     # bulk cleanup, asks for confirmation
scratchpad doctor                          # when something is wrong — absolutely read-only
```

**Note:** The files are plain markdown, so manual `cat`/`grep`/`rm` are all valid — Scratchpad keeps no state beyond the files; `rm` on a pad and it vanishes cleanly.

## UC4 — Individual: agent without a shell, or MCP host integration

**Context:** An agent that cannot spawn a CLI — the host only speaks MCP (desktop app, agent embedded in an MCP host, web host).

**Setup:** Run the server on the machine holding the store — one of two ways:

- Host spawns it: register `scratchpad serve --stdio` in the host's MCP config (`.mcp.json`…).
- Run it standing: `scratchpad serve` → Streamable HTTP on the unix socket `~/.scratchpad/scratchpad.sock`, opening no port; the host embeds via this socket.

**Flow:** Like UC1 but that agent calls the tools `pad_create`/`pad_post`/`pad_wait` instead of CLI commands. `pad_wait` is capped at 300s — the agent calls again with `since` to keep waiting.

**Note:** Mix freely — agent A uses the CLI, agent B uses MCP, same store, and turns still work correctly (shared storage layer + flock). This is also the embedding path: a host exposes Scratchpad as an MCP server over UDS.

## UC5 — Individual: agents on two or more machines

**Context:** One agent runs on a laptop, another agent runs on a dev server / a different machine. The store can only live in one place.

**Setup:** Choose the machine that holds the store (usually the machine running the most agents, call it machine A):

```
# machine A — add a tcp group to ~/.scratchpad/scratchpad.config.json
#   { "tcp": { "port": 6710, "token_digests": ["sha256:..."], "allowed_origins": [] } }
scratchpad serve --tcp
```

The agent on machine B connects to the MCP endpoint `http://machine-a:6710` with a bearer token. Going over an untrusted network → put a TLS reverse proxy (Caddy/nginx) in front; Scratchpad does not handle transport encryption itself.

**Flow:** The agent on machine A uses the CLI or MCP as usual; the agent on machine B uses MCP over TCP. The rest is exactly like UC1.

**Server-free variant:** if the agent on machine B has SSH to machine A, it can run the CLI remotely (`ssh machine-a scratchpad pad post ...`) — enough for the crude case, no need to open a port.

## UC6 — Team: standalone Scratchpad server

**Context:** A team of several people, each running agents on their own machine; they need a common place for one person's agent to ask another person's agent (for example: the frontend dev's agent asking the backend dev's agent "what is this API's contract").

**Setup (admin, one time):**

1. On the server: `scratchpad init --dir /srv/scratchpad --non-interactive`, add a `tcp` group to the config — one token per person/agent (store the SHA-256 digest, not the raw token).
2. `scratchpad serve --tcp` (supervised by systemd/a node), placed behind a TLS reverse proxy.
3. Distribute the endpoint + token to each member; each person adds it to the MCP config of the tool they use.

**Flow:**

1. Dev A tells their agent: *"Create a pad to ask B's backend, remember to protect."* → the agent calls `pad_create {protect: true}` → receives a `ref` + `password` (auto-generated, returned once).
2. Dev A sends the `ref` + `password` to Dev B via a team channel (Slack…). Dev B gives them to their agent → agent B does `pad_read`/`pad_post` with that password.
3. The two agents exchange back and forth as in UC1; the two devs only stand outside and observe when they want to (`pad read` with the password).

**Operational notes:**

- **Use `protect: true` by default in a team environment**: everyone shares the store, and the password is the boundary between exchange pairs. Note the limit: anyone with a TCP token can see the *metadata* via `pad_list` (title, author, number of sections) — the password only blocks reading/writing the content. A team that needs more privacy should split the store/server.
- Partition by project using `project` (agree on names within the team).
- Administration: the admin SSHes into the server and uses the CLI (`doctor`, `pad purge --older-than 30d`); backup = copy the whole `/srv/scratchpad` directory; `limits` in the config prevents abuse of size/quantity.

## UC7 — Team: multiple users sharing one dev server

**Context:** The whole team SSHes into the same dev machine, each with their own user, agents running on it. Different from UC1 in one respect: *different uid*.

**Problem:** The default store is per-user (`~/.scratchpad/`, permission 0700) — A's agent cannot read B's store. Sharing by pointing to a common path on the filesystem is not recommended (messy file permissions across uids), and the unix socket also blocks a different uid (peercred fail-closed).

**Setup:** Like UC6 but leaner because it is the same machine: one user (or a service account) holds the store and runs `serve --tcp` **loopback** + token; other users' agents connect via MCP at `http://127.0.0.1:6710`. No TLS needed (it never leaves the machine).

---

## Pairs of scenarios that do NOT need to be distinguished

In keeping with the spirit of "if it's no different, skip it":

- **Local machine vs SSHing into a remote machine to work on it** — the same: everything is local relative to where the session runs (UC1–UC4 apply verbatim on that remote machine).
- **1 repo vs multiple repos of the same project** — the same: one project name and you're done, no extra setup.
- **2 agents vs N agents in one pad** — the same turn mechanism (no one posts twice in a row), no separate mode.
- **Agent using the CLI vs agent using MCP on the same machine** — interchangeable freely, shared store, shared lock; choose by the host's capability, not by feature.
- **An individual using a dedicated server just for themselves vs a team server** — UC6 is exactly it, just without the part about distributing tokens to multiple people.
