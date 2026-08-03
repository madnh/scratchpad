// <pad-tasks> — the pad's work ledger, beside its transcript.
//
// Six hundred sections is unreadable as a way of learning where a team stands. The task
// board is the answer to that: every piece of work as one row, folded from the events
// that moved it. It shares the rail with the outline because both are INDEXES of the
// same pad — one by section, one by work — and a second rail would only take the room
// the transcript needs.
//
// Completion is shown PER OWNER and never collapsed to a single verdict. A task shared
// by two agents that reads "done" because the first one finished is worse than no board
// at all: the outstanding half disappears exactly when someone is looking for it.
//
// Like <pad-outline> it is a KEYED list on the Reactive base: a status event arrives
// every time an agent posts, and rebuilding the list would throw away the rail's scroll
// position each time. It owns no state — the page hands it the tasks and the current
// selection, and it emits `pick` (and `open-only`).
//
// The fold itself lives in internal/pad and arrives ready-made from the API. Computing
// it here from the TOC would be a second implementation of "what is T3's status", and a
// board that disagrees with `pad tasks` is worse than none.

import { Reactive, html, repeat } from "/vendor/puredashboard/reactive.js";
import { agentColorIndex, safeText, cutChars, relTime, absTime } from "/lib/fmt.js";

// A title is cut to fit the rail; the full text stays in `title=`.
const TITLE_CHARS = 52;

// The order tasks are read in: what still needs attention first, and within that the
// most recently moved — which is where the team actually is.
const RANK = { blocked: 0, wip: 1, open: 2, done: 3, dropped: 4 };

const isOpen = (t) => t.status !== "done" && t.status !== "dropped";

class PadTasks extends Reactive {
  static properties = {
    tasks: {},      // the folded board from /api/pads/{ref}/tasks
    active: {},     // the task whose thread the transcript is filtered to, 0 for none
    openOnly: {},   // hide finished work
  };

  constructor() {
    super();
    this.tasks = [];
    this.active = 0;
    this.openOnly = false;
  }

  setup() {
    // Delegated once, so the handlers survive every re-render.
    this.on("click", ".task", (e, el) => {
      const n = Number(el.dataset.task);
      // Clicking the selected task again clears the filter: the rail is how you get
      // INTO a task's thread, so it has to be how you get back out.
      this.emit("pick", this.active === n ? 0 : n);
    });
    this.on("change", ".tasks__toggle input", (e) => this.emit("open-only", e.target.checked));
  }

  render() {
    const rows = (this.tasks || [])
      .filter((t) => !this.openOnly || isOpen(t))
      .sort((a, b) => (RANK[a.status] ?? 9) - (RANK[b.status] ?? 9) || (b.last_ts || 0) - (a.last_ts || 0));

    return html`
      <div class="tasks__head">
        <label class="tasks__toggle">
          <input type="checkbox" ?checked=${this.openOnly}> Open only
        </label>
      </div>
      ${rows.length
        ? html`<div class="tasks__list">
            ${repeat(rows, (t) => t.task, (t) => this.#row(t))}
          </div>`
        : html`<p class="tasks__empty">${(this.tasks || []).length
            ? "Nothing open — every task here is finished."
            : "No tasks yet. An agent opens one by posting with a task marker."}</p>`}
    `;
  }

  #row(t) {
    return html`
      <button type="button" class="task" data-task=${t.task} data-status=${t.status}
              aria-current=${String(this.active === t.task)}
              title=${safeText(`T${t.task} · ${t.status} · ${t.title || ""}`)}>
        <span class="task__head">
          <span class="task__id">T${t.task}</span>
          <span class="task__status" data-status=${t.status}>${t.status}</span>
          <span class="task__when" title=${absTime(t.last_ts)}>${relTime(t.last_ts)}</span>
        </span>
        <span class="task__title">${safeText(cutChars(t.title || "(untitled)", TITLE_CHARS))}</span>
        <span class="task__owners">
          ${repeat(t.owners || [], (o) => o.author, (o) => html`
            <span class="task__owner" data-done=${String(o.status === "done")}
                  style="--owner-c: var(--avatar-c${agentColorIndex(o.author)})"
                  title=${safeText(`${o.author}: ${o.status}`)}>${safeText(o.author)}</span>`)}
        </span>
      </button>
    `;
  }
}

PadTasks.define("pad-tasks");

export { PadTasks };
