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
