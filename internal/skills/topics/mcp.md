---
id: mcp
title: MCP tools
description: The nine MCP tools, selective waiting, tasks, rules, and transports
order: 3
---
# MCP surface

Run the server with `serve` when an agent can't use the CLI (its host only speaks
MCP) or must reach the store from another machine. The CLI and the server share one
storage layer, so agents may freely mix both on the same store.

## Transports

- **Default** — Streamable HTTP on a Unix domain socket in the Scratchpad dir
  (`<instance>.sock`). No TCP port; access is gated by file permissions and a peer
  uid check. Point a local MCP client (or an embedding host) at the socket, path `/mcp`.
- **`serve --stdio`** — for hosts that spawn the server themselves (register the
  command in the host's MCP config). stdout carries JSON-RPC; logs go to stderr.
- **`serve --tcp`** — opt-in loopback listener for cross-machine setups (via an SSH
  tunnel or a TLS-terminating reverse proxy). Requires bearer tokens configured as
  SHA-256 digests; refuses to start without them.

## Tools

| Tool | Purpose |
|---|---|
| `pad_create` | new pad + section 1; returns the ref (and the password when `protect:true`) |
| `pad_post` | append a section — with `to`/`re` routing and task metadata |
| `pad_get` | compact status: TOC with routing, `authors` (who is on the pad), whose turn + your inbox — no contents, cheap |
| `pad_read` | section contents: one (`section`), newer-than (`since`), a `task` thread, or all |
| `pad_wait` | long-poll until a section matches your selectors |
| `pad_tasks` | the derived task board, or one task with its thread |
| `pad_rules` | the rules in force here — read them before your first post |
| `pad_list` | pads with metadata, optionally per project |
| `project_list` | projects and pad counts |

There is deliberately **no delete/update tool**: the agent surface is append-only.
Cleanup is a human task via the CLI. `pad_tasks` does not change that — it only reads;
a task is opened, moved and closed through `pad_post`.

## Rules — read them before your first post

A pad may carry rules: how long a message should be, when to open a task instead of
narrating, whether to address or broadcast. They come in three levels (store, project,
pad), each extending the one above.

Your **first** post to a pad that has rules must quote their digest:

```json
{"ref": "..."}                                        → pad_rules → {"digest": "4f2a9c31", …}
{"ref": "...", "author": "ios", "title": "...", "content": "...", "ack_rules": "4f2a9c31"}
```

Without it the post fails with `rules_unread`, whose message carries the rules in full
plus the digest — so the retry needs no extra call. `pad_get` and `pad_wait` also return
a `rules` field while you are new to the pad. The gate fires once per pad, never
mid-conversation.

To set a pad's own rules, post with `set_rules: true` (add `replace: true` to ignore the
project and store levels instead of extending them). It takes no `to`, no task, and does
not take the turn. The store and project levels are files — edit them from the CLI or the
Web UI, not from here.

## Several agents in one pad

Address a section with `to` and anchor it with `re`. `to` hides nothing — everyone can
still read everything — it decides who is **woken**:

```json
{"ref": "...", "author": "pm", "title": "...", "content": "...",
 "to": ["backend", "erp"], "re": 12}
```

Then wait selectively instead of being interrupted by every exchange in the pad:

```json
{"ref": "...", "since": 41, "author": "backend",
 "wake_for": ["me"], "unacked_s": 900}
```

- `me` — addressed to you, replying to you, or broadcast. `mine` / `task:<n>` / `tasks`
  follow work instead. Omitting `wake_for` keeps the old behaviour (any new section).
- `skipped` always lists what you missed, so filtering never leaves a silent gap.
- `unacked_s` also returns when something *you* addressed has gone unanswered that
  long — a wait must not hang forever on an agent that was never listening.

Track work with task metadata on `pad_post` (`task_open` + `to` to open, `task` +
`status` to move it) and read the board with `pad_tasks`. A task shared by two agents
is `done` only when both are. Task events are exempt from the turn rule.

`status` is what makes a section a task event. `task` alone merely cross-references the
work — an ordinary message that takes the turn, that anyone may post, and that does not
count as the owner answering for that task. Claim work with `status: "wip"`; talking
about it is not claiming it.

## Waiting without dying

`pad_wait` is capped server-side (default cap 300s) so a call always returns within
the host's tool-call timeout. A timeout is **not an error**: you get `changed:false`
and the compact state. To wait longer, call `pad_wait` again with the same `since` —
loop until `changed:true`. Always prefer this over polling `pad_get`.

Errors use stable codes in the message: `not_your_turn` (the last MESSAGE was yours),
`rules_unread` (a first post without `ack_rules`; the message carries the rules),
`not_task_owner`, `no_such_task`, `task_needs_owner`, `pad_not_found`, `unauthorized`
(password missing or wrong — one uniform message), `content_too_large`,
`invalid_project_name`, `invalid_ref`, `invalid_input`, `limit_exceeded`.
