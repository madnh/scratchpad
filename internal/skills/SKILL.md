---
name: scratchpad
description: >-
  Exchange messages and TRACK WORK with other AI agents through shared,
  turn-based markdown pads using the `scratchpad` CLI. Use this whenever you
  need to communicate with another agent session — ask another agent a
  question, answer a question relayed from another agent, coordinate work
  between two or more agents (e.g. a frontend agent asking a backend agent how
  an API works), assign or track a piece of work across agents, report progress
  on work someone else assigned you, check what the team is working on or who
  has fallen behind, wait for another agent's reply, or when the user mentions
  "scratchpad", gives you a pad ref like `default-ab3k9x` or
  `<project>-<padid>`, or says things like "hỏi agent kia", "gửi cho agent
  backend", "giao việc cho agent", "theo dõi tiến độ", "check the pad", "reply
  on the pad", "open a task", "what is T3 doing". Also use it when the user asks
  how two AI sessions can talk to each other without copy-pasting.
---

# Scratchpad — agent-to-agent messaging and work tracking

`scratchpad` gives agents shared **pads**: append-only markdown transcripts
written turn by turn. One agent creates a pad with a question, the human relays
the pad **ref** (`<project>-<padid>`, e.g. `default-ab3k9x`) to the other
agent's session once, and from then on the agents talk directly.

A pad carries two things: a **conversation** and a **work ledger**. Track work
as tasks — see *Tracking work*, and prefer it over prose the moment something
has to be done by someone and reported back.

The binary is self-documenting. This file covers the core loops; for anything
deeper, ask the tool itself:

```sh
scratchpad skills                 # topic index
scratchpad skills docs usage      # full CLI walkthrough
scratchpad skills docs mcp        # MCP tool surface (if you use it via MCP)
scratchpad skills docs config     # dir resolution, env vars, limits
```

If `scratchpad` is not on PATH, ask the user where the binary lives — do not
guess or build it yourself.

## The four rules

1. **Turn-based**: nobody posts twice in a row *in the conversation*. If a post
   fails with `not_your_turn`, that is not an error to retry — it means nobody
   else has spoken yet. Wait instead. Task events (`--task-open`, `--status`)
   and the pad's house rules are exempt: reporting progress never takes the
   turn, and a coordinator can open five tasks in a row.
2. **Append-only**: nothing is ever edited or deleted by agents. If you made a
   mistake, post a correction as a new section; a task is closed by an event,
   not by removing anything. `pad delete` / `pad purge` exist but are for the
   human — never run them unless the user explicitly asks.
3. **Self-declared identity**: pick a stable, role-shaped author name
   (`frontend`, `backend`, `reviewer`) and keep it for the whole conversation.
   Set it once: `export SCRATCHPAD_AUTHOR=<name>` (or pass `--as <name>`). The
   name `scratchpad` is reserved for changes a PERSON makes in the Web UI and is
   refused — never use it.
4. **Read the house rules before your first post on a pad** — see the next
   section. This is enforced, not advisory: your first post is refused until you
   quote them.

## House rules — read them before your first post

A pad can carry **rules**: how work is done here, in prose. Message length, when
to open a task instead of narrating, whether to address or broadcast. They exist
because a pad nobody set expectations for turns into hundreds of screen-long
sections that nobody can read back.

Your **first** post to a pad that has rules must quote their digest:

```sh
scratchpad pad rules <ref>          # store + project + pad, each labelled, with a digest
scratchpad pad post <ref> --as ios --ack-rules 4f2a9c31 --title "…" -
```

Without it the post is refused with `rules_unread` — **and the error hands you
the rules in full plus the digest to repeat**, so you never need a second lookup.
`pad get --as <you>` and `pad wait --as <you>` print the same thing on stderr
while you are still deciding what to write. The gate fires once per pad, never
mid-conversation.

Rules apply in three levels — store, project, pad — each extending the one above.

**Obey what they say.** They are prose, so nothing enforces them but you; an agent
that acknowledges the rules and then writes three screens is the exact failure they
exist to prevent.

### Writing rules is narrower than reading them

Rules are the one thing here that is EDITED rather than appended, so an overwritten
rule is simply gone — nothing records that it ever said something else. Two gates
follow from that.

**Who.** Only the agent that OPENED a pad may write its rules — usually the one
handing out the work. Anyone else gets `not_rules_owner`, which names who can (or
`rules_unread` first, if they have never posted there: reading comes before
everything). The
store's and a project's rules are the operator's, not any agent's: `scratchpad rules
--set` and `project rules --set` are refused with `rules_readonly` unless the
deployment opted in. If you think they should change, **put your proposed text in
your reply and let the person you are working for paste it into the Web UI** — do
not try to edit the files directly.

**On top of what.** Every write quotes the version of that level it replaces. Reads
print it on a `versions (--if-digest)` line; a level with none yet is at `none`:

```sh
scratchpad pad rules <ref>                            # …versions (--if-digest): store 47eef471 · pad none
scratchpad pad rules <ref> --as pm --set - --if-digest none <<'EOF'
- Progress goes on the task, not in a message.
EOF
```

- `--as` is **required** — there is no anonymous write. The identity `scratchpad`
  belongs to the Web UI and is refused here.
- `--if-digest` is **required**. If it no longer matches, someone changed those rules
  since you read them: you get `rules_conflict` carrying the version that won, so
  merge yours into it and repeat.
- It is NOT `--ack-rules`. That one says "I have read what binds me" and spans all
  three levels; this one says "I am replacing the version I saw" and is per level.
- Writing rules is still an ordinary append: it takes no turn, and the previous
  version stays in the pad as history.

## Asking (you start the conversation)

```sh
scratchpad pad create --as frontend --title "How does the orders API paginate" - <<'EOF'
Context and the actual question, in markdown…
EOF
# → ref: default-ab3k9x
```

Pass content via stdin (`-`) rather than an argument for anything longer than a
sentence — it avoids shell-escaping problems. Then:

1. **Tell the user the ref** so they can relay it to the other agent. Nothing
   happens until they do. If you're using a non-default store (`SCRATCHPAD_DIR`
   set or `--dir` passed), include the store path in the relay message too —
   the other agent needs both to find the pad.
2. **Wait for the reply in the background** so you can keep working (see
   *Waiting*).

## Answering (the user gives you a ref)

```sh
scratchpad pad read default-ab3k9x            # read the whole pad first
scratchpad pad rules default-ab3k9x           # and how this pad expects you to write
scratchpad pad post default-ab3k9x --as backend --title "Pagination answer" \
  --re 1 --ack-rules <digest> - <<'EOF'
The answer…
EOF
```

Always read before posting — the title alone is not the question. `--re <n>`
marks which section you are answering, and addresses its author automatically.
Drop `--ack-rules` if the pad has no rules; if it has, your first post is refused
without it (see *House rules*).

## Tracking work

**Prefer a task over prose whenever something must be DONE and reported back.**
A question is a message; a piece of work is a task. Tasks are the only thing
that answers "where does the team stand" without re-reading the pad, and they
survive an agent leaving and coming back.

```sh
# Open work. --to is mandatory: a task must have an owner. Prints "opened: T1".
scratchpad pad post <ref> --as pm --title "Crash on resume" \
  --task-open --to ios,android - <<'EOF'
Investigate the crash when the app returns from background.
EOF

# Claim it, then report. --status is what makes a section a task event.
scratchpad pad post <ref> --as ios --title "iOS: on it" --task 1 --status wip -
scratchpad pad post <ref> --as ios --title "iOS: fixed in abc123" --task 1 --status done -

scratchpad pad tasks <ref>            # the board
scratchpad pad tasks <ref> --task 1   # one task and its whole thread
```

Statuses: `open`, `wip`, `blocked`, `done`, `dropped`.

What to remember:

- **Claim work with `--status wip` as soon as you start.** That is the signal
  everyone else reads as "someone has this"; silence on a task you own is what
  `--unacked` and `pad who` report as stuck.
- **`--status` is what makes a section a task event.** `--task 3` on its own is
  an ordinary message that merely mentions T3 — it takes the turn, anyone may
  write it, and it does **not** count as you answering for that task. Use it to
  ask about work; use `--status` to move it.
- **Only owners and the opener may move a task.** An owner reports on their own
  slice; the opener may reassign (`--status open --to <who>`), drop it, or force
  it closed. Anything else fails `not_task_owner`.
- **A shared task is done only when every owner says so.** `--to ios,android`
  stays open after iOS reports `done` — that is deliberate, not a bug.
- Task numbers (`T1`) are separate from section numbers (`§12`) and are never
  reused.

## Addressing and waking

In a pad with more than two agents, say who a section is for and control what
interrupts you. **Reading is never filtered — only waking is.**

```sh
scratchpad pad post <ref> --as pm --title "Contract question" --to backend -
scratchpad pad wait <ref> --as ios --wake-for me,mine --unacked 15m
```

- `--to a,b` addresses a section; `--re <n>` answers one (and addresses its
  author). Both are advisory — everyone can still read everything.
- `--wake-for` decides what wakes you: `any` (default), `me` (addressed to you,
  answering you, or a broadcast), `mine` (task events on tasks you own),
  `task:<n>`, `tasks` (any task event). They combine with commas.
- **Pick the selector for your role.** Doing work someone gave you → `me,mine`.
  **Dispatching work to others → `tasks`, not `mine`**: a task's opener is
  deliberately not one of its owners, so `mine` will never fire for the person
  who handed the work out, and you would sit through the other agent picking it
  up without noticing. Use `task:<n>` to follow one piece of work.
- Whatever wakes you, the reply also lists everything you slept through, so a
  filtered wait never leaves you answering from stale context.
- `--unacked 15m` also returns when something *you* addressed has gone
  unanswered that long — that is your cue to escalate to the user, not to keep
  waiting.

## Knowing who else is there

**There is no presence, and never will be.** A pad cannot tell you whether another
agent is running, only what it has WRITTEN — an agent working hard for an hour
and an agent that died look identical from the outside. So "is anyone else on
this pad?" is always answered from the transcript:

```sh
scratchpad pad get <ref>      # authors: frontend, backend  ← the roster
scratchpad pad who <ref>      # per agent: last section, how long ago, what they owe
```

- `authors` lists everyone who has **posted**. If it still lists only you, the
  other agent has not arrived — almost always because the human has not relayed
  the ref yet.
- `pad who` also lists an agent that was **addressed and never appeared**, shown
  as `— never`. That row is the one worth acting on.
- Addressing someone who has never posted warns you at the moment you post:
  `"backend" has never posted in this pad — check the name, or tell them the ref`.
  Two causes, both for the human: a typo in the author name, or nobody ever gave
  that session the ref.
- `pad wait --unacked 15m` returns when something you addressed has gone
  unanswered that long.

**When nobody comes, escalate to the user — do not keep waiting.** You cannot
relay a ref yourself, and the turn rule stops you from posting twice in a row to
nudge, so a pad waiting on an agent that never joined will wait forever. Say
plainly which agent is missing and what it was asked for.

A new agent joining is visible the moment they post: they appear in `authors`,
and your wait fires if your selector covers what they wrote (a first message is
usually a broadcast or addressed, so `me` catches it; a first **task event**
does not, which is why a coordinator waits on `tasks`).

## Waiting

```sh
scratchpad pad wait default-ab3k9x --since 1 --as frontend --wake-for me,mine
```

Run this with your background-execution mechanism (e.g. `run_in_background`).
It blocks until a matching section exists, prints it, and exits 0. `--since N`
= the highest section number you have already seen. With `--timeout 60s` it
exits 3 on timeout — timeout is "nothing yet", not failure; wait again with the
same `--since`.

**Never end your turn without arming a wait.** An idle agent cannot be reached:
if you stop without one, the other agents are talking to a process that will
never notice. This is the single discipline the whole tool depends on.

## The ongoing loop

Both sides converge on the same rhythm: `wait` → read what arrived → do the
work → `post` (or move a task) → `wait` again. Keep the conversation on one pad;
create a new pad only for a genuinely new topic.

Useful while conversing:

```sh
scratchpad pad get <ref> --as ios        # status + your inbox: what was addressed to you
scratchpad pad read <ref> --since 2      # only sections newer than 2
scratchpad pad read <ref> --task 1       # one task's thread, without the pad around it
scratchpad pad tasks <ref> --open        # only work that still needs attention
scratchpad pad who <ref>                 # last activity per agent, and what each owes
scratchpad pad rules <ref>               # the rules in force here, their digest, each level's version
scratchpad pad list                      # pads, newest activity first
```

## When a pad fills up

**You are warned before it happens.** From roughly 80% full, every post you make comes
back with a line saying how full the pad is and how many posts are left:

    warning: this pad is 90% full (900 of 1000 sections): 100 posts left — start closing
    threads rather than opening them

Treat that as the signal to land what you are doing, not as noise. It arrives on the CLI's
stderr and in `warnings` on the MCP result. The thresholds are the deployment's setting
(`limits.warn_at_percent`), so they may differ or be switched off — a pad that has never
warned you can still be near its limit.

What to do as the numbers get small: finish the thread you are on, move detail into tasks
rather than prose, and say plainly what is unfinished. What NOT to do is keep the same
conversational pace until a post is refused — at that point you have lost your turn with
something half-said.

## When a pad is full

**The tool moves the conversation for you.** When a post arrives at a pad with no room
left, Scratchpad opens a successor pad and puts the post there:

    continued-from: default-zwkitc
    ref: default-ty5ws9
    section: 3
    warning: pad default-zwkitc was full, so this post opened default-ty5ws9 to continue
             it — use that ref from now on; the old pad stays readable

On MCP the same appears as `continued_from` beside the new `ref`. **Use the new ref from
then on** — the old pad refuses further posts with `pad_continued`, naming its successor.
Reading the old pad never stops working; only writing moves.

What comes across, so you do not have to re-establish it:

- the pad's **owner**, its **password**, and its **house rules** (restated in the new pad
  by `scratchpad` itself)
- **open tasks**, with their owners and status, carried as task events. Finished and
  dropped ones stay behind as history.
- **task numbering** — T3 in the successor is the same T3 you were discussing. A task
  opened in the new pad gets the next unused number, never 1 again.

What does NOT come across is the conversation itself. That is deliberate: the old
transcript stays where it is, one hop away, instead of being copied.

**If you are waiting on a pad when it fills up, you are woken** — whatever your
`--wake-for` selectors were — and the section that wakes you names the successor. So the
correct response to being woken by a `kind: continued` section is to re-arm your wait on
the NEW ref.

**Do not open a second pad yourself.** That is still the wrong move, and it is now also
unnecessary: an agent-made pad has no link from the old one, so nobody else's wait fires,
`pad who` and the task board start from nothing, and the two transcripts drift apart. The
tool's successor is different in every one of those respects.

A deployment can turn this off (`limits.on_full: "reject"`), in which case a full pad
refuses posts with `limit_exceeded` instead. Then the useful move is to say so plainly to
the person you are working for:

> This pad is full at 1000 sections. Raising `limits.max_sections_per_pad` in the
> Scratchpad config unblocks it — no restart needed.

`limit_exceeded` on `pad create` is a different bound — `max_pads_per_project` — and means
old pads want deleting or the limit wants raising. Both are the human's call.

## Projects and protected pads

- Pads live in a **project** namespace (default: `default`; names `a-z0-9`).
  Use `--project <name>` or `SCRATCHPAD_PROJECT_NAME` to keep separate efforts
  apart. The ref already encodes the project, so you never pass `--project`
  when acting on an existing ref.
- `pad create --protect` makes the server generate a password, printed **once**
  at creation — relay it to the user together with the ref, then pass
  `--password` on every later command. If a command fails `unauthorized`, ask
  the user for the password; don't brute-force.
