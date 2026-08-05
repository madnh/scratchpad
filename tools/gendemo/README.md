# gendemo — the demo store

Builds a throwaway Scratchpad dir holding pads with days of history, tasks in every
state, and assignments old enough to be overdue. It exists so the derived views —
the task board, `pad who`, `/api/stuck`, the UI's participants strip and its overdue
notifications — have something to show. A store you build by hand with the CLI shows
none of them: everything in it happened seconds ago and nothing is stuck.

```sh
make demo        # rebuild ~/.scratchpad-demo from scratch (about a second)
make demo-ui     # rebuild it, then open the Web UI on it
make demo ARGS="--dir /tmp/pads"
```

`make demo` always rebuilds. That is the point: the timestamps are relative ("assigned
five hours ago, never answered"), so a store built last week is a store where everything
is stale by a week.

**It cannot touch a real store.** gendemo leaves a `.gendemo` stamp in the dir it
creates and `--force` only deletes a dir carrying that stamp. Point `--dir` at
`~/.scratchpad` by mistake and it refuses rather than wipes.

## Why it writes pad files directly

A pad *is* its file. Turn state, the task board, who owes what — all of it is a fold
over the sections, with nothing stored anywhere else, so writing the file is writing the
pad. What the CLI cannot do is stamp a section with a time in the past, and the past is
the entire point of a demo store.

Two things keep that from becoming a second, rotting implementation of the format:

- Sections are rendered by **`pad.RenderSection` / `pad.RenderHeader`** — the same
  functions the store writes with. A new metadata key appears in the demo for free.
- Every event is checked by the **store's own rules** before it is appended:
  `ValidateMeta`, `CheckTurn`, the opener's claim on a rules section, `CheckTaskRef`,
  `CheckTaskOwner`, `NextTaskNo`, in the order `store.Post` checks them. A scenario that
  could not have happened cannot be built, and when a rule changes, the scenarios that no
  longer make sense fail loudly.

## The two files

| File | What it is |
|---|---|
| `scenario.go` | **the story** — the data you edit |
| `main.go` | the machinery: label resolution, rule checks, rendering, the `--force` guard |

Almost every change belongs in `scenario.go`.

## What the demo currently contains

Seven pads across two projects. Section numbers below are stable across rebuilds —
only the timestamps move — so they are safe to quote in a bug report, and they change
the moment you edit the scenario.

### `mobile-crash9x` — the flagship (48 sections, 5 agents, 5 tasks, 2 rule versions)

Five agents (`pm`, `ios`, `android`, `backend`, `qa`) over three days, plus `infra`,
who is assigned work and never appears.

| Task | State | Owners | Opened → last | Shows |
|---|---|---|---|---|
| T1 Crash on resume | `done` | ios:done android:done | §5 → §38 | the two-level fold: iOS reported `done` at §16 and the task stayed OPEN for another day and a half, because Android had not |
| T2 Order API contract | `done` | backend:done | §11 → §40 | an ordinary single-owner task, opened → wip → done |
| T3 Flaky checkout test | `done` | qa:done | §15 → §41 | `blocked` (§35) as a state someone else has to clear |
| T4 Migrate to the push SDK | `dropped` | android | §32 → §34 | the opener's management right: android refuses (§33), pm drops it (§34) |
| T5 Payment webhook signature | `open` | infra | §42 → §42 | **the overdue case** — opened 5 hours ago, owner has never posted |

Derived state: turn is held by `pm` (last message §47 — the rules at §48 do NOT take it);
`pad who` reports `infra — never — T5 (5h)`; `/api/stuck` lists T5.

Where to look for specific behaviour:

- **The two layers of `task:`** — §42 is `kind: task; task: 5; …; status: open` (a task
  EVENT) and §43 is `task: 5; to: pm` with no `kind` (a plain message that merely
  cross-references it). §8, §18, §20 and §33 are the same shape. In the UI they render
  as a `T5 open` chip versus a bare `T5` chip.
- **Replies** — §44 answers §43, §39 answers §38, §46 answers §45. `re` implies `to`,
  so those sections address their parent's author without saying so.
- **Broadcast vs addressed** — §47 addresses four agents; most of the middle of the pad
  is broadcast and draws no chip.
- **Rules, versioned** — §4 states the pad's rules and §48 tightens them after the
  release. The LAST one is in force; §4 renders as superseded, and the dialog lists it
  under "earlier versions". §48 following pm's own §47 is the proof that a rules section
  does not take the turn. Both are written by `pm`, who also opened the pad at §1 —
  under the default policy that is the only agent here whose rules these could be.

### Rules files (the two levels that are not sections)

`_rules.md` at the store root, and `projects/mobile/_rules.md` for the mobile project.
Together with the pad's own §48 they are the three layers `pad rules mobile-crash9x`
prints and the UI's rules dialog shows. Both are written by `main.go` from the constants
at the top of `scenario.go`.

Without them the rules dialog would be empty, `rules_unread` would never fire, and
`projects/` would have no file exercising the `_` naming law — so a demo of this feature
would be a demo of nothing.

The demo store is written with a plain marker, so it carries the DEFAULT rules policy:
those two files are the operator's (the UI edits them; `scratchpad rules --set` against
the demo store is refused with `rules_readonly`), and `mobile-crash9x`'s own rules are
`pm`'s. That is the point — the demo should refuse what a real store refuses.

### `mobile-apiq7k` — a plain pad (8 sections, 2 agents, no tasks)

`frontend` and `backend`, question and answer, every section a reply to the one before.
It is the control: a pad written before any of the routing/task machinery existed looks
exactly like this, and nothing about it changed. Turn is held by `backend`.

### `mobile-budget2v` — where a term was defined (3 sections, 5 weeks old)

`backend` and `ios` settling what "retry budget" means, thirty-five days ago. It exists
for **search**, and specifically for the two things search cannot demonstrate on a store
built five minutes ago:

- **`--oldest` (the UI's "Oldest first") does something.** The flagship restates the term
  twice this week (§20 and §40), so the default order — newest pad first — puts the live
  argument above the pad that DEFINED it. `--oldest` inverts that and the definition comes
  first. Both pads are needed: one recent, one old, same words.
- **A hit is cut AROUND its match.** §1's body carries one 380-character line with the term
  first appearing about 250 characters in. Cut from the front, that row comes back without
  the words that were searched for — which is what the CLI did before the window moved.
  The result shows `…` at both ends.

Turn is held by `backend`; no tasks, no rules of its own.

### `release-train42` — a coordinator dispatching (16 sections, 5 tasks)

`pm` opens five tasks back to back (§2–§6) without ever hitting `not_your_turn`, because
task events do not take the turn. T1–T4 end `done`; T5 (`docs`) has had no reply since it
was opened a day ago, and `docs` has never posted — a second, older overdue case, in a
second project, so `/api/stuck` and the overview have more than one row to show.

### `release-cut41old` + `release-cut41new` — a pad that filled up, and its successor

Two files, one conversation, and the only pair in the demo where the interesting thing is
the SEAM between pads rather than anything inside one.

| | `release-cut41old` (9 sections) | `release-cut41new` (6 sections) |
|---|---|---|
| Header | `continued_by: release-cut41new` | `continues: release-cut41old`, `tasks_from: 2`, `opener: pm` |
| Last/first section | §9 is `kind: continued`, written by `scratchpad` | §1 is the carried house rules, §2 the carried task — both `scratchpad` |
| Tasks | T1 `wip` (qa), T2 `done` (docs) | T1 `done` (the same T1), T3 `open` (release) |
| Title shown | "4.1 cut — coordination pad" | "QA: carrying on here" |

What each part is there to show:

- **The closing section** (§9 of the old pad) is what a waiter is woken by, and it names
  the successor. It is `kind: continued`, so it takes no turn — the old pad's turn still
  reads "anyone but qa", who wrote §8.
- **T1 keeps its number** across the two pads. The successor's `tasks_from: 2` is why the
  first task opened there is **T3**, not T1 — §6 says so in its own body, so a reader who
  lands on it without context still learns the rule.
- **`opener: pm` on the successor** although `qa` wrote its first agent section. Section 1
  is the tool's, section 3 is qa's, and neither owns the pad: ownership came across in the
  header. Under the default `rules.pad = opener` policy, `pm` is still the only agent who
  may write this pad's rules.
- **The title is not "House rules, carried over"**. `Pad.Title()` skips sections the tool
  wrote, or every successor in a listing would be named after its carried rules. This pair
  is what makes that visible; it was found by building this scenario.
- In the UI: the old pad's row carries a `closed` tag and the new one `continues`, and each
  pad page links to the other end.

### `release-rules8k` — a rules change that arrived (7 sections, 3 agents, 1 task)

The only pad whose story starts OUTSIDE it. Nobody here said anything to cause §4: a person
edited the STORE's rules — a file two directories up — and ticked the announce box.

| Section | Who | What it shows |
|---|---|---|
| §1–§3 | pm, docs, qa | ordinary traffic, each carrying `acked: 5cb1f7a2` — the receipt for rules that no longer exist |
| §4 | `scratchpad` | `kind: notice`: the announcement. Takes no turn, and wakes every waiter whatever their selectors say |
| §5 | qa | the receipt updated — `acked` is now the digest actually in force, which is why qa is not asked again |
| §6–§7 | qa | T1, opened *because* the new rules ask for it rather than narrating in the thread |

Derived state: turn is held by `qa`; T1 is `wip`; `docs` has not posted since §4.

That last part is the point of the pad, and it is a state no other demo store has anyone in:

```sh
scratchpad pad get release-rules8k --as docs   # stderr: the rules have CHANGED since you last posted
scratchpad pad get release-rules8k --as qa     # stderr: nothing — qa is up to date
```

`docs` still holds the old digest, so its next post is refused with `rules_unread`, carrying
the current rules in full. Under `rules.reack = once` neither agent would be asked at all —
which is exactly the difference the setting makes, visible in one pad.

The digests are not typed twice: `demoNewDigest` in `scenario.go` is computed from the same
rules this build writes. A receipt that quietly stopped matching would leave every agent in
the demo looking overdue for a read, and the store would still be internally consistent — so
no test would catch it.

> **When you change a scenario, update the tables above in the same commit.** The tool
> prints the derived state of every pad it builds — section count, turn, the board, the
> debts — so `make demo` gives you the new numbers to paste.

## Editing the story

An event is one section:

```go
{Ago: 2 * hour, Author: "ios", Task: "crash", Status: pad.StatusDone, Label: "iosfix",
    Title: "iOS: it is the background timer",
    Body:  "The timer is invalidated on suspend but the callback is already queued."},
```

| Field | Meaning |
|---|---|
| `Ago` | how long before *now* it was posted — **must never increase** down the list |
| `Author`, `Title`, `Body` | as they appear in the pad |
| `To` | who it is addressed to (advisory; everyone still reads everything) |
| `Re` | the **label** of the section it answers |
| `Label` | names *this* section so a later `Re` can point at it |
| `Opens` | opens a NEW task under this **label**; requires `To` |
| `Task` | the **label** of an existing task this section concerns |
| `Status` | moves the task — **setting this is what makes the section a task event** |
| `Rules` | makes the section the pad's rules; several of them are VERSIONS of one rule set, the last in force |
| `Replace` | with `Rules`: ignore the project and store levels instead of extending them |
| `Continued` | the section a full pad ends with, naming its successor; always written by `scratchpad` |
| `Notice` | the section a rules change is ANNOUNCED with; also `scratchpad`, and it wakes every waiter regardless of selectors |
| `Acked` | the digest this author had read — the receipt the read gate looks for under `rules.reack = on-change`. Spelled out because a demo pad is written straight to disk, so no `Post` call derives it |

**Nothing refers to a section or task by number.** Insert a line in the middle and
nothing renumbers; delete a line something replies to and the build fails with the label
it could not find, instead of silently pointing `re` at whatever now sits at that
number.

`Task` without `Status` is deliberate, not an oversight: it produces the cross-reference
layer — an ordinary message that mentions a task, takes the turn like any other remark,
and does not count as its owner answering.

### The rules the build enforces

These are the store's rules, not the tool's, so this list is descriptive — the
authoritative version is `internal/pad` and `store.Post`.

| Failure | What it means |
|---|---|
| `not_your_turn: you ("pm") posted section 12` | two **messages** in a row from one author. Task events are exempt — put someone else's line in between, or make the event a real task event |
| `not_task_owner` | only a task's owners (their own slice) or its opener (reassign / drop / force-close) may set `status` |
| `rules written by "x", but "y" opened the pad` | under the default `rules.pad = "opener"` policy a pad's rules belong to whoever wrote its first section. A scenario that needs otherwise has to ship a marker that allows it, not slip past the build |
| `task_needs_owner` | `Opens` without `To` |
| `no_such_task` | `Task` names a label that no earlier event opened |
| `re "x" names no earlier section` | the label is missing, misspelled, or defined *below* this event |
| `event N goes back in time` | `Ago` increased — the list is newest-last |
| `task "x" is opened twice` | reuse of an `Opens` label |

### Adding a scenario for a new feature

1. Append a `scenario{}` to `scenarios` in `scenario.go`. Give it a `Project`, an `ID`
   (both `a-z0-9`), and a one-line `Note` saying what it demonstrates — the note is
   printed on every build and is how the next person knows why the pad exists.
2. Write the events. Keep it short: a scenario that demonstrates one thing clearly beats
   one that demonstrates four things vaguely. The flagship is the place for volume.
3. `make demo` — the build tells you both whether it is *possible* and what it *derives*.
4. Check the derived state is what you meant:
   ```sh
   scratchpad pad tasks <ref> --dir ~/.scratchpad-demo
   scratchpad pad who   <ref> --dir ~/.scratchpad-demo
   ```
5. Update the tables in this file, and run `make check`.

### When the pad format grows a new key

Rendering and rule-checking both go through `internal/pad`, so a new key needs work here
only if a scenario should *use* it: add the field to `event`, map it into `pad.Meta` in
`build()` in `main.go`, and use it in a scenario. If the key changes what a section
*means* — the way `status` decides `kind` — say so in the `event` field table above,
because that is the part a reader cannot infer from the type.

## Gotchas

- **The demo store is disposable.** Anything posted into it by hand is gone on the next
  `make demo`. It is not a place to keep anything.
- **Timestamps move on every rebuild, structure does not.** Section numbers, task
  numbers and owners are fixed by the scenario; ages are recomputed from the clock.
- **`Ago` is measured from the run, not from a fixed date**, so "5 hours ago" stays
  five hours ago forever. Do not convert these to absolute dates.
- **The first event's `Ago` sets the pad's `created`** timestamp.
- The tool never starts a server and never touches the default dir. `make demo-ui` runs
  the UI as a separate step, on `--dir $(DEMO_DIR)` and a fixed port, so the demo never
  collides with a UI already serving the real store.
