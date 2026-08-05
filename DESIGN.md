# Scratchpad — CLI & MCP tools design

See IDEA.md for the overall concept.

## Terminology

- The primary entity is named **`pad`** (a "file" in IDEA.md) — this avoids clashing with the word "file", which is overloaded in the tool context. On disk it is still a file `<project>/<padid>.md`.
- **`section`**: a single post within a pad, numbered incrementally starting from 1.
- **Ref** (the fully-qualified identifier): `<project>-<padid>`, e.g. `projectx-abc123`.
- **`stream`**: the class a section belongs to — `message` (the conversation), `task` (the work ledger) or `rules` (the pad's house style). Declared per section by the `kind` metadata key; absent means `message`. Streams share one file, one numbering and one append-only sequence, but each defines its own rules.
- **`task`**: a unit of work opened by one section and moved by later ones. It has its own number, written `T<n>`, in a numbering space separate from section numbers (written `§<n>`).

## Section metadata — routing, threading, tasks

With two agents a pad simply *is* the conversation: every message is for the other one,
about the last one, and waking on any new section is exactly right. At five agents in
one pad those collapse into four separate questions, and answering them with one
mechanism is what makes a long pad unreadable:

| Question | Answered by |
|---|---|
| Who is this **for**? | `to` |
| Who does it **wake**? | the wait selectors — deliberately NOT `to` |
| What is it **about**? | `re` |
| Where is the team **at**? | the task fold |

### The metadata line

Every section already carries a timestamp comment directly beneath its header. That
line becomes the section's metadata line, in the `key: value; key: value` shape the pad
header line has always used:

```markdown
# 12 - pm - Order API contract
<!-- ts: 2026-08-02T10:30:00Z; kind: task; task: 3; to: ios, android; status: open -->
```

| Key | Meaning | Absent means |
|---|---|---|
| `ts` | timestamp — always present, always first | — |
| `kind` | which stream: `message` \| `task` \| `rules` | `message` |
| `rules` | `replace` on a rules section: the levels above do not apply | they are extended |
| `to` | comma-separated authors this section is addressed to | broadcast (message); **invalid** (task open) |
| `re` | the section number this one answers | not a reply |
| `task` | the task number this section concerns | unrelated to any task |
| `status` | `open` \| `wip` \| `blocked` \| `done` \| `dropped` | no status change |

- Parsing is **forward-compatible**: an unknown key is ignored, never an error, so a
  key added later does not break a reader that predates it.
- `to` entries are validated like authors (single line, no `" - "`, no `;`) and bounded
  (see Limits) — a new key does not get to be an unbounded resource.
- **Addressing is advisory, never access control.** Everyone can always read
  everything; `to` decides who is *woken*. That split is the entire point, and it is
  why `to` is not a permission.
- `re` names a section whatever its kind — **one parent, not a tree**. A pad is a
  linear transcript, and a tree would fight both the numbering and the append-only
  format for a distinction agents rarely need.
- **`re` implies `to`**: replying to a section addresses its author without having to
  repeat it.

### Backwards compatibility, in both directions

- **Old pads on the new binary**: every existing section has a bare `ts` line and no
  other key, so it parses as a broadcast `message` with no `re`. Behaviour is
  unchanged and **no migration is needed**.
- **New pads on an older binary**: today's parser trims the prefix and suffix and runs
  `time.Parse` over the whole remainder, so `ts: …; to: backend` fails to parse and the
  line falls through into the body. The section keeps its boundary, number, author and
  title; it loses its timestamp and shows one comment line as text. That degradation is
  **accepted**, and the pad format stays `scratchpad v1`.
- Consequently the new parser must **cut on `"; "` first** and parse `ts` from the
  first field only.

### Why not put `kind` in the section header

`# <n> - <author> - <title>` is the one line whose exact shape defines a section
boundary, and the parser accepts only `# <digits> - `. A header like `# task:12 - …`
fails that regexp on an older binary, drops into the body, and **loses the section
boundary** — a 600-section pad reads as a handful of sections and turn state comes out
wrong. That failure is silent and lands on the single piece of state every agent relies
on, whereas the metadata-line degradation above is visible and harmless.

The tempting part of such a change is that it looks additive (`^# (?:(\w+):)?(\d+) - `
still reads old files). It is not: the damage is in the other direction. If `kind` ever
moves to the header it must come with a `v2` bump that makes older binaries *refuse*
the file — never as an "optional additive" change.

## Streams — one sequence, two sets of rules

Physically there is **one** append-only sequence: one numbering, one file, one flock.
`kind` separates streams *logically*.

That is not a compromise, it is what keeps appends O(1). In a single file only one
region can be at the end, so a physical `# tasks` / `# messages` split would force a
read-modify-write of the whole file on every update to whichever region is not last.
A safe rewrite means temp-file + rename, and rename swaps the inode — while `openPad`
takes its flock on the pad file itself, so a concurrent writer holding the old lock
would write into a ghost. Logical streams also extend for free: a future `kind:
decision` is a new *value*, not a new region — no format migration, no new rewrite path.

Each stream carries its own rules, which is the property a physical split was wanted
for in the first place:

| | `message` | `task` | `rules` | `notice` |
|---|---|---|---|---|
| Counts for the turn rule | yes | **no** | **no** | **no** |
| `to` absent | broadcast | **invalid** on a task open | broadcast (`to` is rejected) | broadcast (`to` is rejected) |
| Wakes `wake_for: me` | yes | only via the task selectors | yes — before any selector | yes — before any selector |
| Changed by | nothing — append a reply with `re` | nothing — append a status event | nothing — append a newer rules section | nothing — it is a one-off report |

`rules` and `notice` (like `continued`) are checked **before** the selectors rather than
through them, and that is load-bearing rather than tidy. Selective waking exists to spare
an agent traffic it has no part in; it must never spare it something that changes what it
is allowed to do next. The `me` selector is answered by `concernsAuthor`, which counts a
broadcast only for a `kind: message` — so routing rules through it meant the agents that
had narrowed their waits were precisely the ones that never learned the rules had moved.

The turn rule therefore reads: **the last section whose kind is `message` holds the
turn.** Still fully derived, still no state outside the file — the derivation just
filters by kind before taking the last element. Consequence: a coordinator can open
five tasks in a row without hitting `not_your_turn`, and a progress report never steals
or grants a turn.

The filter is written `== message`, never `!= task`. Stated the other way round it
would hand the turn to every stream added later, on the day it is added, silently, in
the one piece of state every agent depends on — which is exactly what happened to
`rules` in review.

**Honest limit.** Like the turn rule itself, every rule here — ownership included — is
a **guard rail, not security**. Identity is self-declared (see Identity), so a
determined agent can label a message as a task or claim any author. These rules stop
accidents and drift between cooperating agents, which is what they are for.

## Tasks — an append-only ledger, folded

A task is not a row that gets edited. It is **opened** by one section and **moved** by
later sections, and its current state is a **fold** over those events — exactly as turn
state is a fold over the transcript. That is what lets a work ledger live in an
append-only file behind an append-only MCP surface: opening, updating and closing a
task are all ordinary `pad_post` calls carrying metadata, and **no mutating tool is
added**.

```markdown
# 12 - pm - Order API contract
<!-- ts: 2026-08-02T10:30:00Z; kind: task; task: 3; to: ios, android; status: open -->
Investigate the crash on resume — both platforms.

# 41 - ios - iOS: background timer
<!-- ts: 2026-08-02T14:05:00Z; kind: task; task: 3; status: done -->
Caused by the background timer; fixed in abc123.

# 47 - backend - Anything I can help with here?
<!-- ts: 2026-08-02T15:10:00Z; task: 3; to: android -->
```

§47 shows the two layers: `kind: task` marks a section as part of the task's **record**,
while a bare `task:` merely **cross-references** it. Without that split every remark
about a task would land in the ledger, and the board would grow back to the size of the
pad — which is the problem the board exists to solve.

### Numbering

Task numbers are their **own** space, dense and starting at 1, allocated the way
section numbers already are: `max(existing task numbers) + 1`, computed under the
exclusive flock `Post` already holds, in the metadata scan it already performs. Nothing
is stored — the counter is derived, so a hand-deleted section simply frees its number
back, matching the existing rule for turn state.

Reusing the opening section's number as the task id was considered and rejected: it
makes task ids sparse and unmemorable (`T288`), and it conflates *which task this
concerns* with *what I am replying to* — two genuinely independent facts, as §47 shows.
Numbers are **never recycled**, so an old `task: 3` reference can never come to mean a
different task.

Everything user-facing writes `T3` for tasks and `§12` for sections: two number spaces
appear on the same screen, so they must never look alike.

### Ownership

| Role | Who | May |
|---|---|---|
| **Owner** | the latest `to` set | report on **their own slice** (`wip` / `blocked` / `done`) |
| **Opener** | the author of the opening section | reassign (`to`), `dropped`, force-close |

A task open without `to` is invalid: **a task must have an owner.** The opener is
deliberately *not* an owner — under the per-owner fold below, the owner set means
precisely "the parties whose completion is required", and a coordinator is not one of
them. But without the opener's management rights a task assigned to an agent that never
comes back would be immortal, so those rights are separated rather than dropped.
Enforcement lives in `Post`, under the flock, next to the turn rule — never in a
surface. Violations return `not_task_owner`.

**Reporting and reassigning are two rights arriving through one section, so they are
checked twice.** `CheckTaskOwner` admits owners *and* the opener, because an owner must be
able to report; `CheckTaskReassign` then asks the narrower question the table above
actually answers — may this author move the WORK. Without the second check the first one's
admission was enough: an owner writing `--status done --to someone` reassigned the task,
and since the fold publishes states only for *current* owners, its own `done` went out with
it. The task then read as unfinished work belonging to an agent that had never touched it,
with nothing in the transcript recording the handover. Restating the existing owners is not
a reassignment and is allowed — it changes nothing, so refusing it would only punish an
agent for being explicit.

The same defect has a second door, and a quieter one: **`re` implies `to`, but never on a
task event.** On a message that convenience is right; on a task event it would write an
owner set the author never typed, handing the work to whoever wrote the section being
answered. There is no `--to` in that command to notice afterwards. `re` still records what
the event answers.

`to` on a task event is the owner set and not addressing, which is the root of both. It is
not being renamed: every existing pad replays its owner set from that key, so a key meaning
one thing in an old file and another in a new one is the failure this format works hardest
to avoid. The cost is that a task event has no way to address anybody, which the CLI help,
`SKILL.md` and the refusal message all have to say out loud.

### The two-level fold

A task may have several owners, and that is the common case: one investigation covering
iOS and Android is one piece of work, and splitting it into parallel tasks means two
threads whose statuses have to be reconciled by hand.

Multi-owner is only honest if completion is tracked **per owner**. With a naive
last-event-wins fold, the first `done` closes the task and the other owner's work
disappears from the board — a correctness bug, not a cosmetic one. No extra key is
needed to fix it, because **every event already records its author**:

1. **Per owner** — that owner's state is the last `kind: task` event *written by them*.
2. **Aggregate** — `done` only when every current owner is `done`; a `dropped` or
   force-close by the opener overrides; otherwise the task is open / wip / blocked.

**Reopening resets the slices.** An opener posting `open` clears its own override *and*
empties the per-owner states, so every current owner reads `open` and has to report again.
Clearing the override alone is what this used to do, and it reads reasonable until you try
it: the owner states being recomputed from are the very `done` the opener is overruling, so
the aggregate returns `done` and the reopening section lands in the file having moved
nothing. Disagreeing with a completion is the one moment reopening exists for, and it was
the one case where it did nothing at all. Emptying the map rather than setting each owner
to `open` is what makes it survive a reassignment carried in the same event.

| Field | Taken from |
|---|---|
| Title | the **opening** section's title, fixed |
| Owners | the latest `to` on a `kind: task` event |
| Status, per owner | that owner's last `kind: task` event |
| Outcome | the body of the last event |
| History | the whole event chain |

The title rule is load-bearing: an update's section title describes *the update*
("iOS: background timer"), not the task. Folding the latest title would silently rename
a task on every progress report.

Two properties fall out. **History is free** — a mutable table would say T3 is `done`
and nothing more, while the chain says it went open → wip → done, who moved it, when,
and what blocked it; across 600 sections that chain *is* the project narrative, which is
the whole reason to want a board. And **watching a task is free** — because task events
do not count for the turn, an agent can be woken by T5 reaching `done`, read it, and go
back to waiting without the turn rule obliging it to say anything.

A hand-deleted opening section leaves an **orphaned** task: status events with no title
or owner. It renders as orphaned and is never a parse error, matching the existing rule
that a vanished file is simply a deleted pad.

## Rules — the house style, and the one moment it is enforced

Everything above makes a long pad *navigable*. None of it stops a pad from becoming
unreadable in the first place, which is what actually happens: agents join one at a
time, each writes at whatever length seems reasonable to it, and six hundred sections
later every one of them is three screens long. No agent misbehaved. There was simply
nowhere to say how work is done here, and no moment at which anyone would have read it.

**Rules are prose.** A machine-checkable version was considered — a byte cap per
section, `require_to`, and so on — and rejected: what makes a pad unreadable is not
measurable ("put detail in a task, not in a status report"), and a rule an agent
understands adapts, while a limit it trips over just gets worked around. The one thing
enforced mechanically is that the rules were *fetched* before the post they bind.

### Three levels, extending

| Level | Where it lives | Why there |
|---|---|---|
| store | `<dir>/_rules.md` | how this deployment works, e.g. "detail goes in tasks" |
| project | `<dir>/projects/<p>/_rules.md` | inside the project, so deleting it takes the rules along |
| pad | a `kind: rules` section | inside the pad, so `rm` takes the rules along |

Effective rules = store + project + pad, concatenated, most specific last — the way a
global CLAUDE.md and a repo's own stack. `replace` (a first line of
`<!-- rules: replace -->` in a file, `rules: replace` on a section) cuts the chain for
the pad whose way of working is genuinely different rather than merely more specific.

**Two levels are files, one is a section, and the split is not an inconsistency.** A
rule set is *edited* — the new text replaces the old — which an append-only pad cannot
express and a file expresses natively. But a pad's own rules must vanish with `rm`
along with everything else about it, and "no state outside the pad file" is the property
the whole design rests on. So the pad level is a section, and being append-only it comes
out *better*: several rules sections are not several rule sets but **versions of one**,
the last in force and the earlier ones readable as history — which a file silently
throws away.

Cumulative pad rules were rejected for the same reason: with append-only accumulation a
rule can never be *removed*, only contradicted by a later "ignore rule 3", and the set an
agent must obey grows monotonically for the life of the pad.

### The gate: read what binds you

The rules are enforced at one point — a **post** — and what is checked is that the author
quoted the digest (8 hex of sha256 over the combined text) of the rules in force.
`pad_get`, `pad_wait` and `pad create`/`pad post` all hand back the full text with that
digest, so the second attempt is a flag away.

WHEN it fires again is the deployment's `rules.reack`, and the two answers are two
readings of what rules are for. Under `once` they are an induction: read on the way in,
then never asked for again. Under `on-change` (the default) they are a standing
instruction: every version binds everyone, so a version nobody read binds nobody.

`once` was the original design, and the argument for it was that a second gate would mean
remembering who has acknowledged what — subscription state, which this design has nowhere
else. That part was right. What it got wrong was the conclusion, on two counts:

- **A rules change did not, in fact, travel.** The claim was that a rules section is a
  broadcast and wakes everyone already on the pad. It did not: `Wakes` answers `me` through
  `concernsAuthor`, which counts a broadcast only for a `kind: message`, so a rules section
  woke `any` and nobody else — and the agents that had narrowed their waits were exactly
  the ones left posting under rules they had never seen. That is fixed independently (rules
  and notices now wake every waiter before any selector is consulted), but it means the
  original argument was never load-bearing.
- **The two file levels have no section at all.** A pad's rules are visible in the pad; the
  store's and a project's are a file two directories up. No amount of waking helps there.

So the choice was never "gate again" versus "let it travel" — it was "gate again" versus
"a person going round every agent session by hand", which costs that person their time and
each agent its context, and still ends with the new rules binding only the next arrival.

The subscription-state objection is answered rather than ignored. The receipt is written
into the pad, as `acked` on the section that quoted it. It is the same kind of thing as
turn state and the author roster: **derived from the transcript, nothing outside the
files**. A hand-deleted section takes its receipt with it, which is right — what the pad no
longer says, nobody said. It also costs no extra I/O to find, because it rides on the
section metadata line, which the append path already parses without touching any bodies.

Two consequences fall out and are handled where they arise, not papered over:

- An agent that WRITES a pad's rules has read them by construction. Its receipt records the
  digest that will be in force once its section lands, not the one from a line earlier —
  otherwise every agent is refused on its own next post for rules it just typed.
- A pad that fills up carries its rules into the successor, so it must carry the receipts.
  The post that lands over there always records one, because the successor holds none of
  the transcript that would otherwise vouch for it.

Like the turn rule and task ownership this is a **guard rail, not security**: an agent
that fetched the rules and did not read them can still quote the digest. It stops the
accident, which is what all the rules here do.

### Making a change arrive, not merely bind

The gate makes a new rule bind. It does not make it **arrive**: an agent parked in
`pad wait` learns about it when it next tries to speak, which may be an hour of work later
— an hour spent under the rule the change was meant to prevent.

Nothing but a pad file crosses that gap. Every agent's wait is its own process, so there is
no in-memory wake to deliver; `internal/watch` reports pad files and `_rules.md` is
deliberately not one; and `Wait` only counts a pad as having news when a new **section**
appears, so touching a file would wake every waiter to find nothing and sleep again.

So announcing a change means writing a section into each pad the level binds — a
`kind: notice` from the reserved `scratchpad` author, which takes no turn and wakes every
waiter regardless of selectors.

It is **on by default and per edit**: the checkbox at the bottom of the Web UI's rules
dialog ships ticked, and `--notify` defaults to true. That follows from what a rules edit
is FOR. A version nobody is told about binds only whoever happens to post next, while the
agents already at work go on under the old one — which is the exact failure the edit was
meant to prevent. Announcing nothing is the narrower case (a typo, a reworded line), and
it costs one click or `--notify=false`.

What stays true is that the STORE never does this on its own initiative. Every notice is
downstream of a person deciding to save; the default lives in the surfaces, not in
`NotifyRulesChanged`, whose own parameter has no default at all.

It is bounded, and the bounds are reported rather than silent: pads already continued, at
their section limit, or quiet for longer than `rules.notify_active_days` are skipped and
counted. Each of those is a pad that cannot USE the notice — one takes no posts at all, one
would be filled by it, one has nobody parked on it to wake. A fan-out that quietly did less
than it said reads as "everyone knows", which is the one belief this must not create
falsely. The count is also shown BEFORE the box is ticked, as "12 of 340": a number without
its denominator invites the wrong decision in both directions.

A **password-protected pad is not one of the bounds**, and the reason is worth stating
because the first cut had it the other way. A password keeps other AGENTS out of a pad; it
never said anything about whether the store may tell that pad its rules moved. Those rules
bind it like any other, and its agents are refused by the read gate like any other's — so
skipping it produces the one pad that is blocked without being told why. The append goes
through `PostRequest.ToolNotice`, which bypasses the password gate and nothing else: it is
a field store code sets on a path a person authenticated to, no surface can send it, and
what it writes says only that the rules changed — which every agent on the store may read
anyway. Reading and posting there still require the password.

### The whole loop: one rules change, end to end

The two halves above — a gate that makes a rule BIND, an announcement that makes it
ARRIVE — are easier to hold together as one sequence. This is a change to the store's
rules, saved from the Web UI with the box left ticked.

**1. The person, in the dialog.** Settings (store rules) or a project page (that project's).
The dialog opens holding the current text, that level's version (`if_digest`, never shown),
and the checkbox — ticked, with its count already fetched:

```
☑ Tell the agents on the affected pads  (6 of 7 — skipping 1 already continued)
```

The count arrives before the decision, and carries its denominator and its exclusions: a
bare "6" invites both wrong conclusions — that the store holds six pads, or that three
hundred are about to be written into.

**2. The server, in an order that is not arbitrary.**

```
PUT /api/rules  {text, replace, if_digest, notify}
   │
   ├─ 1. compare-and-set on if_digest
   │        stale → rules_conflict; NOTHING is written, NOBODY is told,
   │        and the dialog keeps the typed text beside the version that won
   │
   ├─ 2. write _rules.md                    ← the rules are safe from here on
   │
   └─ 3. if notify: list the pads this level binds
            skip: continued · full · quiet > rules.notify_active_days
            each survivor ← one `kind: notice` section from `scratchpad`
```

Step 3 runs **after** step 2 and can never fail the request. By then the rules are on disk;
reporting an error would tell the person the opposite of what happened, and they would edit
again. What went wrong travels in the response body, and the toast says both halves:
`rules saved — 6 pads told`, or `— 5 pads told, 1 could not be reached`.

**3. The agent, by whichever of three routes reaches it first.** They are independent on
purpose: no agent depends on having been running at the right moment.

| Where the agent is | How it finds out |
|---|---|
| parked in `pad wait` | the pad file changed → kernel fs event → woken, whatever its selectors say |
| busy, not waiting | its next `pad get --as <name>` / `pad wait` hands it the rules (stderr; MCP: the `rules` field) |
| not running at all | its next post is refused with `rules_unread`, carrying the full text and the digest |

The third is the backstop, and it is what makes the other two optional: an agent that was
switched off for three days is still stopped the moment it tries to write.

**4. The agent reads, then posts.** It quotes the digest, the post lands, and the store
records the receipt on that very section:

```
<!-- ts: 2026-08-05T…Z; to: pm; acked: 10da8dc7 -->
```

From here it is not asked again — until the next change moves the digest, and the loop
starts over.

Two things this sequence makes visible that the prose above does not. The gate and the
announcement are genuinely separable: leave the box unticked and every agent is still
bound, just not interrupted. And the notice deliberately carries **no digest**, because the
digest spans all three levels and therefore differs per pad — one number printed in a
broadcast would hand the wrong string to every pad that has house rules of its own.

`ToolNotice` is deliberately not folded into `SystemPost`. They are different privileges:
`SystemPost` says "a person is writing under the reserved identity", and the Web UI sets it
to edit a pad's rules — where it still unlocks the pad first, because that writes CONTENT
into someone's conversation. One field for both would have handed that path a password
bypass nobody asked for.

The pad level has no announcement, and that is not an omission — a pad's rules already ARE
a section in that pad.

### The second gate: write on top of what you read

The read gate says nothing about *writing* rules, and the first deployment showed why it
had to. Rules are the only thing in this store that is **edited** rather than appended: a
message an agent disagrees with sits in the transcript beside its reply, but a rule it
overwrites is simply gone, and nothing records that it ever said something else. Two
agents both "improving" a pad's rules is not a conflict anyone notices — it is the second
one winning, silently.

So a rules write answers two more questions.

**On top of what** — every level carries its own version (`pad.LevelDigest`: 8 hex of
sha256 over what that level would look like on disk, so flipping `replace` moves it), and
a write quotes the one it replaces (`--if-digest` / `rules_digest`). Mismatch is
`rules_conflict`, and the refusal carries the version that won so the merge costs no
second read. A level with no rules yet is at `none`, quoted the same way — filling an
empty level is exactly the write two agents are most likely to race on.

This is per-LEVEL and deliberately not the combined digest the read gate uses. The two
answer different questions: `digest` is "what am I bound by", which spans every level in
force; a version is "is the thing I am about to overwrite still the thing I read", which
is about one file or one section. One token for both would fail a pad-rules edit because
somebody touched the store's — a conflict the writer can neither see nor resolve.

At the pad level the check runs inside `Post`, under the append's own exclusive flock, so
it is a true compare-and-set. At the file levels it is a read-then-write, and knowingly
so: serialising them would mean a lock file in the store dir — a fourth thing `doctor`
has to know about — to close a window of microseconds, when what this defends against is
measured in minutes.

**Who** — the marker's `rules` group, whose defaults say no:

| Level | Default | Because |
|---|---|---|
| `store`, `project` | `ui` | They are the operator's standing instruction to every agent that will ever work here. An agent that could rewrite them could rewrite its own instructions. The Web UI writes them, and so does an editor — they are markdown files in a directory the operator owns. The CLI and MCP get `rules_readonly`, which names both of those. |
| `pad` | `opener` | A pad is nearly always opened by the agent handing out the work, so the one who framed the job says how it is worked. Anyone else gets `not_rules_owner`, which names who can. |

Set to `agent` / `any` for a deployment where the distinction protects nobody. An unknown
value makes the marker fail to LOAD: every other bad setting degrades to something merely
wrong, this one would degrade to a permission nobody granted.

The Web UI is exempt from the policy — it is the surface the policy points at — and NOT
from the version check, because two tabs lose an edit exactly the way two agents do. Both
are claimed through `store.RulesWriter` (`ByUI` / `ByAgent`), a field on the request like
`SystemPost` beside it, so the exemption is something calling code states rather than
something an agent can arrange.

Like the read gate this is a guard rail, not security: an agent determined to clobber the
rules can read them first. It stops the accident.

### The reserved author `scratchpad`

A person must be able to edit rules at every level, including a pad's. The file levels
need no identity; a section needs an author. Hence one reserved name, `pad.SystemAuthor`
= `scratchpad`: a fixed string, not derived from the executable name, for the same reason
the pad header says `scratchpad v1` — renaming the binary must not change what an
already-written file means.

`ValidateAuthor` refuses it, which also covers `to` (a `to` target is validated as an
author — and nobody holds a conversation with the tool). Only the rules-writing path
passes `ValidateAuthorAllowSystem`, and claiming the identity is a *field on the request*
(`SystemPost`) rather than an inference from the author string, so it takes a deliberate
act of the calling code and never a string an agent can send. It is excluded from the
roster, participants and inboxes: it is the tool recording a change, not a teammate.

It belongs to the **Web UI alone**. `scratchpad pad rules --set` used to fall back to it
when `--as` was omitted — a convenience for a person at a terminal that turned out to be
a hole straight through the opener policy, since any agent could take it by simply not
naming itself. The CLI now requires `--as`; a person deciding how a pad works uses the
UI, which is the surface that has no name to give.

This is also what lets the **Web UI write rules** without reopening the question its
read-only rule settled. That rule exists because posting needs an agent identity and
obeys the turn rule; a rules section has neither problem. Messages and task events stay
agent-only, which was the part that mattered.

### The naming law that came with it

Rules at the store and project level are *settings*, and settings must not be
indistinguishable from data. So the store gained a written rule rather than a
convention: **a file starting with `_` belongs to the tool; a pad is `[a-z0-9]{1,64}.md`
and nothing else.** It holds in both directions — pad ids are drawn from `a-z0-9`, so
`_rules.md` can never be a pad id and `ParseRef` rejects one that tries.

Everything that walks the store uses the same predicate (`pad.IsPadFileName`), which
also fixes a pre-existing hole: any stray `*.md` used to be parsed as a pad and reported
as corrupt. Now it is skipped — but not silently, because silence would hide a pad
renamed by hand: `doctor` lists strays as their own section, and `internal/watch` uses
the same predicate so a rules file never emits a change event for a ref nobody can
resolve.

## Waking — reading is universal, waking is selective

Today `wait` returns on **any** new section, so in a five-agent pad every agent is woken
by every exchange, including ones between two other agents. Three agents pay a context
bill for a conversation belonging to two. The fix separates two things that were never
the same: **reading stays universal** — the pad is fully readable by everyone, always —
and only **waking** is filtered.

Selectors form a union: a comma list on the CLI, an array over MCP.

| Selector | Wakes on |
|---|---|
| `any` | any new section — today's behaviour, and the default |
| `me` | `to` contains me ∪ `re` points at a section I wrote ∪ broadcast |
| `mine` | a task event on a task I own |
| `task:<n>` | a task event on that task, whoever owns it |
| `tasks` | any task event — for a coordinator |

Broadcast waking `me` is deliberate: a section with no `to` means "the whole team", and
should behave that way. It is also the migration path — in a pad written before this
change *everything* is a broadcast, so `me` behaves exactly like `any` and grows quieter
as agents start addressing. The incentive to address lands on the coordinator, which is
where it belongs.

`mine` is not covered by `me`: the section that *opens* a task carries `to`, but a
co-owner's progress update usually does not repeat it — and that update is exactly what
a co-owner needs to see. `task:<n>` deliberately ignores ownership: an agent blocked on
someone else's task needs to know when it lands.

The selector is named `wake_for` (`--wake-for`) rather than `for`, because the one thing
it must not be confused with is what the caller may *read*.

**Catch-up is never filtered.** Whatever wakes a waiter, the result carries the bodies
of the matching sections *and* the table of contents of every section skipped since
`since`. Waking selectively must never mean waking blind: an agent answering from stale
context is the exact failure this feature exists to prevent, and a silent gap would
cause it.

`since` remains a **section** number regardless of kind — task events advance it even
when they wake nobody.

**No subscription state exists.** A waiter passes its selectors on every call and the
store evaluates a predicate; there is no subscriber table to keep, expire or clean up.

## Knowing whether work is moving

An agent assigns work to an agent that is not watching the pad, and waits forever. The
obvious fix is presence — "who is currently waiting" — and it is the wrong one.

Presence is ephemeral, mutable state that an append-only transcript cannot express: a
crashed agent never appends "I left". Worse, it answers the wrong question and lies in
both directions — an agent implementing for two hours is not inside `wait` and shows as
absent, while an agent parked in `wait` with the wrong selector shows as present. What
matters is not whether a process is blocked in a syscall; it is whether the work moved.
And *that* is derivable from the transcript itself:

| Addressed | Acknowledged when |
|---|---|
| a message `to: X` | X posts any later section — X is alive and reading |
| a task owned by X | X posts a `kind: task` event **on that task** |

The task rule is stricter on purpose: an owner can be alive, busy talking about
something else, and still sitting silently on T7. `status: wip` is the "I have this"
signal, and this is the rule that gives it a job.

Three layers, all derived, all working over TCP:

1. **At post time.** `Post` already parses the pad's metadata under the flock, so the
   answer is in hand: posting to an addressee who has been silent for a long time
   returns a warning beside the successful post — *"android has not posted since §12
   (4h ago); nobody may be listening"*. This is the immediacy presence was wanted for,
   at no cost and with no state, delivered at the moment the sender can still act on it.
2. **At wait time.** `--unacked <duration>` puts a floor under the wait: it also returns
   when something this author addressed has gone unacknowledged that long, naming what
   is stuck. This is what bounds a wait that would otherwise never end.
3. **On demand.** `pad who` renders the board: per author, their last section and its
   age, plus what they owe.

The real fix sits upstream of all three and lives in the docs, not the code: **an agent
must not end its turn without arming a background `pad wait`.** That discipline is only
affordable once waking is filtered — an always-armed wait is expensive today precisely
because it fires on everything — which is why the selectors ship before tasks do.

If presence is ever built regardless, build it on `flock`: the kernel releases it on
process death, so it is crash-proof with no heartbeat and no staleness. Keep it out of
the pad files, and document its two limits plainly — it cannot see an agent connected
over TCP from another machine, and it cannot tell "busy" from "gone". It is a
diagnostic, never something an agent bases a decision on.

## MCP tools

Following the convention: names are `<entity>_<verb>` in snake_case, and **the server does not prefix the product name itself** (the aggregator/proxy attaches `scratchpad_*`). Input is a Go struct with a `jsonschema` tag, and descriptions are written thoroughly for agents to read. Every mutation returns the refreshed object in a single round-trip.

### List

| Tool | Role |
|---|---|
| `pad_create` | Create a new pad + post section 1 |
| `pad_post` | Post a new section (turn-based) |
| `pad_get` | Compact state: TOC + turn + participants — does NOT return content, cheap to poll |
| `pad_read` | Read section content |
| `pad_wait` | Long-poll waiting for a section that matches the caller's selectors (timeout capped) |
| `pad_tasks` | The derived task board, or one task with its thread |
| `pad_rules` | The rules in force (store + project + pad), as layers plus a digest and a per-level version |
| `pad_list` | List pads |
| `project_list` | List projects + pad count |

There is no `pad_delete` / `pad_update` over MCP: a pad is append-only, and deletion/cleanup is the user's job via the CLI. **Tasks do not change this** — `pad_tasks` is read-only, and a task is opened, moved and closed by `pad_post` carrying metadata, so the agent surface stays append-only in the true sense.

### `pad_create`

```
input:  { project?, author, title, content, protect?, ack_rules? }
output: { ref, project, pad_id, section: 1, next: 2, password?, turn: {...} }
```

- Omitting `project` → the default project (env `SCRATCHPAD_PROJECT_NAME` → config `default_project` → `"default"`). The project is auto-created if it does not exist (validated against `a-z0-9`).
- **There is no password input.** To protect a pad, use `protect: true` — **the server generates the password itself** and returns it exactly once in the create result; the user/agent does not have to think up a password. Every subsequent call on this pad must include that password (the user passes it to the other agent along with the ref).
- The server generates a random `pad_id` (`a-z0-9`, avoiding easily-confused characters) and returns the complete `ref` for the user to copy-paste into another session.

### `pad_post`

```
input:  { ref, author, title, content, password?,
           to?: [author], re?: n,
           task_open?: bool, task?: n,
           status?: "open"|"wip"|"blocked"|"done"|"dropped",
           ack_rules?: string, set_rules?: bool, replace?: bool,
           rules_digest?: string }
output: { ref, section, next, task?, turn: {...}, warnings?: [string] }
```

- Enforces the turn rule **against the last `message` section**: if its author ==
  `author` → a clear error `"not your turn: you posted section N; wait for another
  agent (use pad_wait)"`. A `kind: task` section is exempt and does not change whose
  turn it is. A timeout or other error does not consume a turn.
- Returns the section id just posted + the next id (matching IDEA.md).
- `task_open: true` opens a task: the server allocates the next task number under the
  same lock and returns it as `task`. `to` is then **mandatory** — a task must have an
  owner. `task: <n>` references an existing task; the two are mutually exclusive.
  Opening is a separate boolean rather than the sentinel `task: "new"` because a field
  typed `integer | "new"` has no honest JSON Schema, and the schema is what an agent
  reads before its first call — it mirrors the CLI's `--task-open` / `--task N`.
- `kind` is not an input: it is implied. A section carrying `task_open` or `task` +
  `status` is a task event; anything else is a message. One fact, declared once.
- A `kind: task` section carrying `status` must come from an owner (their own slice) or
  the opener (`dropped` / reassign / force-close), else `not_task_owner`.
- `warnings` is advisory and never fails the post — it carries the "nobody may be
  listening" notice when an addressee has been silent (see *Knowing whether work is
  moving*). A post that succeeded must never look like it failed.
- `ack_rules` is checked only on an author's FIRST post to this pad, and only when the
  pad has rules; else `rules_unread`, whose message carries the rules and the digest to
  repeat with. `set_rules: true` makes the section the pad's rules — no `to`, no task, no
  turn taken.
- `set_rules` carries two more checks (see *Rules*): by default only the pad's **opener**
  may set them (`not_rules_owner`), and `rules_digest` must quote the pad level's current
  version from `pad_rules.versions` — `"none"` when it has none yet — else
  `rules_conflict`, whose message carries the version that won. It is a different token
  from `ack_rules`: that one says what binds you, this one says what you are replacing.

### `pad_get`

```
input:  { ref, password?, author?, kind? }
output: { ref, project, created_ts, section_count, authors, last_author, last_ts,
          turn: {...},
          sections: [ { n, author, title, ts, kind?, to?, re?, task?, status? } ],
          participants: [ { author, last_section, last_ts, unacked: [...] } ],
          inbox?: { unread: [n], unacked: [...] } }   # TOC only, no content
```

Compact by design: cheap, transfers no content. The agent looks at the TOC and then decides which section to read.

- The TOC carries each section's routing metadata, which turns it from a list of titles
  into a **map of the conversation**: an agent returning after a long absence can see
  who addressed whom and which sections belong to its own thread, then read only those.
  For a 600-section pad that is the difference between one cheap call and a full read.
- `kind` filters the TOC to one stream.
- `author` adds `inbox`: sections addressed to that author since their own last post,
  plus what they owe. Derived, not stored — "unread" means "you have not posted since
  it", which is the observable fact and the one that matters.
- `authors` is the pad's roster: every agent that has POSTED, in the order each first
  appeared, derived from the section headers and never stored — an author exists only by
  having posted. It is not `last_author` (the turn keys off who spoke last) and it is not
  `participants`, which also counts an agent that was addressed and never answered.

### `pad_read`

```
input:  { ref, password?, section?, since?, kind?, task? }
output: { ref, sections: [ { n, author, title, ts, content, kind?, to?, re?, task?, status? } ] }
```

- `section` = read exactly 1 section; `since` = every section with n > since; omitting both = the entire pad.
- `kind` and `task` narrow the same selection: `task: 3` reads that task's thread — the opening section and everything carrying `task: 3` — which is how an agent reads about one piece of work without reading the pad around it.
- Content is capped at 64KB/section (see Limits), so returning it through the tool result is valid (text, bounded — not out-of-band file bytes).

### `pad_wait`

```
input:  { ref, since, timeout_s?, password?,
           author?, wake_for?: ["any"|"me"|"mine"|"tasks"|"task:<n>"], unacked_s? }
output: { ref, changed: bool, reason?: "match" | "unacked", section_count, last_author,
          sections?: [ { n, author, title, ts, content, ... } ],   # the MATCHING sections
          skipped?:  [ { n, author, title, ts, ... } ],            # TOC of everything else
          unacked?:  [ { what, to, since_section, age_s } ] }
```

Following a standard long-poll pattern:

- `timeout_s` defaults to 60, **capped server-side at 300s** (safe with respect to an MCP client's per-request timeout); ≤0 or exceeding the cap → clamped, not an error.
- Internally it re-checks periodically (~750ms), it does not push.
- **A timeout is not an error**: it returns `changed: false` + compact state — the agent distinguishes "time ran out" from "broken", and calls again itself if it wants to keep waiting.
- The description teaches the agent: "Use this instead of polling pad_get in a loop".
- `wake_for` defaults to `["any"]` — today's behaviour, so an existing caller is
  unaffected. Anything other than `any` requires `author`; the selectors are a union
  (see *Waking*). The predicate is evaluated **in the store**, not by the caller:
  filtering after the round-trip would save no context, which is the entire point.
- `skipped` is always the full TOC of sections above `since` that did not match. Waking
  is selective; **catch-up never is**.
- `unacked_s` adds a second way to return: something this author addressed has gone
  unacknowledged that long. `reason` says which condition fired, so an agent that wakes
  to `unacked` knows to escalate to the user rather than to keep waiting.

### `pad_tasks`

```
input:  { ref, password?, task?: n, open_only?: bool }
output: { ref, tasks: [ { task, title, status, opener, opened_section, opened_ts,
                          owners: [ { author, status, last_section, last_ts } ],
                          last_section, last_ts, outcome?, orphaned?: bool } ],
          thread?: [ { n, author, title, ts, kind, status? } ] }   # when task is given
```

The derived board — the answer to "where is the team at" without reading the pad. It is
a fold over the metadata, so it costs one metadata scan and never materialises a single
section body; that is what makes it affordable to call often on a 600-section pad.

`task: <n>` returns that task plus its `thread`: the opening section and every section
carrying `task: <n>`, in order — the per-task transcript an agent can read instead of
the pad. `owners` carries the per-owner status, so a multi-owner task reports "ios done,
android outstanding" rather than collapsing to one misleading verdict.

There is no `pad_task_update`. A task moves by `pad_post`.

### `pad_rules`

```
input:  { ref? , project?, password? }
output: { ref?, layers: [ { level, source, text, author?, section?, ts?,
                            replace?, superseded? } ],
          text, digest, versions: { store, project?, pad? }, history?: [n] }
```

Read-only, and the store/project levels have **no writing tool at all**: those are files,
and rewriting a file is not an append. A pad's own rules are set through `pad_post`
(`set_rules`) like any other section, which is what keeps this surface append-only in the
true sense — the same reasoning that keeps `pad_delete` out of it. Under the default
policy those levels are not an agent's to write anyway, so the shape of the surface and
what the deployment permits agree; widening `rules.store` opens the CLI, not a new tool.

`versions` carries every level that APPLIES here, empty ones included (`"none"`), because
filling an empty level is a write like any other. It is what `rules_digest` /
`--if-digest` quotes, and is not `digest`: see *The second gate* above.

`layers` is deliberately not flattened into `text` alone: an agent (and a person) needs
to know which line came from the store and which from this pad, because that is what says
where to go to change it. `superseded` marks a level a lower `replace` switched off,
rather than dropping it — a level that exists but does not apply is a different fact from
one that does not exist.

### `pad_list`

```
input:  { project? }
output: { pads: [ { ref, project, title, section_count, authors,
                    last_author, turn_author, last_ts, protected: bool } ] }
```

`last_author` wrote the most recent SECTION; `turn_author` wrote the most recent MESSAGE
and therefore holds the turn. Both are published because collapsing them gets one of the
two wrong: a change notification has to name who actually just wrote, while "whose move
is it" must not name whoever filed a task event or edited the rules — neither takes the
turn.

`title` = the title of section 1 (a pad has no name, so it borrows context from the opening question). A pad with a password still appears in the list (metadata), but its content cannot be read without the password.

### Identity (author)

The author is always **self-declared, from a single source**: the `author` param (MCP) or `--as`/env `SCRATCHPAD_AUTHOR` (CLI). There is no identity mechanism from the host (the `session_meta_key` and the `whoami` tool have been dropped): both the CLI and MCP independently need the author specified, and adding a second identity source from session-meta only causes confusion and redundancy — that mechanism belongs to tools that have an auth service, not Scratchpad.

### Common error semantics

- `not_your_turn` — includes who is currently blocked, and suggests `pad_wait`. Derived from the last `message` section only.
- `rules_unread` — a first post to a pad with rules, without the right `ack_rules`. The message carries the rules IN FULL plus the digest, so the retry needs no second call.
- `rules_conflict` — a rules write whose `rules_digest`/`--if-digest` is missing or stale. Missing and stale share the code (the remedy is the same) and not the sentence (they are not the same mistake); both carry the version that won, so merging needs no second call.
- `not_rules_owner` — a pad's rules written by someone other than its opener. Names the opener, so the caller knows who to ask.
- `rules_readonly` — the store's or a project's rules written by an agent under the default policy. Names both places a person can make the change.
- `not_task_owner` — names the task's current owners and its opener, so the caller can see whether to ask an owner or the opener.
- `no_such_task` — `task: <n>` references a task this pad never opened.
- `task_needs_owner` — a task was opened without `to`.
- `pad_not_found` — the ref is wrong / it was deleted by the user.
- `unauthorized` — the pad has a password that is missing or wrong (a single unified message, not distinguishing the two cases).
- `content_too_large`, `invalid_project_name`, `invalid_ref` — validation, with a message that states the rule clearly.

### A full pad continues; it does not end

`max_sections_per_pad` used to be a wall. The refusal was honest and useless: an agent
cannot raise a limit, and the pads that reach one are exactly the pads with the most
history behind them. The two things that could happen next were both bad — the work
stopped until a person noticed, or an agent opened a pad of its own and split the
conversation in half.

So the store does it, and does the parts an agent could not. `limits.on_full` picks
between them; `continue` is the default because the alternative has no exit that does not
need a person.

| | agent opens a second pad | the store continues the pad |
|---|---|---|
| Finding it | the ref exists only in one agent's head | both files name each other (`continues` / `continued_by`, plus a closing section) |
| Waiting agents | never woken; they wait forever on a dead pad | woken by the closing section, **whatever their selectors** |
| Ownership | section 1's author — a passer-by | `opener` copied in the header |
| Password | lost; the new pad is open | copied |
| House rules | gone | restated in the successor by `pad.SystemAuthor` |
| Open work | invisible; the board starts empty | carried as task events, owners and status intact |
| `T3` | means a different task on each side | means the same task: numbering continues via `tasks_from` |

Three decisions worth stating, because each had a plausible alternative:

**The transcript does not move.** Copying it would double every byte and still not make
two files one conversation. The old pad stays readable forever, one hop away; only writing
moves.

**The old pad is closed permanently**, not "closed while it is over the limit". Raising
the limit afterwards does not reopen it — `pad_continued` is returned even then. Two live
ends would be two conversations that both look current, which is the failure this feature
exists to prevent, arrived at from the other direction.

**Nothing merges the two.** Every view — turn state, the task board, `pad who`, the UI —
keeps meaning "this pad". A merged view would need each of them to quietly mean something
else, and one place forgetting is a turn computed across a boundary it cannot see. What a
reader gets instead is a link, which is enough to follow the work and impossible to
misread.

**The approach warning names the ending the policy actually produces.** `CapacityWarning`
takes the on-full policy as a bool, because a warning that says "before posts are refused"
under the default `continue` describes a wall that is not there — an agent then wraps up
against nothing, which is the opposite of the behaviour the warning is for. The advice is
the same under both policies (a successor keeps the tasks but leaves the transcript a hop
behind, so "wrap up here" holds either way); only the consequence differs, and a policy
that changed the advice too would be a second message to keep in step with this one.

Ordering, since a half-done continuation is the one outcome that would be worse than the
refusal it replaces: the successor is written FIRST, and only then is the old pad closed.
A failure before that point leaves the old pad untouched and the post refused as it always
was. A failure after it is reported with both refs, because both files are intact and a
person needs to know where the work went.

### Limits (every resource is bounded)

| Limit | Default | Configurable |
|---|---|---|
| `title` | 4KB | `limits.max_title_kb` |
| `content` per section | 64KB | `limits.max_content_kb` |
| Sections / pad | 1000 | `limits.max_sections_per_pad` |
| Pads / project | 1000 | `limits.max_pads_per_project` |
| `timeout_s` of `pad_wait` | cap 300s | `wait.max_s` |
| `to` targets per section | 20 | no — a hard bound in the format |
| `wake_for` selectors per call | 20 | no — a hard bound in the format |

The first five are **defaults, not ceilings**: a deployment that outgrows one raises it in
the marker. Because a running process re-reads the marker (see *Config reload* below), the
pad that just refused a post with `limit_exceeded` accepts the next one — no restart, and
nothing to migrate.

Tasks need no limit of their own: every task requires a section to open it, so
`max_sections_per_pad` already bounds them. Adding a config field that can never bind
would be one more thing to explain in `config.md` for no gain.

### Config reload

The marker is READ CONTINUOUSLY, not once at startup. `internal/watch` watches the file
the same way it watches the pad store — the file, never the writers — so an edit from the
Web UI, an editor, or a config-management tool all arrive identically.

Settings split in two, and the split is about honesty rather than importance:

- **Hot** (`display_name`, `default_project`, `limits`, `wait`, `rules`) — consulted per
  operation, so a new value simply applies to the next one.
- **Cold** (`instance`, `dir`, `tcp`, `ui`, and every derived path) — already bound by the
  running process. These are reported in the log when they change and applied on restart;
  swapping them in memory would leave the process serving the old port while claiming the
  new one.

A marker that fails to load leaves the running config alone. Falling back to defaults
would read a half-written file as a request for the built-in settings, and the one that
would silently change is `rules` — the setting where a wrong guess grants a permission
nobody granted.

## CLI

- **Binary: `scratchpad`**; the name in help/error is **derived from the executable** (appinfo), not baked into the message. Framework: **cobra** (HARD RULE).
- **Unified env prefix: `SCRATCHPAD_`**, with each env having a corresponding flag.
- Name-neutral: no host names baked in; host integration goes through generic flags.

### Command tree

```
scratchpad
├── init                 # initialize a CUSTOM dir (flag/env); the default dir self-bootstraps, so init is not required
├── rules   [--set <text|-> --if-digest <d>] [--replace]   # the store-wide rules (operator's by default)
├── serve                # MCP server: UDS by default; --stdio; --tcp opt-in
├── ui                   # Web UI for a human: browse, read, watch (loopback only) — see the Web UI section
├── doctor               # diagnostics, strictly read-only (see the Doctor section)
├── skills               # self-documenting docs (go:embed); skills docs <topic>; -o json
│                        #   skills install --into <dir> (env SCRATCHPAD_SKILLS_DIR) publishes
│                        #   SKILL.md; the destination is ALWAYS the operator's to name — this
│                        #   repo names no host, and a conventional path is a host's property
├── version
├── project
│   ├── list
│   └── rules   <project> [--set <text|-> --if-digest <d>] [--replace]   # the project's rules (operator's by default)
└── pad
    ├── create   --project <p> --as <author> --title <t> [--protect] [--ack-rules <digest>] [content | -]
    ├── post     <ref> --as <author> --title <t> [--password] [content | -]
    │              [--to a,b] [--re N] [--ack-rules <digest>]
    │              [--task-open | --task N] [--status open|wip|blocked|done|dropped]
    ├── get      <ref> [--as <author>] [--kind message|task]   # TOC + turn (compact)
    ├── read     <ref> [--section N | --since N] [--kind K] [--task N]
    ├── search   <pattern>                         # the only read that selects by CONTENT
    │              [--project <p>] [--pad <ref> [--password]] [--exclude-pad <ref>,…]
    │              [--author <a>] [--kind K] [--before <when>] [--after <when>]
    │              [--oldest] [--regexp] [--word] [--case-sensitive] [--limit N]
    ├── wait     <ref> --since N [--timeout 10m]   # for a background CLI wait
    │              [--as <author>] [--wake-for any|me|mine|tasks|task:N,…] [--unacked 15m]
    ├── tasks    <ref> [--task N] [--open]         # the derived board
    ├── rules    <ref> [--set <text|->] [--replace] [--as <author>]   # rules in force / the pad's own
    ├── who      <ref>                             # last activity + what each author owes
    ├── list     [--project <p>]
    ├── delete   <ref>                    # confirm with a human, --yes for automation
    └── purge    [--project <p>] --older-than <dur>   # bulk cleanup, confirm/--yes
```

Notes:

- `content` is taken via an arg or stdin (`-`) — this avoids shell-escaping issues with long content.
- `--as` defaults from env `SCRATCHPAD_AUTHOR` (convenient to set once for a whole agent session).
- `--task-open` allocates the next task number and prints it (`opened T3`); it requires
  `--to`. `--task N` moves an existing one. Both imply `kind: task`.
- `--wake-for` defaults to `any`, so an existing script or skill keeps working
  unchanged. It is spelled `--wake-for`, not `--for`, because the distinction it
  encodes — what wakes you versus what you may read — is the one a short name would blur.
- `pad tasks` and `pad who` are **read-only derived views**; they take the same shared
  flock as any read and add no state. Both are equally useful to a person at a terminal
  and to a coordinating agent, which is why they are CLI commands rather than UI-only.
- `pad who` exists because presence does not: it reports *last activity and outstanding
  acknowledgements*, which are derivable, instead of *who is currently blocked in a
  wait*, which is not (see *Knowing whether work is moving*).
- `pad search` is the one read that selects by **what was written** rather than by
  position, and it is a CLI (and store) capability rather than an MCP tool for now — the
  MCP surface stays append-only and small on purpose, and this can be added to it and to
  the Web UI later from the same `store.Search`. Four decisions are load-bearing:
  - **No index.** It reads the pads it looks at. An index would be state living outside
    the pad files, and everything here derives from them precisely so that a person with
    `rm` or an editor cannot leave a stale second copy of the truth. `--project`, `--pad`
    and `--exclude-pad` are what keep a large store affordable; memory follows the RESULT,
    not the store, because the scan streams and only matching lines are kept.
  - **Titles are searched with bodies.** A section title is the most deliberate statement
    of what a section is about, and it is the index a person reads a long pad by.
  - **Order is a question, not a preference.** The default — newest pad first, a pad's
    hits kept together — answers "what is being said about this". `--oldest` answers
    "where was this DECIDED", which is almost always the FIRST time a word appears, and
    it deliberately drops the grouping because "which came first" is about absolute time.
    `--limit` applies AFTER ordering, so `--oldest --limit 5` keeps the earliest five.
    The time window (`--before`/`--after`, a date or an age) filters SECTIONS, not pads:
    a pad that is busy today usually also holds the old decision.
  - **No reading through a password.** Protected pads are skipped unless addressed by
    `--pad` WITH `--password`; a search that read through protection would be a way to
    read a protected pad one noun at a time. Everything left out (protected or unreadable)
    is reported on stderr, and the COUNT appears on the summary line itself — an empty
    result must never be readable as "the word is nowhere in the store".

  stdout carries the table and nothing else, and a search that matched nothing prints
  nothing at all — the header row included. That is `grep`'s convention and it is a
  contract here: a lone header reads as one result to anything counting lines
  (`… 2>/dev/null | wc -l`), which is how a script concludes "found it" about a word that
  is not there. Everything a person needs to read the silence is on stderr.
- The three `rules` commands read without `--set` and write with it, at one level each.
  Writing is behind an explicit flag rather than "an argument means write", so a mistyped
  read can never overwrite the rules with the word that was meant as a filter.
  **`--set` carries the text** (`--set -` reads stdin) rather than being a boolean beside
  a positional argument — unlike `pad post`, whose content is positional. Rules are a
  bullet list, so the text nearly always begins with `-`, and a positional argument
  starting with `-` is a flag to any getopt-style parser. As a flag *value* it is simply
  the value.
  `pad rules --set` **requires `--as`**. It used to default to `scratchpad` — at a
  terminal that reads as a person deciding how the pad works — but the identity carries a
  person's exemptions with it, so the default handed any agent those exemptions for the
  price of omitting a flag. A person uses the Web UI, which is the surface that genuinely
  has no name to give.
  Writing at any level also requires `--if-digest`, the version of THAT level being
  replaced (`none` when it has none). It is a plain flag rather than a cobra-required one
  so the refusal comes from the store, worded identically on every surface and carrying
  the current rules with it. Reads print the levels' versions on their own line, because a
  level nobody has written yet has no heading to tuck its version into.
- `pad get --as X` and `pad wait --as X` print the rules to **stderr** when X has never
  posted in this pad — the moment before the first post, rather than the error after it.
- **`pad wait` via the CLI is not capped at 300s** (`--timeout` is arbitrary, defaulting to infinite until SIGINT) — this is exactly wait style #2 in IDEA.md: the agent runs it in the background (`run_in_background`), the command exits when a new section appears → waking the agent. Exit codes: 0 = a new section exists (printed to stdout), 3 = timed out. The new MCP `pad_wait` needs the cap because of the MCP client's per-request timeout.
- `delete`/`purge` follow the interactivity convention: prompt with a human (TTY), fail-fast with a process, `--yes`/`--non-interactive` to override; there is a root flag `--non-interactive` + env `SCRATCHPAD_NONINTERACTIVE`.
- The `pad *` commands operate **directly on disk** through a shared storage layer (flock when writing) — no running server is needed. The server and CLI share the same storage package, so they share the same lock discipline.

### Scratchpad directory

A single self-contained directory holds everything: config, pads, socket. **Default: `~/.scratchpad/`** — when nothing is specified, every command uses it and **self-bootstraps on first use** (creating the dir + marker + config.md + `projects/`). Self-bootstrapping the default is safe because the path is fixed relative to home, not dependent on cwd — cwd-relative paths are a classic source of stray stores, and here that is eliminated at the root: **there is no cwd-based inference**.

Specifying somewhere else (precedence high → low):

1. Flag `--dir <path>`
2. Env `SCRATCHPAD_DIR` — for projects that need their own storage (e.g. set in a repo's `.envrc`)
3. The `dir` field in the config file at `~/.scratchpad/` — a fixed machine-wide storage relocation
4. Default `~/.scratchpad/`

An explicitly-specified dir (flag/env/config) that has not been initialized → **a clear error pointing to `init`**, not auto-creation — a typo in an env must not be allowed to spawn a stray store. Only the default path is self-bootstrapped; `init` is therefore only needed for a custom dir or provisioning.

The standard need for "running the CLI from any folder in a repo lands in the same place" is met by the default dir; separation between projects uses a **project** rather than a separate store — a repo that sets `SCRATCHPAD_PROJECT_NAME` has every pad it creates land in the right project.

### Environment variables

| Env | Meaning | Default |
|---|---|---|
| `SCRATCHPAD_DIR` | Where all of Scratchpad's files live | `~/.scratchpad/` |
| `SCRATCHPAD_PROJECT_NAME` | The default project when a command/tool does not pass `project` | `default` |
| `SCRATCHPAD_AUTHOR` | The default author for `--as` | — |
| `SCRATCHPAD_NONINTERACTIVE` | Disable prompts (automation) | — |
| `SCRATCHPAD_UI_PORT` | Loopback port for the Web UI (`ui`) | `6711` |

Every env has a corresponding flag; on conflict, **flag > env > config file > default**.

```
~/.scratchpad/                   # the Scratchpad directory, self-contained, 0700
├── scratchpad.config.json      # marker + settings (see Marker file contents)
├── config.md                       # config guide, go:embed from a separate source file in the repo
├── _rules.md                   # store-wide rules (optional; absent = none)
├── scratchpad.sock             # unix socket, 0600 — derived from dir, not configured separately
└── projects/
    ├── default/
    │   ├── _rules.md           # this project's rules (optional)
    │   └── ab3k9x.md
    └── projectx/
        └── abc123.md
```

**A file starting with `_` belongs to the tool; a pad is `[a-z0-9]{1,64}.md` and nothing
else.** Settings must not be indistinguishable from data, and the rule holds in both
directions because pad ids are drawn from `a-z0-9` (see *Rules*). One predicate,
`pad.IsPadFileName`, is used by the store, the watcher and `doctor` — a second copy of a
naming law is a second law.

### Marker file contents

```json
{
  "type": "scratchpad",
  "version": 1,

  "display_name": "Scratchpad",
  "instance": "scratchpad",

  "dir": "",
  "default_project": "default",

  "limits": {
    "max_title_kb": 4,
    "max_content_kb": 64,
    "max_sections_per_pad": 1000,
    "max_pads_per_project": 1000
  },
  "wait": { "default_s": 60, "max_s": 300 },

  "tcp": {
    "port": 6710,
    "token_digests": ["sha256:..."],
    "allowed_origins": []
  },

  "ui": { "port": 6711, "no_auth": false }
}
```

- **Required header**: `type` (fixed, a recognition guard) + `version` (the marker's schema version).
- **Identity group**: `display_name` (the human-facing display name — deliberately *not* `project_name`, because "project" already means something different in Scratchpad), `instance` (a technical label: the socket name).
- **Storage/behavior group**: `dir` (optional — relocate storage elsewhere, meaningful only in a config at the default location; this is a deliberate exception to the "do not store paths" rule, acting as a pointer that the user requested be configurable via the config file), `default_project` (the default project, overridden by env `SCRATCHPAD_PROJECT_NAME`/flag).
- **Optional group, omit = default**: `limits`, `wait`, `tcp`, `ui`. `init` writes only the header + identity; the optional groups are added by the operator when needed (the defaults are explained in `config.md`). `tcp.token_digests` stores only the SHA-256 digest, never the raw token; once `tcp` is in the file, `serve --tcp` does not need the flag repeated (flags still win over the file on conflict). `ui` holds the Web UI's loopback `port` and `no_auth` — no origin allow-list, because the UI binds loopback and the browser's own origin is the only one that can reach it.

**Not stored in config**: paths (`projects/`, socket — derived from dir); author (per-agent, belonging to each session's env `SCRATCHPAD_AUTHOR`); a pad's password (belonging to each pad file's header — a pad is self-contained, `rm` cleans it, leaving no cruft in config).

- The marker has `type` + `version`: it rejects unknown files and refuses a version newer than the binary. The fixed identifiers (marker name, `type` value, candidate dir name) **do not change with the binary name**; conversely, every "run `X …`" message derives the command name from the running executable.
- Data (`projects/`) and the socket are all **derived from dir** — moving the dir moves everything.
- `init` is used for a custom dir (`--dir`/env) or provisioning; interactively it confirms before creating, and refuses to clobber an existing marker. The default dir needs no `init` (it self-bootstraps).
- Docs discipline: `config.md` is a separate source file in the repo (embedded into the binary), its content is purely user-facing — it explains each field of the marker, the resolution order, and the env vars, so that whoever opens the dir 6 months later understands it on their own. The rule "changing the config schema means updating the guide + bumping `version` in the same change" lives in the repo's `CLAUDE.md` (maintainer-facing), not in the deploy guide.

## Web UI (`ui`)

`pad wait` is an **agent** ergonomic: it blocks, and its exit code wakes a program. A
person gets nothing from that. `ui` is the human counterpart — browse projects and
pads, read one, and be told when a section lands.

It is a **separate loopback listener, not a fourth MCP transport**. Three things
differ from `serve`, and each one is why it does not belong on the same port:

- A browser cannot speak to a Unix socket, so this needs TCP regardless.
- Auth is browser-shaped: a one-time token in the URL printed at startup, exchanged
  for an `HttpOnly; SameSite=Strict` session cookie and then dropped from the address
  bar via a 303 — rather than MCP's bearer token on every call.
- Its audience is a person, so its lifecycle is ad-hoc (`--open`, Ctrl-C), not
  host-supervised.

Default port **6711** (67xx range, next to the MCP TCP port). Guards: bind `127.0.0.1`
only, reject a non-loopback `Host` (the DNS-rebinding defence — a page resolving its
own name to 127.0.0.1 still sends its own `Host`), require a same-origin `Origin` on
state-changing methods, and serve a strict CSP (`script-src 'self'`).

With five agents in one pad the person's role changes: they are no longer a spectator
of a two-way transcript but the one who **intervenes when coordination breaks** — a
stale agent, an unclaimed task, an assignment nobody answered. So the UI's first job is
not "render the transcript" but **"show where the team is stuck"**; the transcript is
what they open *after* they know where to look.

**Read-only for the conversation.** A person watching a conversation is not a participant
in it: posting needs an author identity and would have to obey the turn rule, which
belongs to the agents' surfaces. **Tasks do not open a hole in this**: a task event has
an author too, and now an ownership check as well — a "close T3" button would have to
post as *somebody*, and the UI deliberately has no identity.

**Rules are the one exception, and they are one precisely because they fail neither
test.** A rules section does not take the turn, and it is authored by the reserved
`scratchpad` identity — the tool recording what a person changed, not an agent being
impersonated (see *Rules*). Without this a person could edit the store's and the
project's rules but not a pad's, which is the level most worth fixing when a pad is going
badly. Three `PUT` endpoints exist, all of them rules; messages and task events remain
agent-only. The escape hatch already
exists and is in the right place: a person has a shell, so `pad post --as <them>
--task 3 --status dropped` is how they intervene. The UI is for looking; the CLI is for
touching. Deleting a pad is available — **one at a time**, with
no row selection and no bulk endpoint: wiping a batch of transcripts is irreversible,
and `pad purge` already does that where a person states an age threshold and reads the
victim list before confirming.

### Change detection — push, not poll

The pad file is the single source of truth, so the UI watches the **store**, not the
writers: whoever appends — CLI, MCP server, or a person with an editor or `rm` — is
noticed identically, no writer has to cooperate, and `internal/store` stays ignorant
of who is listening. A writer-side hook would miss every uncooperative writer and drag
a "is the UI running?" question into the write path.

The mechanism is kernel filesystem notification (inotify / kqueue / FSEvents via
fsnotify) on the projects directory and each project directory — one watch per
project, not per pad. Two safeguards, because notification is best-effort: a slow full
rescan (30s) covers dropped events and filesystems where notification silently does
not work, and a failure to start fsnotify degrades to that rescan and reports itself
(`/api/status` → `watcher: "rescan"`, surfaced in the UI's Settings) instead of going
quietly blind. Emission is state-based (stat mtime+size against a snapshot), which
collapses the several write events one append produces into one event and makes the
rescan idempotent.

End to end: `write()` → kernel event (~ms) → 50ms debounce → `store.Get` under a
shared flock (so a half-written file is never read — the existing lock discipline
already solves this) → SSE → browser.

### HTTP surface

SSE, not WebSocket: the traffic is one-way, it rides ordinary HTTP, and `EventSource`
reconnects by itself, so a server restart heals with no client retry logic.

| Endpoint | Returns |
|---|---|
| `GET /api/status` | deployment, store path, watcher mode (`push`/`rescan`) |
| `GET /api/projects` | projects + pad count + last activity |
| `GET /api/pads[?project=]` | pad metadata listing |
| `GET /api/pads/{ref}` | header + turn + participants + full TOC (with routing metadata), **no section bodies** |
| `GET /api/pads/{ref}/sections[?before=&limit=&section=&kind=&task=]` | one page of bodies |
| `GET /api/pads/{ref}/sections/{n}/preview` | the opening excerpt of one section |
| `GET /api/pads/{ref}/tasks[?task=]` | the derived board, or one task |
| `GET /api/stuck` | across all pads: assignments unacknowledged past a threshold |
| `GET /api/rules`, `PUT /api/rules` | the store-wide rules |
| `GET /api/projects/{name}/rules`, `PUT …` | one project's rules |
| `PUT /api/pads/{ref}/rules` | append the pad's rules as `scratchpad` (no GET: they ride with the pad) |
| `POST /api/pads/{ref}/unlock` | verify a protected pad's password once per session |
| `DELETE /api/pads/{ref}` | delete one pad (no bulk counterpart, by design) |
| `GET /api/config`, `PUT /api/config` | the deployment's own settings — see below |
| `GET /api/events` | SSE stream of pad changes |

The rules of a pad **ride along with `/api/pads/{ref}`** rather than sitting behind their
own GET, following `participants` and for a stronger reason: the header must say whether
this pad *has* rules before the person decides to open them, so the digest has to be in
that response anyway — and once it is, the text is a few hundred bytes on a payload that
already carries the whole TOC. Like the section titles it is withheld while the pad is
locked, and it is built from the pad already parsed for the response — reading it again
would rebuild every section body a second time on the one request that is already the
most expensive.

`PUT /api/config` is the one write that is not about the conversation at all: it edits
the marker, which is the OPERATOR's, not any agent's. It is allowed here for the same
reason the rules writes are — config takes no turn and carries no author — but what it
may set is a closed list: `display_name`, `default_project`, `limits`, `wait`. It may
never write `tcp`, `ui` or `rules`, because those decide **who may reach this deployment
and who may rewrite the operator's standing instructions**, and a browser session must
not be how either is granted. It writes through `config.UpdateMarker` (quote-the-version
+ atomic rename), so two tabs cannot silently overwrite each other, and only the hot
groups take effect without a restart.

The four writing endpoints go through the state-changing guards that already exist:
loopback bind, non-loopback `Host` refused, same-origin `Origin` required, and a
protected pad must have been unlocked in the session (exactly as `DELETE` requires).
One path detail has to be caught in the CLIENT because the server never sees it:
`encodeURIComponent` leaves `..` alone, and both the browser and Go's router normalise a
path before routing, so `/api/projects/../rules` IS `/api/rules` — an action a person
took as "edit this project's rules" would edit the store's. `lib/api.js` therefore
refuses a name that is not `a-z0-9` before building the URL.

Two placements follow the reasoning `preview` already established. **Participants ride
along with the pad response** because the strip is always on screen — a second
round-trip for something never hidden is pure latency. **Tasks are their own endpoint**
because the board is a tab: a person reading the transcript should not pay for a panel
they have not opened.

`/api/stuck` is the one genuinely new *page-level* need. A person opens the UI in the
morning to ask "what stalled overnight?", and that question spans pads — answering it
per-pad means opening every pad to find the one that is stuck.

The task fold lives in the shared logic package and is exposed here, never
reimplemented in JavaScript. A second implementation of "what is T3's status" that
drifts from the CLI's is exactly the failure this design is arranged to prevent.

**Sizing for real pads drove this shape.** A pad reaches hundreds of sections of long
agent prose, so nothing loads a whole one: the TOC carries no bodies, bodies arrive
one page (20) at a time newest-first, and `before` walks backwards on demand.

A protected pad's password is verified once and held in the server-side session (in
memory, never on disk), so paging costs no extra bcrypt round and the browser never
keeps the secret. Events carry **metadata only — exactly the level `pad_list` already
publishes** — with the last section's title omitted for a protected pad, so a
notification reveals nothing its listing entry does not.

Events additionally carry the new section's `kind`, `task` and `status`, because a
notification saying *"T3 → done"* is worth one saying *"the pad changed"*, and the task
panel cannot stay live without them. Routing metadata (`to`, `re`) and the task fields
are **subject to the same rule as the title**: omitted for a protected pad. They say
more than a listing entry does, and the boundary is the level `pad_list` publishes —
not "whatever the UI happens to find useful".

### Front end

An **app shell**: `index.html` ships the frame as static markup so it paints on first
byte, and the router swaps only the content region — the header, the sidebar and the
SSE connection survive every navigation. Hash routing (`#/pads/projectx-abc123`),
because the UI is served from the binary at `/` and hash mode needs no catch-all
rewrite; the router only reacts to `hashchange`, so ⌘-click and copy-link keep working.

Built on **puredashboard**, vendored into `internal/webui/assets/vendor/` and embedded
with `go:embed` — no build step, one binary. It is copied rather than a git submodule
because `go:embed` cannot read an un-checked-out submodule, which would break
`go install …@latest` and any non-recursive clone; `make vendor-ui` refreshes it.

A pad's **roster** — the `authors` the API derives — is shown wherever a pad is: as a
column in the pads table (capped, with a "+N") and in full under the title on the pad's
own page, each agent in the colour its transcript avatar already has. The pads table
gets it live, because a change event carries `authors` the same way it carries the
section count; the pad page repaints it on the same condition that rebuilds the author
filter, so an agent joining a conversation appears without a reload.

A pad renders as a timeline (it is a turn-taking transcript, not a document), newest
first — which also means "load older" appends *downward*, so the page never has to
preserve scroll position around content inserted above the viewport. A long section
renders clamped with an explicit expand. A section arriving while the person is
reading history offers a jump pill instead of moving the viewport.

Beside the transcript sits the **outline**: every section of the pad — not just the
loaded page — as one row each, in whichever order the transcript is showing, marking
where the reader is and which sections currently have bodies on screen. It is what
makes a 300-section pad navigable, since scrolling only ever covers the loaded page.
Hovering a row fetches that section's opening excerpt for a popup; that is a separate
endpoint rather than a field on the TOC because it is wanted for the two or three rows
a person points at, not for all three hundred. Below 1100px the rail becomes a
dismissable overlay — a fact about the window, so it never overwrites the reader's
stored preference.

The outline is the one part of the page built on puredashboard's `Reactive` base with
a keyed `repeat()`: it changes on every arriving section, every order flip and every
scroll, and rebuilding it would throw away its own scroll position each time. For the
same reason the rest of the page is mounted ONCE and updated in place — the transcript
is the only subtree that is rebuilt, so a half-typed "Jump to §" survives an agent
posting mid-sentence.

Three additions carry the multi-agent case, in the order a person needs them.

**The participants strip**, at the top of the pad view — per author, their last section
and its age, and what they owe. It is the first thing the eye lands on and the only
thing that tells a person they need to act at all:

```
pm §44 · 2m      backend §41 · 8m      ios §38 · 25m ⚠      android §12 · 4h ⚠⚠
                                        T3 unanswered 25m     T3 4h · §40 35m
```

**The rules, in a modal.** Deliberately not a third rail tab: rules are a short block a
person opens, reads and closes, so a permanent column of the transcript's width would be
the wrong trade, and it would drag the rail's own state — which tab is selected, where it
is scrolled — into something looked at once a session. One dialog component serves the
pad view, the project page and Settings, differing only in how many levels there are to
show; each level renders as its own block with its source, because knowing *which* level
a line comes from is what tells a person where to change it. It is reached from a chip
beside the pad's identity, from the `RULES` badge on the section itself, from a chip on
the project page, and from Settings.

Four things about how it reads, each of which was got wrong first:

- **Levels are named by reach, not by file.** "Everywhere in this store" / "Everywhere in
  project X" / "This pad only" — `store`/`project`/`pad` is the vocabulary of the disk
  layout, while what a reader needs is how far a rule reaches. For the same reason the
  `replace` option names the levels it switches off ("Ignore the store rules and X's rules
  — this pad follows only what is written here") instead of saying "the levels above",
  which points at nothing when you are looking at one box.
- **Rules render as the markdown they are written in**, so a list of habits reads as a
  list. Through the shared renderer, which builds nodes and never touches `innerHTML` —
  this text is written by agents. The editor stays plain text: you edit what you wrote.
- **The digest is not a label.** The chip says "Rules"; the digest sits at the foot of the
  dialog with the sentence that says what it is for ("an agent must quote this code on its
  first post"). A hash on a button answers the agent's question, not the person's.
- **Settings shows the store's rules in place**, not a summary with a button: the card is
  three lines, and someone who came to check what this store asks of its agents should not
  have to open a dialog to find out. The dialog is for CHANGING them.

**A Tasks tab beside the Outline**, in the existing right rail. A second rail is not
added: the rail is already the pad's index, and Tasks is another index of the same pad.
Selecting a task filters the transcript to its thread — the person's version of the
context saving `pad_tasks` gives an agent. Tasks earns the same `Reactive` + keyed
`repeat()` treatment as the outline, and for the same reason: it changes on every
arriving event and must not lose its own scroll position doing so.

**Routing shown inline in the transcript** — `to` chips, `re` as a back-link with a
"3 replies" affordance on the parent, and task sections visually distinct because they
behave differently (they do not take the turn). A broadcast draws no chip: absent `to`
already means everyone, and a chip on every legacy section would be noise, not
information. All of this is **free of extra requests** — `/api/pads/{ref}` already
returns the pad's entire TOC, so once the TOC carries routing metadata the reply links
and filters are computed from data the page has in hand.

Notifications are turn-aware — who moved and who it is on now, the one fact that
matters in a turn-taking protocol. With five agents that becomes noise, and the person
turns out to have the agents' problem #3: being interrupted by exchanges that are not
theirs. It gets the same answer and deliberately the **same vocabulary** — a Settings
filter of *everything* / *tasks only* / *one task* / *only when something is overdue*,
mirroring `wake_for` and `--unacked`, stored in the existing preferences. One concept
across both surfaces, not a second model invented for the browser.

Three of the four are a predicate over the arriving event; the fourth cannot be, and
that is the whole point of it. What makes an assignment overdue is that **nothing
arrived**, so a filter riding the event stream would be a setting that never fires. It
polls `/api/stuck` instead — the same derivation the overview renders — and announces an
assignment the first time it crosses the line. The first sweep after a tab opens only
records: whatever was already overdue is a backlog, not news, and announcing it on every
page load would train the person to dismiss the one notification that means something.
The poll costs nothing while the option is unselected, because the option is what starts
it. Task numbers are per pad, which Settings says plainly rather than leaving to be
discovered: *one task* means T`n` in every pad the scope allows, so following a single
task means narrowing the scope to that pad.

The Notification API needs a secure context and `http://127.0.0.1` qualifies, so this
works with no certificate. The honest limit, stated in Settings rather than discovered:
notifications fire only while a tab is open (backgrounded is fine, closed is not) — for
an unattended wait, `pad wait` is still the tool.

### Doctor

Following `doctor-command.md`: **strictly no side effects** — stat before opening, open read-only, never create the thing it is checking. Unlike every other command, `doctor` **does not error when the config dir cannot be resolved** — that is exactly what is being diagnosed; it reports the resolution process (the flag/env values, the candidate dirs it probed, and which dir has a marker).

Reports:

- **Resolution**: the running binary (its real path, symlinks resolved), version, `on PATH` (resolving to this exact file), a **PATH shadow** on its own line if the command name resolves to a different file (compared by inode/`os.SameFile`, **never executing** the file found); cwd, the config dir + the winning source, the marker path, and the derived `projects/` and socket paths.
- **Store**: does `projects/` exist? is it writable? the number of projects/pads (counted by stat/list, changing nothing); can the last pad file be parsed (read read-only).
- **Rules**: which of the three levels exist, and the digest of what a pad in the default project would get. This is where "the agents are ignoring the rules" starts, and the first question is whether the rules are where the operator thinks they are.
- **Strays**: files under `projects/` that are neither pads nor tool files. Every listing skips them; `doctor` is the one place that says they are there, so a pad renamed by hand does not simply disappear.
- Opt-in: `--content` (lists each pad's ref + section count), `--verdict` (a conclusion + next steps, walking through failure modes from the outside in), `--json`.

### Output streams (hard rules)

- Under `serve --stdio`: **stdout belongs to JSON-RPC**, and all logs/diagnostics go to stderr. The startup line "using config dir …" also goes to stderr. Test: `scratchpad serve --stdio >/tmp/out 2>/tmp/err` → `/tmp/out` must be empty.
- Operator commands (`pad *`, `init`, `doctor`): the **result** goes to stdout (clean, pipeable — important for `pad read`/`pad wait` when an agent parses the output), and **chatter/warnings** go to stderr.
- Never execute a file discovered on the filesystem (not even to ask its version); only `Stat`/`LookPath`/`SameFile`.

### Transports (`serve`)

1. **Default: Streamable HTTP over a Unix Domain Socket** in the config dir (ADR-008) — no TCP port is opened, protected by filesystem permissions + peer-credential (uid == getuid, fail-closed).
2. **`--stdio`**: a compatibility mode for hosts that spawn it themselves (Claude Code, Codex…). stdout belongs to JSON-RPC, and all logs/diagnostics go to stderr.
3. **`--tcp`** opt-in: loopback-only, a bearer token is mandatory (stored as a SHA-256 digest), Origin/Host guard, port in the 67xx range. This is the path for the "2 agents on 2 machines" case — for TLS, put a reverse proxy in front.

Lifecycle: supervised by the node/host (it does not daemonize itself), absolute paths via args/env, SIGTERM stops cleanly.

## Where the logic lives — `internal/pad` and `internal/store`

Everything above adds one grammar, five derivations and three rules. Left alone, those
would land in the surfaces, because **that is already happening**: fourteen sites across
`mcpsrv`, `webui` and `cmd` walk `pad.Sections` themselves, and "which sections do I
want" exists in three different vocabularies —

| Surface | Selection it invented |
|---|---|
| `mcpsrv/tools.go` | `section` \| `since` \| all |
| `webui/api.go` | `section` \| `before` + `limit` |
| `cmd/scratchpad/pad.go` | `since` |

Three implementations of one concept, before any of this design exists. The cause is
not a missing layer — `internal/store` *is* the engine. The cause is that it hands out
`Pad{ Sections []Section }`, an open bag of data, and a caller given a raw slice has no
choice but to interpret it. The fix is therefore **not another wrapper** (a wrapper
around the same slice leaks identically) but to raise the API to speak in the domain's
verbs and stop publishing the slice.

**Two packages, one direction.**

```
internal/pad/      pure: no I/O, no locks, no clock beyond an injected `now`
  pad.go           Pad, Section, Meta
  meta.go          the metadata-line grammar: parse + render, the ONLY place
  scan.go          bytes → Pad
  select.go        Selector: kind / task / since / section / window
  turn.go          turn state (message stream only) + the turn rule
  tasks.go         the two-level fold, ownership, "a task needs an owner"
  people.go        participants, acknowledgement, inbox, the silence warning
  wake.go          the wake selectors, evaluated as a predicate over a section
  errors.go        the coded errors the rules return

internal/store/    files, flock, limits — calls internal/pad to enforce and derive
```

Each rule sits in the file that owns the derivation it guards, rather than in a
`rules.go` of its own: the turn rule is one line away from turn state and the ownership
rule one line away from the fold it protects, so neither can drift from what it guards
without the drift being on screen.

`store` depends on `pad`; `pad` depends on nothing. The `Pad` type itself moves to
`internal/pad` — leaving it in `store` would force the storage layer to know what a task
is, which is the entanglement being removed. The derivations are pure functions, so they
are testable without touching a filesystem, and the flock discipline stays isolated in
the one package that has always owned it.

**One selection vocabulary.** `Selector{Kind, Task, Since, Section, Before, Limit}`
replaces all three. MCP's `since=`, the UI's `before=&limit=` and the CLI's `--since`
become three ways of *filling in the same struct*, and the surfaces shrink to what they
should always have been: parse a request, build a Selector, serialise the result.

**Enforcement has one home.** The turn rule and the ownership rule are checked inside
`store.Post`, under the exclusive flock it already holds — never in a surface. This is
already true of the turn rule; ownership joins it there. A rule enforced in three
surfaces is a rule with three behaviours.

**A machine-checkable invariant**, which is what keeps the refactor from decaying:

> Outside `internal/pad/`, nothing may write `range ….Sections`.

It greps, so it belongs in `make check` beside gofmt and vet. This repo already
documents hard rules in prose; this one enforces itself.

Worth stating plainly, because the feature list has grown a long way from IDEA.md: the
*surface* grew but the *kind* of complexity did not. There is no state machine, no
migration, no reconciliation, no cache to invalidate. Turn, tasks, ownership,
acknowledgement and inbox are all folds over one append-only log, in the same shape as
the original `TurnState()`. The only real risk is fourteen copies of a fold drifting
apart, and the invariant above is aimed exactly at it.

## CLI vs MCP — what lives where

Positioning: **the CLI is the primary path, self-sufficient for local use** — an agent with a shell only needs the binary, operating directly on disk, with no running server needed. **MCP is for special use cases**: embedding into an MCP host (via UDS), an agent/host that cannot spawn the CLI, cross-machine access via TCP, and tool discovery (the agent sees the schema + description right away, with no need to read docs).

| Capability | CLI | MCP |
|---|---|---|
| Create / post / view TOC / read / list (`create` `post` `get` `read` `list`, project list) | ✅ | ✅ |
| Wait for a new section | ✅ `pad wait` — **not capped**, runs in the background, exit code wakes the agent | ✅ `pad_wait` — **capped at 300s**, the agent loops itself using `since` |
| Selective waking (`--wake-for`, `--unacked`) | ✅ | ✅ (`wake_for`, `unacked_s`) |
| Tasks: open / move / close | ✅ `pad post --task-open/--task` | ✅ `pad_post` with task metadata |
| Tasks: the derived board | ✅ `pad tasks`, `pad who` | ✅ `pad_tasks` (read-only) |
| Rules: read | ✅ `rules`, `project rules`, `pad rules` | ✅ `pad_rules` |
| Rules: write a PAD's | ✅ `pad rules --as X --set --if-digest D` | ✅ `pad_post(set_rules, rules_digest)` — it is an append |
| Rules: write a store's / project's | ⚠️ `rules --set`, `project rules --set` — refused unless the deployment sets `rules.store`/`rules.project` to `agent` | ❌ — rewriting a file is not an append |

Both rules-writing rows carry the policy and the version check on top of the surface's own
shape: by default a pad's rules are its opener's, and the file levels are the operator's on
BOTH surfaces — the CLI's ✅ above is the command existing, not the permission.
| Delete / cleanup (`delete`, `purge`) | ✅ (confirm with a human, `--yes` for automation) | ❌ — the agent surface is append-only |
| Operations (`init`, `serve`, `ui`, `doctor`, `skills`, `version`) | ✅ | ❌ |
| Identity | `--as` / env `SCRATCHPAD_AUTHOR` | param `author` (self-declared, mandatory) |
| Needs a running server | ❌ — reads/writes disk directly (flock) | ✅ — needs `serve` (UDS / stdio / TCP) |
| Long content | via stdin (`-`), no shell-escaping worries | param `content`, capped at 64KB |
| Self-bootstrap the default dir | ✅ | ✅ (`serve` bootstraps when it resolves to the default) |
| Typical use case | a local agent with a shell; a user viewing, managing, cleaning up | an embedding MCP host; a host without a shell; remote via TCP |

Design consequence: the CLI and MCP use **one shared storage layer** (the same flock discipline), so they can be mixed — one agent uses the CLI, another uses MCP on the same store, and the turns still stay correct.

## Storing a pad on disk

Each pad is one markdown file, with the metadata header as an HTML comment on the first line, followed by the sections (formatted as in IDEA.md):

```markdown
<!-- scratchpad v2; created: 2026-07-11T10:29:00Z; opener: frontend; password: $2b$12$... -->

# 1 - frontend - How does API X work
<!-- ts: 2026-07-11T10:30:00Z -->

Content...

# 2 - pm - Order API contract
<!-- ts: 2026-07-11T10:42:15Z; kind: task; task: 1; to: backend; status: open -->

Content...
```

- `password` (a bcrypt hash) appears only when the pad is protected. The file remains one-file-per-pad, the user can `cat` it, and cleanup can be done with `rm` (Scratchpad treats a vanished file as a deleted pad — there is no state outside it).

### The header is keyed, versioned, and holds what the sections cannot say

The header is `scratchpad v<N>` followed by `key: value` fields. Both properties were
bought at the same time and for the same reason.

**Keyed, because positional parsing cannot be extended.** v1 read the line by cutting it
at `"; password: "`. Any field added before that point corrupted the timestamp; any field
added after it was absorbed into the hash, so a protected pad silently refused its own
password. A format whose only extension point breaks the field next to it has no extension
point.

**Versioned as a NUMBER, because v1's version was decoration.** It sat inside the literal
prefix the parser matched, so nothing ever read it: a v2 file was "not a scratchpad file",
the same answer as a shopping list. A version you cannot compare is not a version — it
cannot say "newer than me, upgrade", which is the only message worth having.

**`opener` is in the header because the sections genuinely cannot say it.** Everything
else about a pad's participants is derived (see *Authors*), and derivation is preferred
here — a derived fact cannot go stale. Ownership is the exception: for a pad that
CONTINUES another, section 1 is written by whichever agent happened to fill the previous
pad, so "the author of section 1" hands the pad to a passer-by. It is written once, by the
code that creates the pad, from an author that code has already validated. **No request
field sets it** — the same rule as `SystemPost` and `RulesWriter`, and for the same reason:
a privilege an agent can name is a privilege an agent will claim.

**Upgrading is the tool's job, not an operator's.** There is no `migrate` command and
nothing to schedule. A v1 pad reads normally; the first post to it rewrites line 1 with
`opener` taken from section 1's author — the answer v1 itself derived — under the exclusive
`flock` that post already holds. `pad.Upgrade` is pure and is the ONLY place that
derivation exists, so it cannot drift into a fallback that every reader has to remember.
A post that is refused rewrites nothing: "it was rejected" and "the file changed" must not
be true at once.

The rewrite is **in place**, never temp-file-and-rename. Rename swaps the inode, and every
lock here is taken on the pad file itself — a reader holding a shared lock on the old inode
would read a file no writer can see. That is the same reasoning that rules out a
rewrite-based section format above, applied to the one write that cannot be an append.

The honest cost, in both directions: an **older** binary meeting a v2 file calls it corrupt
("not a scratchpad file"), because v1 had no version to compare and nothing in v2 can
change what the old binary prints. Upgrade every binary that shares a store.
- The section header line is **unchanged and stays strictly parsed** (`# <digits> - `); everything added since lives on the metadata line beneath it (see *Section metadata*). The header is the line that defines a section boundary, and it is deliberately the one line that never grows.
- Writing: open the file with an exclusive `flock` → parse the metadata of every section → check the turn (last `message`) and, for a task event, ownership → allocate the section number and, when opening a task, the task number → append → release. **Nothing is stored outside the file**: turn state, task state, ownership and the task counter are all derived from the sections themselves, which is why a hand-edited or truncated pad heals instead of corrupting.
- One pass over the metadata answers all of it, and `Post` already performs that pass to find the turn holder — so ownership checks and task-number allocation cost no additional read.

## Key decisions vs IDEA.md

- Wait style 1 (MCP) = `pad_wait` capped at 300s, the agent loops itself; wait style 2 (background CLI) = `pad wait` uncapped, exits when there is a reply.
- The password is only access control (a bcrypt hash in the header), the content is plaintext. The user does not set the password themselves: `protect: true` at create time → the server generates it and returns it once.
- Delete/cleanup is only via the CLI (or a manual `rm`) — the agent's MCP surface is append-only in the true sense.
- IDEA.md left room in the file format "for the future (e.g. an `encrypted: true` flag per section)". The metadata line is that room being used: `kind`, `to`, `re`, `task` and `status` are optional keys on a line that already existed, so no existing pad changes and no migration runs.
- IDEA.md says "2 agents vs N agents in one pad — the same turn mechanism, no separate mode". That still holds, but N agents needed the turn rule to be stated more precisely: it governs the **message** stream. Bookkeeping is not conversation, and a coordinator dispatching work is not taking a turn away from anyone.

## Delivery

Both phases ship the engine split — it is cheap now and gets dearer with every feature
laid on top of the current shape.

| Phase | Contents |
|---|---|
| **1** | `internal/pad` split + Selector + the `make check` invariant; the metadata line (`to`, `re`); `--wake-for`; `--unacked`; the post-time silence warning; `pad who`; UI routing chips, reply links, participants strip |
| **2** | `kind: task` + ownership + the two-level fold; `pad tasks` / `pad_tasks`; task selectors (`mine`, `task:<n>`, `tasks`); UI Tasks tab, task thread filter, `/api/stuck`, notification filters |
| **3** | `kind: rules` + the three levels + the `_` naming law; `rules` / `project rules` / `pad rules`; `--ack-rules` and the `rules_unread` gate; `pad_rules`; the reserved `scratchpad` author; UI rules dialog and the three `PUT`s |
| **3b** | Who may CHANGE rules, and on top of what: the marker's `rules` policy group (`store`/`project` = `ui`, `pad` = `opener`); per-level versions + `--if-digest` / `rules_digest`; `rules_conflict` / `not_rules_owner` / `rules_readonly`; `--as` required by `pad rules --set`; the UI's merge-on-conflict editor |

Phase 1 addresses the stale-agent, fake-mention and irrelevant-wake problems; phase 2
addresses tracking a pad too long to read.

**Docs that must move with the code**, per the repo's hard rule — the pad file format is
part of the config schema:

- `internal/config/config.md` documents the on-disk format (its "Pad files" section shows the `ts` line). It is embedded and written into every Scratchpad dir, so it must be updated **in the same change that makes the binary write the new line** — not before, or it would describe a format the binary does not produce.
- `internal/skills/topics/usage.md` and `mcp.md` teach the CLI and tool surfaces; both gain the new flags/params, and `usage.md` gains the discipline this design depends on: **never end a turn without arming a background `pad wait`**.
- `config.ConfigVersion` does **not** move: the marker's schema is untouched. The pad file format stayed `scratchpad v1` through the metadata line (see *Backwards compatibility, in both directions*); it moved to v2 later, when the header gained `opener` — a header field, unlike a metadata key, cannot be skipped by a reader that does not know it.
