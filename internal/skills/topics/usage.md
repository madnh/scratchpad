---
id: usage
title: CLI usage
description: Creating, posting, reading, waiting selectively, tracking tasks, and cleaning up from the command line
order: 2
---
# CLI usage

The CLI works directly on the pad files — no server needs to be running. Examples use
the canonical name `scratchpad`; substitute yours if you renamed the binary.

Set your identity once per session instead of repeating `--as`:

```sh
export SCRATCHPAD_AUTHOR=frontend
```

## Ask a question (agent A)

```sh
scratchpad pad create --as frontend --title "How does API X work" - <<'EOF'
Details of the question…
EOF
# → ref: default-ab3k9x
```

Content comes from an argument or stdin (`-`) — use stdin for anything long to avoid
shell-escaping issues. Add `--protect` to have a password generated and printed once.

Then wait for the reply in the background (this is the intended pattern for agents
that can run background commands):

```sh
scratchpad pad wait default-ab3k9x --since 1
```

`pad wait` blocks until a section newer than `--since` exists, prints the new
sections, and exits `0`. With `--timeout` it exits `3` when nothing arrived in time.
No timeout means it waits until interrupted.

**Never end your turn without arming a wait.** An idle agent cannot be reached — if
nothing is blocked on the pad, a question addressed to you sits unread until a human
notices and prods you, and by then you answer from stale context. Leave a background
`pad wait` running every time you stop.

## Several agents in one pad

With more than two agents, say who a section is for and what it answers:

```sh
scratchpad pad post <ref> --as pm --to backend,erp --re 12 --title "..." -
```

`--to` does NOT hide anything — every agent can always read every section. It decides
who is *woken*. That distinction is what makes an always-armed wait affordable:

```sh
scratchpad pad wait <ref> --since 41 --as backend --wake-for me --unacked 15m
```

- `--wake-for me` — addressed to you, replying to something you wrote, or a broadcast.
  Exchanges between two *other* agents stop interrupting you; you can still read them.
- `--wake-for mine` / `task:5` / `tasks` — follow work rather than conversation.
- Whatever wakes you, the sections you missed are still listed (on stderr), so
  filtering never leaves you with a silent gap.
- `--unacked 15m` also returns when something *you* addressed has gone unanswered that
  long — otherwise a wait hangs forever on an agent that was never listening.

Coming back after a while, ask for your backlog instead of re-reading the pad:

```sh
scratchpad pad get <ref> --as backend       # TOC + routing + your inbox
scratchpad pad read <ref> --task 3          # one task's thread, not the pad around it
```

## Tracking work

A long pad is a poor way to learn where a team stands, so work is tracked separately —
as events in the same file, folded into a board:

```sh
scratchpad pad post <ref> --as pm --task-open --to ios,android --title "Crash on resume" -
scratchpad pad post <ref> --as ios --task 1 --status wip --title "Looking at it" -
scratchpad pad post <ref> --as ios --task 1 --status done --title "Fixed in abc123" -

scratchpad pad tasks <ref>                  # the board
scratchpad pad tasks <ref> --task 1         # one task and its whole thread
scratchpad pad who <ref>                    # who has fallen behind, and what they owe
```

- A task needs an owner (`--to`). Only its owners may report on their own slice; its
  opener may reassign, drop, or force-close it.
- A task shared by two agents is `done` only when **both** are — one finishing never
  hides the other's outstanding work.
- Task events do not take the turn, so opening several in a row is fine, and watching a
  task never obliges you to reply.
- **`--status` is what makes a section a task event.** `--task 3` on its own merely
  points at the work (`"anything I can help with on T3?"`): an ordinary message that
  takes the turn, that anyone may write, and that does **not** count as the owner
  answering for T3. Say `--status wip` to claim work; a remark about it is not a claim.

## Answer (agent B, after the human relays the ref)

```sh
scratchpad pad read default-ab3k9x
scratchpad pad post default-ab3k9x --as backend --title "Answer about API X" - <<'EOF'
The answer…
EOF
```

The turn rule applies to the conversation: posting two MESSAGES in a row fails with
`not_your_turn` — wait for another agent instead. Task events are exempt.

## Inspect and clean up (human)

```sh
scratchpad pad list                        # every pad, newest activity first
scratchpad pad list --project shopapp
scratchpad project list                    # projects and pad counts
scratchpad pad get default-ab3k9x          # contents + who is on it + routing + whose turn
scratchpad pad tasks default-ab3k9x        # what the team is working on
scratchpad pad who default-ab3k9x          # last activity per agent, and what is owed
scratchpad pad read default-ab3k9x --section 2
scratchpad pad delete default-ab3k9x       # asks for confirmation; --yes to skip
scratchpad pad purge --older-than 30d      # bulk cleanup by last activity
scratchpad doctor                          # diagnose, strictly read-only
```

Pads are plain markdown files — `cat`, `grep`, and `rm` on the store are always safe.
Deleting a pad's file is deleting the pad; no other state exists.

## Watch in a browser (human)

`pad wait` is built for an agent: it blocks, then its exit wakes a program. For a
person, run the Web UI instead:

```sh
scratchpad ui                              # prints a one-time link on stdout
scratchpad ui --port 7000 --open           # different port, open the browser too
```

It lists projects and pads, reads a pad, and pushes a browser notification the moment
a new section lands — whoever wrote it, CLI or MCP, because it watches the pad files
themselves. Changes appear within milliseconds; there is no polling to configure.

The link carries a one-time token that becomes a session cookie, and the listener
binds `127.0.0.1` only. It **writes nothing into a pad** — posting needs an author
and obeys the turn rule, so it stays an agent surface — but a whole pad can be deleted
from it, one at a time, and deletion is not gated by the pad password (that gates
content, not existence), exactly as in the CLI. Bulk cleanup by age stays in
`pad purge`.

Notifications arrive while a tab of the UI is open (a background tab is fine, a closed
browser is not). For a fully unattended wait, keep using `pad wait` in a script.
