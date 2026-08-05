// <pad-search> — finding a line inside ONE pad, beside its transcript.
//
// The store-wide page answers "which pad talked about this". This answers the question
// you have once you are already in a pad and it runs to six hundred sections: "where in
// HERE was that said". They are the same store call with a ref attached, and deliberately
// not the same screen — the answer to this one is only useful next to the transcript it
// jumps into.
//
// It shares the rail with the outline and the task board because all three are INDEXES
// of the same pad: one by section, one by work, one by what was written. A fourth column
// would take the room the transcript needs.
//
// Like its two neighbours it owns no answer of its own — the page runs the search and
// hands the hits down — and it emits `pick` for a hit and `search` for a new query. What
// it DOES own is the text in its own box, which is why the query lives here as state:
// re-rendering the rail while someone is typing must not take their caret with it.

import { Reactive, html, repeat } from "/vendor/puredashboard/reactive.js";
import { safeText, safeInline, cutChars, relTime, absTime } from "/lib/fmt.js";

// A title is cut to the rail's width; the whole of it stays in `title=`.
const TITLE_CHARS = 46;

class PadSearch extends Reactive {
  static properties = {
    hits: {},       // the hits for the query that has been RUN, from /api/search
    query: {},      // the query those hits belong to — not what is in the box
    state: {},      // "idle" | "loading" | "done" | "error"
    error: {},      // the message, when state is "error"
    active: {},     // the section the transcript is showing, so the row can be marked
    truncated: {},  // the search stopped at its limit
    stale: {},      // sections arrived after these hits were found
  };

  // `active` arrives on every scroll tick, which re-renders this component while someone
  // may be typing in the box above. That is safe, and it is worth writing down WHY,
  // because the obvious guess is wrong: it is not that the binding is careful, it is
  // that a binding does not fire at all when its value has not changed —
  // `AttrPart.setValue` compares against the last value it wrote and returns early. So
  // `.value=${this.query}` writes only when the QUERY changes, not when the component
  // re-renders.
  //
  // What does destroy an input is a change of TEMPLATE IDENTITY: `NodePart.commit` keeps
  // the existing DOM only while `v.strings` is the same array. Hence the shape here — the
  // input lives in render()'s own literal, which never varies, and only `#body()` swaps
  // between templates below it.

  constructor() {
    super();
    this.hits = [];
    this.query = "";
    this.state = "idle";
    this.error = "";
    this.active = 0;
    this.truncated = false;
    this.stale = false;
  }

  setup() {
    // Enter runs it. There is no index behind this — the store reads the pad — so a
    // search per keystroke would re-read the whole file ten times for one word.
    this.on("keydown", ".padsearch__input", (e, el) => {
      if (e.key === "Enter") this.emit("search", el.value.trim());
      // Escape clears the search rather than the box: leaving the hits on screen with
      // an empty field is a rail describing a question that is no longer being asked.
      if (e.key === "Escape") { el.value = ""; this.emit("search", ""); }
    });
    this.on("click", ".padsearch__go", () => {
      const box = this.querySelector(".padsearch__input");
      this.emit("search", (box?.value || "").trim());
    });
    this.on("click", ".padhit", (e, el) => this.emit("pick", Number(el.dataset.n)));
    this.on("click", ".padsearch__again", () => this.emit("search", this.query || ""));
  }

  render() {
    const hits = this.hits || [];
    return html`
      <div class="padsearch__head">
        <input type="search" class="padsearch__input" placeholder="Find in this pad"
               aria-label="Find in this pad" .value=${this.query || ""}>
        <button type="button" class="padsearch__go" title="Search this pad">Find</button>
      </div>
      ${this.#body(hits)}
    `;
  }

  #body(hits) {
    if (this.state === "loading") return html`<p class="padsearch__note">Reading the pad…</p>`;
    if (this.state === "error") {
      return html`<p class="padsearch__note padsearch__note--bad">${safeText(this.error || "That search could not be run")}</p>`;
    }
    if (this.state === "idle") {
      return html`<p class="padsearch__note">Type a word to find it in this pad's sections.</p>`;
    }
    if (!hits.length) {
      return html`<p class="padsearch__note">No line in this pad contains that.</p>`;
    }
    return html`
      <p class="padsearch__note">${hits.length} match${hits.length === 1 ? "" : "es"}${this.truncated ? ", stopped at the limit" : ""}</p>
      <p class="padsearch__note padsearch__note--stale" ?hidden=${!this.stale}>The pad has moved on since this ran ·
        <button type="button" class="padsearch__again">Search again</button></p>
      <div class="padsearch__list">
        ${repeat(hits, (h) => `${h.section}:${h.line}:${h.in_title ? "t" : "b"}`, (h) => this.#row(h))}
      </div>
    `;
  }

  #row(h) {
    return html`
      <button type="button" class="padhit" data-n=${h.section}
              aria-current=${String(this.active === h.section)}
              title=${safeText(`§${h.section} · ${h.author} · ${h.title || ""}`)}>
        <span class="padhit__head">
          <span class="padhit__sec">§${h.section}</span>
          <span class="padhit__where">${h.in_title ? "title" : `L${h.line}`}</span>
          <span class="padhit__when" title=${absTime(h.ts)}>${relTime(h.ts)}</span>
        </span>
        <span class="padhit__title">${safeText(cutChars(h.title || "", TITLE_CHARS))}</span>
        <span class="padhit__text">${this.#marked(h)}</span>
      </button>
    `;
  }

  // The match is painted where the SERVER found it. Re-running the pattern here would be
  // a second matcher, and it would disagree: --word is spelled with Unicode classes that
  // JavaScript's \b does not mean, and folding is on by default here and off there.
  //
  // safeInline rather than safeText, because the offsets were measured against the text
  // as written and safeText collapses whitespace — every character after a double space
  // would shift, and the highlight would land beside the word instead of on it.
  #marked(h) {
    const r = [...safeInline(h.text || "")];
    const { match_start: s, match_end: e } = h;
    if (!(Number.isInteger(s) && s >= 0 && Number.isInteger(e) && e > s && e <= r.length)) {
      return html`${r.join("")}`;
    }
    return html`${r.slice(0, s).join("")}<mark>${r.slice(s, e).join("")}</mark>${r.slice(e).join("")}`;
  }
}

PadSearch.define("pad-search");

export { PadSearch };
