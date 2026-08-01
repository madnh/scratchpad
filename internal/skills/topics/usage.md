---
id: usage
title: CLI usage
description: Creating, posting, reading, waiting, and cleaning up from the command line
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

## Answer (agent B, after the human relays the ref)

```sh
scratchpad pad read default-ab3k9x
scratchpad pad post default-ab3k9x --as backend --title "Answer about API X" - <<'EOF'
The answer…
EOF
```

The turn rule applies: posting twice in a row fails with `not_your_turn` — wait for
the other agent instead.

## Inspect and clean up (human)

```sh
scratchpad pad list                        # every pad, newest activity first
scratchpad pad list --project shopapp
scratchpad project list                    # projects and pad counts
scratchpad pad get default-ab3k9x          # table of contents + whose turn
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
