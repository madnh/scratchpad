package main

import (
	"time"

	"github.com/madnh/scratchpad/internal/pad"
)

// This file is the STORY. main.go is the machinery; everything you edit to show off a
// new feature belongs here.
//
// Two rules make it safe to edit:
//
//   - Sections and tasks are named by LABEL, never by number. Insert a line anywhere and
//     nothing renumbers; delete one that something replies to and the build fails loudly
//     instead of pointing `re` at the wrong section.
//   - Events run newest-LAST but are written as "how long ago", because that is how the
//     interesting cases are specified: "assigned five hours ago and never answered" is
//     the fact, not a date. Ago must never increase down the list.
//
// Adding a scenario for a new feature: append to `scenarios`. The build re-runs every
// rule the store enforces, so a scenario that could not have happened cannot be built.

const (
	minute = time.Minute
	hour   = time.Hour
	day    = 24 * time.Hour
)

type event struct {
	Ago    time.Duration // when it was posted, counting back from now
	Author string
	Title  string
	Body   string

	To    []string // who it is addressed to (advisory — everyone still reads it)
	Re    string   // the LABEL of the section it answers
	Label string   // name this section so a later `Re` can point at it

	Opens  string     // opens a NEW task under this LABEL; needs To
	Task   string     // the LABEL of an existing task this section concerns
	Status pad.Status // moving the task — this is what makes it a task EVENT

	// Rules makes this section the pad's rules. Several of them in one scenario is the
	// point rather than a mistake: the last is in force and the earlier ones are the
	// history the UI shows as superseded. Replace cuts off the project and store levels.
	Rules   bool
	Replace bool

	// Continued makes this the section a full pad ends with, naming its successor. Only
	// the tool writes one, so the author is always pad.SystemAuthor.
	Continued bool

	// Notice makes this the section a rules change is ANNOUNCED with (`--notify`, or the
	// checkbox in the UI's rules dialog). Like Continued it is the tool speaking, so the
	// author is pad.SystemAuthor — and like it, it wakes every waiter regardless of their
	// selectors. A demo needs one because it is the only kind that arrives in a pad from
	// OUTSIDE the pad: nothing anyone said here caused it.
	Notice bool

	// Acked records the digest this section's author had read, which is what the read gate
	// looks for under `rules.reack = on-change`. It is spelled out here rather than
	// computed because a demo store is written straight to disk: there is no Post call to
	// derive it, and a pad whose sections carry no receipt would show every agent as owing
	// a read the moment anyone opens the store.
	Acked string

	// TaskNo pins a task to a number instead of allocating the next one. It exists for
	// the sections a CONTINUATION carries over, where the number came from the pad before
	// this one and keeping it is the entire point.
	TaskNo int
}

// storeRules and projectRules are the two FILE levels of the demo. They are here for the
// same reason the pads are: `pad rules`, the UI's rules dialog and the rules_unread gate
// all show nothing on a store where nobody has written any.
const storeRules = "- Keep a message under 15 lines. Detail belongs in a task, not in a status report.\n" +
	"- Address what you write (`--to`); broadcast only what the whole pad needs."

var projectRules = map[string]string{
	"mobile": "- Reproduce before you claim a task, and say which device in the claim.\n" +
		"- Crash work is tracked as tasks; the conversation is for decisions.",
}

type scenario struct {
	Project string
	ID      string
	Note    string // one line, printed after the build
	Events  []event

	// Opener overrides who owns the pad. Normally that is whoever writes section 1, which
	// is what the header records — but a pad that CONTINUES another keeps the original
	// owner, and its section 1 belongs to whichever agent happened to fill the old pad.
	Opener string

	// The two ends of a continuation, and the task numbering that crosses it. They are
	// header fields, so they are set on the scenario rather than on any event.
	Continues   string
	ContinuedBy string
	TasksFrom   int
}

var scenarios = []scenario{
	// ── The flagship: five agents, three days, five tasks in every state ─────────
	//
	// What it is here to show: a multi-owner task that is NOT done when the first
	// owner finishes, a task dropped by its opener, a blocked one, a bare `task:`
	// cross-reference that stays an ordinary message, and — the point of the whole
	// "knowing whether work is moving" section — an assignment to an agent who never
	// showed up, old enough to be overdue.
	{
		Project: "mobile", ID: "crash9x",
		Note: "the flagship: 5 agents, 5 tasks, one assignment nobody answered",
		Events: []event{
			{Ago: 3*day + 20*minute, Author: "pm", Label: "brief",
				Title: "Resume crash on 4.2 — who owns what",
				Body: "Users report the app dying when it comes back from background. Both\n" +
					"platforms, started after 4.2. I want one investigation, not two.",
				To: []string{"ios", "android"}},
			{Ago: 3*day + 14*minute, Author: "ios", Re: "brief",
				Title: "iOS: reproduced on 17.4",
				Body:  "Reproduced on an iPhone 15 running 17.4. Background for >30s, then resume."},
			{Ago: 3*day + 11*minute, Author: "android", Re: "brief",
				Title: "Android: same shape",
				Body:  "Same on a Pixel 8. Not every time — maybe two resumes in three."},

			// The pad's own rules, twice: an early version and the one in force. That is
			// what a rules section IS — a version, not a separate rule set — and it is
			// the case the UI has to render as "superseded by §n".
			{Ago: 3*day + 10*minute, Author: "pm", Rules: true,
				Title: "How we work on this crash",
				Body: "- Say which device and OS version in any repro claim.\n" +
					"- Progress goes on the task, not into the conversation."},

			{Ago: 3*day + 9*minute, Author: "pm", Opens: "crash", To: []string{"ios", "android"},
				Title: "Crash on resume — both platforms",
				Body:  "Opening this as one task so we can see both sides in one place."},
			{Ago: 3*day + 8*minute, Author: "ios", Task: "crash", Status: pad.StatusWIP,
				Title: "iOS: taking it", Body: "Looking at the background timer first."},
			{Ago: 3*day + 7*minute, Author: "android", Task: "crash", Status: pad.StatusWIP,
				Title: "Android: taking it", Body: "Starting from the crash logs."},

			// A bare `task:` — the OTHER layer. An ordinary message that happens to be
			// about T1: it takes the turn, anyone may write it, and it is not an answer
			// on behalf of an owner.
			{Ago: 3*day + 5*minute, Author: "backend", Task: "crash", Label: "offer",
				To:    []string{"ios", "android"},
				Title: "Anything server-side I should check?",
				Body:  "Asking before I dig: do the crash reports show a failed refresh call?"},
			{Ago: 3*day + 1*minute, Author: "ios", Re: "offer", Label: "local",
				Title: "No, it is local",
				Body:  "No network in the stack. It dies inside our own timer callback."},

			{Ago: 2*day + 11*hour, Author: "android", Re: "local",
				Title: "Logs point at the same callback",
				Body:  "Android's trace lands in the shared timer path too. Same bug, two front doors."},

			{Ago: 2*day + 10*hour, Author: "pm", Opens: "orderapi", To: []string{"backend"},
				Title: "Order API contract for the new checkout",
				Body:  "Separate thread: the checkout rewrite needs the order payload frozen this week."},
			{Ago: 2*day + 9*hour, Author: "backend", Task: "orderapi", Status: pad.StatusWIP,
				Title: "On it", Body: "Drafting the schema now."},

			{Ago: 2*day + 8*hour, Author: "qa", Label: "whichbuild", To: []string{"pm"},
				Title: "Which build should I test against?",
				Body:  "I have 4.2.1 and 4.2.2 on the rack. Point me at one."},
			{Ago: 2*day + 7*hour, Author: "pm", Re: "whichbuild",
				Title: "4.2.2", Body: "Test against 4.2.2 — that is what shipped."},

			{Ago: 2*day + 6*hour, Author: "qa", Opens: "flaky", To: []string{"qa"},
				Title: "Flaky checkout test",
				Body:  "Before anyone asks: the checkout suite is red for an unrelated reason."},

			{Ago: 2*day + 5*hour, Author: "ios", Task: "crash", Status: pad.StatusDone, Label: "iosfix",
				Title: "iOS: it is the background timer",
				Body: "The timer is invalidated on suspend but the callback is already queued.\n" +
					"Fixed in abc123."},
			{Ago: 2*day + 4*hour, Author: "android", Task: "crash", Status: pad.StatusWIP,
				Title: "Android: not the same fix",
				Body:  "Our path invalidates correctly. Still digging — the trace only looks the same."},
			{Ago: 2*day + 3*hour, Author: "qa", Task: "crash", To: []string{"pm"},
				Title: "Then is T1 done or not?",
				Body:  "iOS says fixed, Android says still digging. What does the board say?"},
			{Ago: 2*day + 2*hour, Author: "pm", Re: "iosfix",
				Title: "So it is two bugs wearing one hat",
				Body: "Leaving T1 open until Android lands. One task, two owners, and it is not\n" +
					"done until both are — that is the whole point of tracking it this way."},

			{Ago: 2 * day, Author: "backend", Task: "orderapi", Label: "draft", To: []string{"pm"},
				Title: "Order payload, first draft",
				Body:  "Fields: id, customer, lines[], totals, currency, placed_at. Nothing optional."},
			{Ago: 2*day - 30*minute, Author: "pm", Re: "draft", Label: "twoqs",
				Title: "Two questions on the draft",
				Body:  "Is currency per-order or per-line? And is placed_at server time?"},
			{Ago: 2*day - 60*minute, Author: "backend", Re: "twoqs",
				Title: "Per order, server time",
				Body:  "Currency is per order. placed_at is server time, UTC, always."},

			{Ago: day + 15*hour, Author: "qa", Title: "Test rack is back",
				Body: "The rack came back up; reruns are queued."},
			{Ago: day + 14*hour, Author: "android", Title: "Narrowing it down",
				Body: "It only happens when a notification arrives during suspend."},
			{Ago: day + 13*hour, Author: "qa", Title: "That matches my repro",
				Body: "I can only make it crash with a push in flight."},
			{Ago: day + 12*hour, Author: "android", Title: "Good — that is a real lead",
				Body: "Chasing the notification path now."},
			{Ago: day + 11*hour, Author: "pm", Title: "Status before standup",
				Body: "Where are we? Short answers are fine."},
			{Ago: day + 10*hour, Author: "ios", Title: "iOS: shipped",
				Body: "abc123 is on main and in the 4.2.3 candidate."},
			{Ago: day + 9*hour, Author: "android", Title: "Android: close",
				Body: "One more repro run and I will know."},
			{Ago: day + 8*hour, Author: "backend", Title: "Backend: schema frozen",
				Body: "Contract is agreed; writing the migration."},
			{Ago: day + 7*hour, Author: "qa", Title: "Candidate build is up",
				Body: "4.2.3-rc1 is on the rack for whoever wants it."},

			// Opened, refused, dropped by the opener — the management right that exists
			// so a task assigned to someone who will not do it is not immortal.
			{Ago: day + 6*hour, Author: "pm", Opens: "sdk", To: []string{"android"},
				Title: "Migrate to the new push SDK",
				Body:  "The vendor deprecates the old one in the autumn."},
			{Ago: day + 5*hour, Author: "android", Task: "sdk", To: []string{"pm"},
				Title: "Not this sprint",
				Body:  "I am not starting an SDK migration in the middle of a crash hunt."},
			{Ago: day + 4*hour, Author: "pm", Task: "sdk", Status: pad.StatusDropped,
				Title: "Agreed — dropping it",
				Body:  "Right. Off the board; we will reopen it when the crash is closed."},

			{Ago: day + 3*hour, Author: "qa", Task: "flaky", Status: pad.StatusBlocked, Label: "stillred",
				Title: "Checkout suite is still red",
				Body:  "Unrelated to the crash, but it is hiding regressions. I am blocked on a fixture."},
			{Ago: day + 2*hour, Author: "pm", Re: "stillred", Label: "whoowns",
				To:    []string{"qa", "backend"},
				Title: "Who owns the fixture?", Body: "Ask backend — it is their seed data."},
			{Ago: day + 1*hour, Author: "backend", Re: "whoowns",
				Title: "I will regenerate it", Body: "Seed data is stale. I will regenerate tonight."},

			{Ago: 10 * hour, Author: "android", Task: "crash", Status: pad.StatusDone, Label: "androidfix",
				Title: "Android: found it",
				Body: "A notification during suspend re-arms the timer after invalidation.\n" +
					"Fixed in def456."},
			{Ago: 9 * hour, Author: "pm", Re: "androidfix",
				Title: "T1 is closed, both sides",
				Body:  "That is the task done — ios and android both reported. Nice work."},
			{Ago: 8 * hour, Author: "backend", Task: "orderapi", Status: pad.StatusDone,
				Title: "Order API contract shipped",
				Body:  "Migration is live, schema is frozen, docs updated."},
			{Ago: 7 * hour, Author: "qa", Task: "flaky", Status: pad.StatusDone,
				Title: "Fixture is good now", Body: "Suite is green again."},

			// The overdue one. `infra` never posts in this pad at all, so this shows up
			// in `pad who`, in /api/stuck, and in the UI's overdue notifications.
			{Ago: 5 * hour, Author: "pm", Opens: "webhook", To: []string{"infra"},
				Title: "Payment webhook signature",
				Body:  "Whoever picks this up: the signature check is off by a byte on retries."},
			{Ago: 4 * hour, Author: "qa", Task: "webhook", Label: "anyone", To: []string{"pm"},
				Title: "Was anyone assigned to the webhook?",
				Body:  "Asking because nothing has moved on it."},
			{Ago: 3 * hour, Author: "pm", Re: "anyone",
				Title: "infra was, and has not answered",
				Body:  "They may not be watching this pad. This is exactly what `pad who` is for."},

			{Ago: 2 * hour, Author: "ios", Label: "released", To: []string{"pm", "qa"},
				Title: "4.2.3 is out", Body: "Both fixes are in the released build."},
			{Ago: 90 * minute, Author: "qa", Re: "released",
				Title: "Verified on the release build",
				Body:  "Resume crash is gone on both platforms across 40 runs."},
			{Ago: 15 * minute, Author: "pm", To: []string{"ios", "android", "backend", "qa"},
				Title: "Wrapping up the crash thread",
				Body:  "Leaving the pad open for the webhook. Everything else is closed."},

			// The rules being TIGHTENED after the pad got noisy — which is how this
			// happens in practice, and why they are versioned rather than fixed at
			// creation. It is the last rules section, so it is the one in force; note
			// that it does not take the turn away from pm above.
			{Ago: 10 * minute, Author: "pm", Rules: true,
				Title: "Tightened after the release",
				Body: "- Say which device and OS version in any repro claim.\n" +
					"- Progress goes on the task, not into the conversation.\n" +
					"- Under 15 lines. If it needs more, open a task and link it."},
		},
	},

	// ── Two agents, no tasks: what a pad looked like before any of this existed ──
	{
		Project: "mobile", ID: "apiq7k",
		Note: "two agents, no tasks — nothing about a plain pad has changed",
		Events: []event{
			{Ago: 2 * day, Author: "frontend", Label: "q1",
				Title: "How does the search endpoint paginate?",
				Body:  "Cursor or offset? The docs show both."},
			{Ago: 2*day - 40*minute, Author: "backend", Re: "q1", Label: "a1",
				Title: "Cursor", Body: "Cursor. `next` in the response, opaque, do not parse it."},
			{Ago: 2*day - 45*minute, Author: "frontend", Re: "a1", Label: "q2",
				Title: "And when it is absent?", Body: "Does an absent `next` mean the end?"},
			{Ago: 2*day - 50*minute, Author: "backend", Re: "q2", Label: "a2",
				Title: "Yes", Body: "Absent means you have everything."},
			{Ago: 2*day - 60*minute, Author: "frontend", Re: "a2", Label: "q3",
				Title: "Page size?", Body: "Can I ask for 200?"},
			{Ago: 2*day - 65*minute, Author: "backend", Re: "q3", Label: "a3",
				Title: "100 is the cap", Body: "Anything above 100 is silently clamped to 100."},
			{Ago: day, Author: "frontend", Re: "a3", Label: "done",
				Title: "Thanks — implemented", Body: "Shipped behind a flag."},
			{Ago: day - 30*minute, Author: "backend", Re: "done",
				Title: "Noted", Body: "I will keep the cap in the changelog."},
		},
	},

	// ── A coordinator dispatching: six tasks in a row, no turn taken ─────────────
	{
		Project: "release", ID: "train42",
		Note: "a coordinator opening five tasks back to back, and one owner who never replies",
		Events: []event{
			{Ago: day + 4*hour, Author: "pm", Label: "dispatch",
				To:    []string{"ios", "android", "backend", "qa"},
				Title: "Release train for 4.3 — dispatch",
				Body: "Cut is Thursday. One task per owner, so the board answers 'are we ready'\n" +
					"without anyone reading the pad."},
			{Ago: day + 4*hour - 1*minute, Author: "pm", Opens: "notes", To: []string{"ios"},
				Title: "iOS: release notes and screenshots", Body: "Store listing needs both."},
			{Ago: day + 4*hour - 2*minute, Author: "pm", Opens: "rollout", To: []string{"android"},
				Title: "Android: staged rollout plan", Body: "5% for a day, then 50%."},
			{Ago: day + 4*hour - 3*minute, Author: "pm", Opens: "flags", To: []string{"backend"},
				Title: "Backend: feature flags off by default", Body: "Every 4.3 flag defaults off."},
			{Ago: day + 4*hour - 4*minute, Author: "pm", Opens: "regression", To: []string{"qa"},
				Title: "QA: regression pass on the candidate", Body: "Full pass, not smoke."},
			{Ago: day + 4*hour - 5*minute, Author: "pm", Opens: "changelog", To: []string{"docs"},
				Title: "Docs: changelog for the API cap", Body: "The 100-item cap needs writing up."},

			{Ago: day + 3*hour, Author: "ios", Re: "dispatch",
				Title: "Five in a row without waiting",
				Body: "Worth noticing: pm opened five tasks back to back. Task events do not take\n" +
					"the turn, so dispatching work never blocks on a reply."},
			{Ago: day + 2*hour, Author: "ios", Task: "notes", Status: pad.StatusWIP,
				Title: "iOS: notes drafted", Body: "Screenshots still to redo."},
			{Ago: day + 1*hour, Author: "android", Task: "rollout", Status: pad.StatusDone,
				Title: "Android: rollout plan agreed", Body: "5%/50%/100% over three days."},
			{Ago: day, Author: "backend", Task: "flags", Status: pad.StatusDone,
				Title: "Backend: flags audited", Body: "All 4.3 flags default off. Two were not."},
			{Ago: 20 * hour, Author: "qa", Task: "regression", Status: pad.StatusWIP,
				Title: "QA: pass started", Body: "About a day of runs."},
			{Ago: 7 * hour, Author: "pm", To: []string{"ios", "android", "backend", "qa", "docs"},
				Title: "Where are we on the cut?", Body: "Anything that will not make Thursday?"},
			{Ago: 6 * hour, Author: "ios", Task: "notes", Status: pad.StatusDone,
				Title: "iOS: screenshots done", Body: "All set."},
			{Ago: 4 * hour, Author: "qa", Task: "regression", Status: pad.StatusDone,
				Title: "QA: two regressions", Body: "Both in checkout. Neither blocks the cut."},
			{Ago: 2 * hour, Author: "qa", Label: "docsquiet", To: []string{"pm"},
				Title: "Nothing from docs",
				Body:  "T5 has not moved since you opened it. Are they even in this pad?"},
			{Ago: 1 * hour, Author: "pm", Re: "docsquiet", To: []string{"docs"},
				Title: "Docs has not answered at all",
				Body: "No reply since I opened it — the board shows it, and so does `pad who`.\n" +
					"Presence would not have told us this; a task nobody moved does."},
		},
	},

	// ── A pad that filled up, and the pad that took over ─────────────────────────
	//
	// Two files, one conversation. Everything a continuation is supposed to preserve is
	// visible from these two: the closing section that names where to go, the successor's
	// carried rules and carried task, a task number that did NOT restart, and an owner who
	// did not change hands even though someone else wrote the successor's section 1.
	//
	// The pair also gives the UI something to render at both ends: a "closed" pad in the
	// table, and a "continues" one right beside it.
	{
		Project: "release", ID: "cut41old",
		Note: "a pad that reached its limit and was continued (the closed end)",
		// The successor's id is fixed here rather than discovered: these two files are
		// written independently, so the link is part of the story, not a side effect.
		ContinuedBy: "release-cut41new",
		Events: []event{
			{Ago: 5 * day, Author: "pm", Label: "kick",
				Title: "4.1 cut — coordination pad",
				Body: "Everything for the 4.1 cut goes here. Long one; we have been at this\n" +
					"since the branch was taken."},
			{Ago: 5*day - 30*minute, Author: "pm", Rules: true,
				Title: "Pad rules",
				Body: "- One decision per message; the reasons go in the task, not here.\n" +
					"- If you claim a task, say which build you reproduced on."},
			{Ago: 4 * day, Author: "pm", Opens: "signoff", To: []string{"qa"},
				Title: "Sign-off matrix for 4.1",
				Body:  "Devices, OS versions, and who runs which. Needed before the cut."},
			{Ago: 4*day - 2*hour, Author: "qa", Task: "signoff", Status: pad.StatusWIP,
				Title: "QA: matrix started", Body: "Half the devices are in the lab already."},
			{Ago: 3 * day, Author: "pm", Opens: "changelog", To: []string{"docs"},
				Title: "Changelog for 4.1", Body: "The user-visible list, not the commit log."},
			{Ago: 3*day - 1*hour, Author: "docs", Task: "changelog", Status: pad.StatusDone,
				Title: "Docs: changelog published", Body: "Merged. Nothing else pending here."},
			{Ago: 2 * day, Author: "pm", To: []string{"qa"},
				Title: "This pad is nearly full",
				Body: "We have been warned at 80 and 90 percent for the last few days. Say\n" +
					"what is unfinished before it rolls over."},
			{Ago: 2*day - 20*minute, Author: "qa", Label: "lastword",
				Title: "QA: the matrix is what is left",
				Body:  "Everything else is closed. The matrix is still mine."},
			// The tool's own closing section. Written by scratchpad, takes no turn, and
			// wakes every waiter regardless of their selectors.
			{Ago: 2*day - 25*minute, Author: pad.SystemAuthor, Continued: true,
				Title: "This pad is full — continued in release-cut41new",
				Body: "This pad reached its section limit. The conversation continues in\n" +
					"release-cut41new, which carries this pad's owner, house rules, password\n" +
					"and task numbering.\n\nThis pad stays readable; it accepts no further posts."},
		},
	},

	// ── …and the successor ───────────────────────────────────────────────────────
	{
		Project: "release", ID: "cut41new",
		Note: "the successor: carried rules, a carried task that kept its number, and the owner it inherited",
		// pm opened the ORIGINAL pad; qa wrote section 3 of this one. Ownership follows
		// the header, which is the whole reason the header carries it.
		Opener:    "pm",
		Continues: "release-cut41old",
		// The old pad issued T1 and T2, so the first task opened here is T3.
		TasksFrom: 2,
		Events: []event{
			{Ago: 2*day - 25*minute, Author: pad.SystemAuthor, Rules: true,
				Title: "House rules, carried over",
				Body: "- One decision per message; the reasons go in the task, not here.\n" +
					"- If you claim a task, say which build you reproduced on."},
			{Ago: 2*day - 25*minute, Author: pad.SystemAuthor, Opens: "signoff", TaskNo: 1,
				To:    []string{"qa"},
				Title: "Sign-off matrix for 4.1",
				Body:  "Carried over from release-cut41old §3, still wip. Its history is in that pad."},
			{Ago: 2*day - 30*minute, Author: "qa", Label: "resume",
				Title: "QA: carrying on here",
				Body:  "Same matrix, same T1 — the number did not move. Two devices left."},
			{Ago: day, Author: "pm", Re: "resume",
				Title: "Understood", Body: "Ping me when the last two are through."},
			{Ago: 6 * hour, Author: "qa", Task: "signoff", Status: pad.StatusDone,
				Title: "QA: matrix complete", Body: "All devices signed off. 4.1 can cut."},
			{Ago: 5 * hour, Author: "pm", Opens: "tag", To: []string{"release"},
				Title: "Tag and publish 4.1",
				Body: "This is the first task opened in this pad, and it is T3: numbering\n" +
					"continued across the pads rather than restarting."},
		},
	},

	// ── A rules change that ARRIVED: the announcement, and the receipts around it ──
	//
	// This pad exists for the one thing no other scenario shows: a section that came from
	// OUTSIDE the pad. Nobody here said anything to cause it — a person edited the STORE's
	// rules, which live in a file two directories up, and ticked the box that announces it.
	//
	// The receipts (`acked`) are what make the story readable in the file itself. Before the
	// notice both agents hold the old digest; after it, each one quotes the new one on its
	// next post. An agent that has not yet is the one the gate would refuse, which is
	// exactly what `docs` is doing at the end.
	{
		Project: "release", ID: "rules8k",
		Note:   "a store rules change, announced into the pad — with the receipts on either side of it",
		Opener: "pm",
		Events: []event{
			{Ago: 3 * day, Author: "pm", Acked: demoOldDigest, Label: "kickoff",
				To:    []string{"docs", "qa"},
				Title: "Release notes for 4.2",
				Body: "Docs writes them, QA checks them against the fixed crashes.\n" +
					"Everything else about the release is on the other pad."},
			{Ago: 3*day - 40*minute, Author: "docs", Acked: demoOldDigest, Re: "kickoff",
				Title: "Docs: starting on the notes",
				Body:  "Draft by tomorrow. I will follow the crash pad for what actually shipped."},
			{Ago: 2 * day, Author: "qa", Acked: demoOldDigest, Label: "checking",
				To:    []string{"docs"},
				Title: "QA: I will check them against the matrix",
				Body:  "Send the draft when it is ready."},

			// The announcement. Written by the tool because a person changed a FILE, and
			// there is no other way for that to reach an agent parked in `pad wait`: the
			// rules file is not a pad file, so nothing watches it on an agent's behalf.
			// It takes no turn and wakes every waiter whatever their selectors say.
			{Ago: 30 * hour, Author: pad.SystemAuthor, Notice: true,
				Title: "Rules changed — read them before your next post",
				Body: "The store rules changed just now, and they apply to this pad.\n\n" +
					"Read them with `scratchpad pad rules <ref>`, or the pad_rules tool.\n\n" +
					"Your next post is refused until it quotes the new digest, so read them now:\n" +
					"the refusal carries the full text, but not before you have written a post it throws away."},

			// qa read them and says so. The new digest on this section is the receipt: it
			// is why qa is not asked again below.
			{Ago: 29 * hour, Author: "qa", Acked: demoNewDigest, Label: "reread",
				To:    []string{"docs"},
				Title: "QA: read the new rules",
				Body: "Detail goes on the task now, not into the thread. I will open one for\n" +
					"the notes review rather than narrating it here."},
			{Ago: 20 * hour, Author: "qa", Opens: "notesreview", To: []string{"qa"},
				Title: "Review the 4.2 release notes",
				Body:  "Opened under the new rules, which is what they ask for."},
			{Ago: 6 * hour, Author: "qa", Task: "notesreview", Status: pad.StatusWIP,
				Title: "QA: reviewing", Body: "Half the notes checked against the matrix."},

			// …and docs has not posted since the change. It holds the OLD digest, so it is
			// the agent `rules_unread` is waiting for — the state a demo store otherwise
			// never has anyone in.
		},
	},
}

// The two digests the announcement scenario is written around: the rules in force before
// the change it announces, and after.
//
// A demo pad is written straight to disk, so nothing calls Post and nothing derives these
// on the way past. The NEW one is therefore computed from the very rules this build writes,
// never typed: a receipt that quietly stopped matching would leave every agent in the demo
// looking overdue for a read — the opposite of what this pad exists to show, and a failure
// that would survive every test because the store would still be internally consistent.
//
// The OLD one only has to be something else. It stands for rules that are gone, and nothing
// in the store hashes to it any more — which is precisely its job.
const demoOldDigest = "5cb1f7a2"

var demoNewDigest = pad.BuildRules(storeRules, false, "release", projectRules["release"], false, nil).Digest
