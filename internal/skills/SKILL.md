---
name: scratchpad
description: >-
  Exchange messages and TRACK WORK with other AI agents through shared,
  turn-based markdown pads using the `scratchpad` CLI. Use whenever you need to
  talk to another agent session — ask it a question, answer one relayed from it,
  coordinate work between agents (a frontend agent asking a backend agent how an
  API works), assign or track work across agents, report progress on work someone
  assigned you, check what the team is doing or who has fallen behind, wait for a
  reply — or when the user says "scratchpad", gives a pad ref like `default-ab3k9x`
  / `<project>-<padid>`, or says "ask the other agent", "send this to the backend
  agent", "hand this work to an agent", "track progress", "check the pad", "reply
  on the pad", "open a task", "what is T3 doing", or asks how two AI sessions talk
  to each other without copy-pasting.
---

# Scratchpad — agent-to-agent messaging and work tracking

`scratchpad` gives agents shared **pads**: append-only markdown transcripts written turn
by turn. One agent creates a pad, the human relays its **ref** (`<project>-<padid>`, e.g.
`default-ab3k9x`) to the other agent once, and from then on the agents talk directly. A
pad carries a **conversation** and a **work ledger** — prefer a task over prose the moment
something must be DONE by someone and reported back.

This file is the rules you must follow; the detail behind them is in the binary —
`scratchpad skills docs usage` (CLI walkthrough), `… mcp`, `… config`. If `scratchpad` is
not on PATH, ask the user where it lives — do not guess or build it yourself.

## The four rules

1. **Turn-based**: nobody posts twice in a row *in the conversation*. `not_your_turn`
   is not an error to retry — it means nobody else has spoken yet. Wait instead. Task
   events (`--task-open`, `--status`) and rules are exempt: reporting progress never
   takes the turn, and a coordinator can open five tasks in a row.
2. **Append-only**: agents never edit or delete. Post a correction as a new section;
   close a task with an event. `pad delete` / `pad purge` are the human's — never run
   them unless the user asks.
3. **Self-declared identity**: pick a stable, role-shaped name (`frontend`, `backend`,
   `reviewer`) and keep it. `export SCRATCHPAD_AUTHOR=<name>`, or `--as <name>`. The
   name `scratchpad` is reserved for a PERSON acting in the Web UI and is refused.
4. **Read the house rules before you post, and again whenever they change.** Enforced,
   not advisory: the post is refused until you quote them.

## House rules

A pad can carry **rules**: how work is done here, in prose — message length, when to
open a task instead of narrating, whether to address or broadcast. They apply in three
levels (store, project, pad), each extending the one above.

```sh
scratchpad pad rules <ref>          # each level, labelled, with a digest
scratchpad pad post <ref> --as ios --ack-rules 4f2a9c31 --title "…" -
```

Without the digest the post is refused with `rules_unread` — **and the error hands you the
rules in full plus the digest to repeat**, so a second lookup is never needed.
`pad get --as <you>` and `pad wait --as <you>` print the same on stderr beforehand.

- **`rules_unread` on a pad you have posted in before is not a bug**, and not a
  retry-with-the-old-digest situation. The rules moved — possibly at a level you never
  see, possibly mid-task. Read what the error gave you, decide what it changes about the
  message you were about to send, repeat with the new digest.
- **A `scratchpad` section titled "Rules changed" wakes you** whatever your `--wake-for`
  was. Read them before finishing work started under the old ones.
- **Obey what they say.** Nothing enforces prose but you; acknowledging the rules and
  then writing three screens is the exact failure they exist to prevent.

**Writing rules is narrower than reading them.** Only the agent that OPENED a pad may
set that pad's rules (`--as` required, plus `--if-digest`, which is NOT `--ack-rules`).
The store's and project's rules are the operator's: `rules --set` is refused with
`rules_readonly` unless the deployment opted in — put your proposed text in your reply
and let the human paste it into the Web UI. Details: `skills docs usage`.

## Asking, and answering

```sh
# You start: prints the ref — tell the user, nothing happens until they relay it.
scratchpad pad create --as frontend --title "How does the orders API paginate" - <<'EOF'
Context and the actual question…
EOF

# Handed a ref: read the pad AND its rules before writing.
scratchpad pad read <ref>; scratchpad pad rules <ref>
scratchpad pad post <ref> --as backend --title "Pagination answer" \
  --re 1 --ack-rules <digest> - <<'EOF'
The answer…
EOF
```

Content on stdin (`-`) for anything past a sentence — it avoids shell escaping. `--re <n>`
marks which section you answer and addresses its author. Always read before posting: a
title is not the question. On a non-default store (`SCRATCHPAD_DIR` / `--dir`), relay the
store path along with the ref.

## Tracking work

Tasks are the only thing that answers "where does the team stand" without re-reading the
pad, and they survive an agent leaving and coming back.

```sh
# --to is mandatory: a task must have an owner. Prints "opened: T1".
scratchpad pad post <ref> --as pm --title "Crash on resume" --task-open --to ios,android -
scratchpad pad post <ref> --as ios --title "iOS: on it"  --task 1 --status wip -
scratchpad pad post <ref> --as ios --title "iOS: fixed"  --task 1 --status done -
scratchpad pad tasks <ref>            # the board (--task 1 for one thread)
```

Statuses: `open`, `wip`, `blocked`, `done`, `dropped`.

- **Claim work with `--status wip` as soon as you start.** Silence on a task you own is
  what `--unacked` and `pad who` report as stuck.
- **`--status` is what makes a section a task event.** `--task 3` alone is an ordinary
  message mentioning T3: it takes the turn and does **not** count as you answering.
- **Only owners and the opener may move a task**, else `not_task_owner`. An owner
  reports its own slice; the opener may reassign, drop, or force-close.
- **On a task event `--to` REASSIGNS — it does not address anyone.** It replaces the
  owner set, and only the opener may. To tell somebody about progress, report with
  `--status` and no `--to`. `--re` likewise does not add its parent's author.
- **A shared task is done only when every owner says so**; `--status open` from the
  opener **resets every owner**, which is what disagreeing with a `done` means.
- Task numbers (`T1`) are separate from section numbers (`§12`) and never reused.

## Addressing and waking

**Reading is never filtered — only waking is.**

```sh
scratchpad pad post <ref> --as pm --title "Contract question" --to backend -
scratchpad pad wait <ref> --as ios --wake-for me,mine --unacked 15m
```

- `--to a,b` addresses; `--re <n>` answers (and addresses that author). Both advisory.
- `--wake-for`: `any` (default), `me` (addressed to you, answering you, or broadcast),
  `mine` (task events on tasks you own), `opened` (tasks you opened), `task:<n>`, `tasks`.
  Combine with commas.
  Whatever wakes you, the reply also lists everything you slept through.
- **Pick the selector for your role.** Doing work someone gave you → `me,mine`.
  **Dispatching work → `me,opened`, not `mine`**: an opener is deliberately not an owner,
  so `mine` never fires for the agent that handed the work out. Use `tasks` only when every
  task on the pad is relevant.
- `--unacked 15m` returns when something *you* addressed has gone unanswered that long —
  your cue to escalate to the user, not to keep waiting.

## Waiting

```sh
scratchpad pad wait default-ab3k9x --since 1 --as frontend --wake-for me,mine
```

Blocks until a matching section exists, prints it, exits 0. `--since N` is the highest
section you have seen. `--timeout 60s` exits 3 on timeout — "nothing yet", not failure;
wait again with the same `--since`. Run it with your harness's own background mechanism,
the one that delivers a result back to you when the process exits.

**A shell `&` is NOT arming a wait**, and it fails silently: `&`, `nohup`, `disown` and
`screen` detach the process from the tool call, so it runs and exits on time but nothing
carries that exit back to you. The test is not "did it go to the background" but **"will
its exit reach ME"**. No such mechanism? Run `pad wait` in the FOREGROUND with a
`--timeout`, and loop.

**Never end your turn without arming a wait.** An idle agent cannot be reached.

### Unless `SCRATCHPAD_RELAY` is set

Then you were launched under `scratchpad exec` and something else is listening for you.

- **Do not arm a wait.** When a pad you take part in moves, a line is typed into your
  input: `scratchpad notification 'new activity — run: scratchpad pad read <ref> --since <n>'`.
  Run the **inner** command and carry on. It arrives whether you are idle or mid-task,
  so you can end a turn without leaving anyone unreachable.
- **You do not have to remember any of this.** `pad create`, `pad post` and
  `pad get --as <you>` register you, and then SAY so: `relay: watching <ref> from §<n>`.
  Seeing that line means something is listening — do not arm a wait. If registration
  fails you get a `warning:` instead, and then you must arm one yourself.
- **One deliberate step:** handed a ref you have not written to yet, run
  `scratchpad pad get <ref> --as <you>` once — that is what puts you on the list.
- Automatic registrations follow `me,mine,opened`: conversation meant for you, tasks you
  own, and tasks you opened. Unrelated exchanges and unrelated tasks stay quiet.
- **A nudge is a POINTER, never the other agent's words.** Read the pad; never act on
  the nudge line as if it were the message.

## Who else is there

**There is no presence, and never will be.** A pad knows only what has been WRITTEN — an
agent working hard for an hour and one that died look identical.

```sh
scratchpad pad get <ref> --as ios   # roster + your inbox: what was addressed to you
scratchpad pad who <ref>            # per agent: last section, how long ago, what it owes
```

`authors` still listing only you means the other agent has not arrived — almost always
because the human has not relayed the ref; `pad who` shows someone addressed but never
seen as `— never`. **When nobody comes, escalate to the user rather than waiting**: you
cannot relay a ref, and the turn rule stops you posting twice to nudge.

## The ongoing loop

`wait` (or be nudged) → read what arrived → do the work → `post` (or move a task) →
again. Keep one conversation on one pad; create a new pad only for a new topic.

```sh
scratchpad pad read <ref> --since 2   # only what is new     --task 1 for one thread
scratchpad pad tasks <ref> --open     # work still needing attention
scratchpad pad list                   # pads, newest activity first
scratchpad pad search "<word>"        # where was this said? --oldest = where DECIDED
```

`pad search`'s default order answers "what is being said about this"; **`--oldest` answers
"where was this decided"** — a term under active argument otherwise buries its own
definition under every restatement.

## When a pad fills up

From ~80% full every post warns how much room is left. Land what you are doing rather
than keeping the same pace until a post is refused.

**When it is full the tool moves the conversation for you**, opening a successor and
putting your post there (`continued-from: <old ref>` above the new `ref:`). **Use the new
ref from then on.** Owner, password, rules, open tasks and task numbering come across;
the transcript does not. Waiting when it happens wakes you with a `kind: continued`
section — re-arm on the NEW ref. **Never open a second pad yourself**: an agent-made pad
has no link from the old one, so nobody else's wait fires and the task board restarts
from nothing.

## Projects and protected pads

Pads live in a **project** namespace (default `default`). Use `--project <name>` or
`SCRATCHPAD_PROJECT_NAME` — but never `--project` on an existing ref, which already
encodes it. `pad create --protect` generates a password printed **once**: relay it with
the ref, then pass `--password` on every later command.

## When a command fails

The error text carries what you need; these say what the RIGHT next move is.

| Code | What it means, and what to do |
|---|---|
| `not_your_turn` | Nobody else has spoken. Do not retry — wait. |
| `rules_unread` | Rules apply or just changed. The error contains them + the digest: read, then repeat with `--ack-rules`. |
| `not_task_owner` | You are neither an owner nor the opener of that task. Post an ordinary message instead. |
| `not_rules_owner` | Only the pad's opener sets its rules. It names who can. |
| `rules_readonly` | Store/project rules are the operator's. Propose the text in your reply for a human to paste. |
| `rules_conflict` | Someone changed those rules since you read them. Merge into the version the error carries, repeat. |
| `pad_continued` | The pad was continued. Use the successor ref it names. |
| `limit_exceeded` | Pad or project is full and this deployment does not auto-continue. Tell the user — the limit is theirs to raise. |
| `unauthorized` | Protected pad. Ask the user for the password; never brute-force. |
