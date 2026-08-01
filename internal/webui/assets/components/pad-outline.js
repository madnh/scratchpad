// <pad-outline> — the index of one pad, beside its transcript.
//
// A pad runs to hundreds of sections and the transcript only ever holds a PAGE of
// bodies, so scrolling is a bad way to find anything and an even worse way to know
// where you are. The outline is the whole pad at once: every section, in the order the
// transcript shows them.
//
// It is deliberately PLAIN — no card, no heading, no lettered avatars. What a reader
// scans here is the titles, and every box, rule and disc drawn around them is another
// thing competing for the same glance. The author survives as a coloured dot (the hue
// the transcript's avatar already uses), the current section as colour alone, and a
// section whose body is not loaded simply recedes.
//
// It is built on the vendored Reactive base rather than by hand because it is a KEYED
// LIST that changes constantly: a section arrives every time an agent posts, the order
// flips when the reader changes ends, and the highlight moves on every scroll. With
// repeat() keyed by section number, an arriving section inserts ONE row, flipping the
// order MOVES the existing rows, and the highlight touches a single attribute — the
// rail keeps its own scroll position through all of it, which is the whole point of
// having it.
//
// What it deliberately does NOT own: which section is active (the transcript decides,
// and pushes it in) and what happens on a click (it emits `pick`; loading a page of
// history is the page's job). It only needs the TOC — no section body ever reaches it.

import { Reactive, html, repeat, renderResult } from "/vendor/puredashboard/reactive.js";
import { agentInitials, agentColorIndex, safeText, cutChars, bytes, clockTime, absTime } from "/lib/fmt.js";

// How long a pointer has to rest on a row before its excerpt is fetched. Long enough
// that dragging the pointer down the rail costs nothing, short enough to feel like a
// hover rather than a wait.
const HOVER_DELAY = 350;

// A title is cut to fit the rail. The full text is in the popup and in `title=`.
const TITLE_CHARS = 64;

// How long after the reader touches the rail themselves the highlight stops dragging
// it around: they are looking for something, and moving the list under them loses it.
const USER_SCROLL_GRACE = 4000;

class PadOutline extends Reactive {
  static properties = {
    sections: {},   // the pad's TOC: [{n, author, title, ts, bytes}] — never bodies
    order: {},      // "newest" | "oldest" — mirrors the transcript
    active: {},     // section number currently in view, pushed in by the page
    filter: {},     // author filter, "" for all — mirrors the toolbar
    range: {},      // {from, to}: the sections whose bodies are on screen
  };

  constructor() {
    super();
    this.sections = [];
    this.order = "newest";
    this.active = 0;
    this.filter = "";
    this.range = null;

    // Supplied by the page: (n, {signal}) => Promise<{title, author, bytes, preview}>.
    // Injected rather than imported so this component knows nothing about the API —
    // and so a test can hand it a stub.
    this.loadPreview = null;

    this._cache = new Map();     // n → the excerpt, for as long as this pad is open
    this._hoverTimer = 0;
    this._abort = null;
    this._shownFor = 0;          // which row the popup currently belongs to
    this._pop = null;
    this._userScrolledAt = 0;
  }

  setup() {
    // Delegated, so the handlers survive every re-render — and so a pad with 300
    // sections wires four listeners rather than twelve hundred.
    this.on("pointerover", ".outline__item", (e, el) => this.#hover(el, HOVER_DELAY));
    this.on("pointerout", ".outline__item", () => this.#leave());
    // Keyboard users reach a row by focusing it, and get the excerpt without waiting.
    this.on("focusin", ".outline__item", (e, el) => this.#hover(el, 0));
    this.on("focusout", ".outline__item", () => this.#leave());
    this.on("click", ".outline__item", (e, el) => this.emit("pick", Number(el.dataset.n)));
    this.on("keydown", (e) => { if (e.key === "Escape") this.#leave(); });
  }

  disconnectedCallback() {
    this.#leave();
    this._pop?.remove();
    this._pop = null;
  }

  // rows applies the same two choices the transcript applies, so the two always agree
  // about what is shown and in which direction.
  rows() {
    const list = this.filter ? this.sections.filter((s) => s.author === this.filter) : this.sections;
    return this.order === "newest" ? [...list].reverse() : list;
  }

  render() {
    const rows = this.rows();

    // No heading and no count: the toolbar beside this already says "showing #23–#42
    // of 45", and a rail that announces itself twice over a list of twenty titles is
    // mostly announcement. What is left is the one control the list cannot do without.
    return html`
      <div class="outline__head">
        <button type="button" class="outline__close" title="Hide the outline"
                aria-label="Hide the outline" @click=${() => this.emit("close")}>«</button>
      </div>
      <div class="outline__list js-outline-list" @scroll=${() => { this._userScrolledAt = Date.now(); }}>
        ${repeat(rows, (s) => s.n, (s) => this.#row(s))}
        ${rows.length ? null : html`<p class="outline__empty">No sections match this filter.</p>`}
      </div>`;
  }

  #row(s) {
    const onScreen = !!this.range && s.n >= this.range.from && s.n <= this.range.to;
    const title = safeText(s.title) || "(untitled)";
    // Deliberately no `title=`: the browser's own tooltip would appear on the same
    // hover as the popup below, a second later and in a different place, saying less.
    return html`
      <button type="button" class="outline__item" data-n=${s.n}
              data-loaded=${String(onScreen)}
              aria-current=${s.n === this.active ? "true" : null}>
        <span class="outline__dot" aria-hidden="true"
              style="--avatar-bg: var(--avatar-c${agentColorIndex(s.author)})"></span>
        <span class="outline__n">#${s.n}</span>
        <span class="outline__title">${cutChars(title, TITLE_CHARS)}</span>
      </button>`;
  }

  updated(changed) {
    if (changed.has("active")) this.#revealActive();
  }

  // #revealActive keeps the highlighted row in the rail as the reader scrolls the
  // transcript — but only scrolls the LIST, never an ancestor (scrollIntoView would
  // happily scroll the page too, yanking the transcript the highlight is following),
  // and never right after the reader scrolled the rail themselves.
  #revealActive() {
    if (Date.now() - this._userScrolledAt < USER_SCROLL_GRACE) return;
    const list = this.querySelector(".js-outline-list");
    const item = this.querySelector('.outline__item[aria-current="true"]');
    if (!list || !item) return;
    const r = item.getBoundingClientRect();
    const lr = list.getBoundingClientRect();
    const pad = 8;
    if (r.top < lr.top + pad) list.scrollTop -= lr.top + pad - r.top;
    else if (r.bottom > lr.bottom - pad) list.scrollTop += r.bottom - lr.bottom + pad;
  }

  // ── the hover popup ────────────────────────────────────────────────────────
  //
  // A title is what the agent CALLED the section; the excerpt is what the section
  // actually opens with, which is often the thing you are looking for. It is fetched
  // on hover rather than shipped with the TOC because it is wanted for the two or
  // three rows a person points at, not for all three hundred.

  #hover(el, delay) {
    clearTimeout(this._hoverTimer);
    const n = Number(el.dataset.n);
    if (!n) return;
    this._hoverTimer = setTimeout(() => this.#show(el, n), delay);
  }

  #leave() {
    clearTimeout(this._hoverTimer);
    this._hoverTimer = 0;
    this._abort?.abort();
    this._abort = null;
    this._shownFor = 0;
    if (this._pop) this._pop.hidden = true;
    this.querySelector(".outline__item[aria-describedby]")?.removeAttribute("aria-describedby");
  }

  #popup() {
    if (this._pop?.isConnected) return this._pop;
    const p = document.createElement("div");
    p.className = "outline-pop";
    p.id = "outline-pop";
    p.setAttribute("role", "tooltip");
    p.hidden = true;
    // On document.body, not inside this element: Reactive's render() replaces this
    // element's children wholesale, and the popup must outlive that. Being fixed on
    // the body also keeps it clear of the rail's own scrolling.
    document.body.append(p);
    this._pop = p;
    return p;
  }

  async #show(el, n) {
    const sec = this.sections.find((s) => s.n === n);
    if (!sec) return;
    this._shownFor = n;
    el.setAttribute("aria-describedby", "outline-pop");

    const cached = this._cache.get(n);
    this.#paint(sec, cached ?? null, el);
    if (cached !== undefined || !this.loadPreview) return;

    this._abort?.abort();
    const ac = new AbortController();
    this._abort = ac;
    try {
      const res = await this.loadPreview(n, { signal: ac.signal });
      this._cache.set(n, safeText(res?.preview ?? "", { multiline: true }));
    } catch {
      // A failed excerpt is not worth an error state: the popup keeps the title and
      // the metadata, which is still more than the row had.
      this._cache.set(n, "");
    }
    if (this._shownFor === n && !ac.signal.aborted) this.#paint(sec, this._cache.get(n), el);
  }

  #paint(sec, preview, el) {
    const pop = this.#popup();
    renderResult(html`
      <div class="outline-pop__head">
        <span class="outline-pop__avatar" aria-hidden="true"
              style="--avatar-bg: var(--avatar-c${agentColorIndex(sec.author)})">${agentInitials(sec.author)}</span>
        <span class="outline-pop__author">${safeText(sec.author)}</span>
        <span class="outline-pop__n">#${sec.n}</span>
        <span class="outline-pop__time" title=${absTime(sec.ts)}>${clockTime(sec.ts)}</span>
        <span class="outline-pop__bytes">${bytes(sec.bytes)}</span>
      </div>
      <div class="outline-pop__title">${safeText(sec.title) || "(untitled)"}</div>
      ${preview === null
        ? html`<p class="outline-pop__loading">Loading the opening…</p>`
        : preview
          ? html`<p class="outline-pop__body">${preview}</p>`
          : null}`, pop);
    pop.hidden = false;
    this.#place(pop, el);
  }

  // #place puts the popup beside the row, flipping to the other side when there is no
  // room and clamping to the viewport so it is never half off-screen.
  #place(pop, el) {
    const r = el.getBoundingClientRect();
    const gap = 8;
    // Measure only after it is visible, and with the previous position cleared — a
    // stale `left` can shrink the box against the viewport edge and skew the width.
    pop.style.left = "0px";
    pop.style.top = "0px";
    const pr = pop.getBoundingClientRect();

    let left = r.right + gap;
    if (left + pr.width > window.innerWidth - gap) left = r.left - pr.width - gap;
    left = Math.max(gap, Math.min(left, window.innerWidth - pr.width - gap));

    let top = r.top;
    if (top + pr.height > window.innerHeight - gap) top = window.innerHeight - pr.height - gap;
    top = Math.max(gap, top);

    pop.style.left = `${Math.round(left)}px`;
    pop.style.top = `${Math.round(top)}px`;
  }
}

PadOutline.define("pad-outline");

export { PadOutline };
