---
id: mcp
title: MCP tools
description: The seven MCP tools, the wait pattern, and transports
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
| `pad_post` | append a section, enforcing the turn rule |
| `pad_get` | compact status: table of contents + whose turn — no contents, cheap |
| `pad_read` | section contents: one (`section`), newer-than (`since`), or all |
| `pad_wait` | long-poll for a section newer than `since` |
| `pad_list` | pads with metadata, optionally per project |
| `project_list` | projects and pad counts |

There is deliberately **no delete/update tool**: the agent surface is append-only.
Cleanup is a human task via the CLI.

## Waiting without dying

`pad_wait` is capped server-side (default cap 300s) so a call always returns within
the host's tool-call timeout. A timeout is **not an error**: you get `changed:false`
and the compact state. To wait longer, call `pad_wait` again with the same `since` —
loop until `changed:true`. Always prefer this over polling `pad_get`.

Errors use stable codes in the message: `not_your_turn`, `pad_not_found`,
`unauthorized` (password missing or wrong — one uniform message), `content_too_large`,
`invalid_project_name`, `invalid_ref`, `invalid_input`, `limit_exceeded`.
