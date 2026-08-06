// harness.js — the measuring apparatus for /harness.html. Loaded by that page and by
// nothing else.
//
// It mounts the REAL pad page (`/pages/pad.js`, the same module the router mounts) into
// this document and then only reads the DOM it produces. Nothing here is a stub, a mock
// or a copy: a harness that measures a reimplementation measures the reimplementation.
//
// It also does not reach into the page's internals — there is no hook in pad.js for this
// file, and there must not be. Everything it drives, it drives by clicking the control a
// person would click; everything it knows, it reads from the rendered DOM. That is the
// property that keeps the harness honest as the page changes: if a control stops
// existing, this stops working loudly rather than measuring something that no longer
// matches what a reader sees.

import "/vendor/puredashboard/layout.js";
import mountPad from "/pages/pad.js";
import { connect } from "/lib/bus.js";

// The live stream belongs to the app shell, which is not here — so the harness opens it,
// or an arriving section would never reach the page and the measurement that matters
// most could not be taken at all.
connect();

const view = document.getElementById("hx-view");
const outBox = document.getElementById("hx-out");
const verdictBox = document.getElementById("hx-verdict");
const actionBox = document.getElementById("hx-action");
const refInput = document.getElementById("hx-ref");
const secInput = document.getElementById("hx-sec");

let teardown = null;
let lastAction = "none yet";

// ── identity ───────────────────────────────────────────────────────────────────
//
// "Was this the same node?" cannot be answered by comparing attributes — a rebuilt row
// looks identical. So every node the harness has seen gets a number stamped on it, and
// the number is what is compared. A rebuilt row arrives without one.
let seq = 0;
const idOf = (n) => n.__hxId || (n.__hxId = ++seq);

const rows = () => [...document.querySelectorAll(".chat > .msg")];

// The page scrolls in whichever ancestor actually scrolls; find it the way the page does
// rather than assuming the window.
function scroller() {
  const t = document.querySelector(".pad__transcript");
  for (let n = t && t.parentElement; n; n = n.parentElement) {
    const oy = getComputedStyle(n).overflowY;
    if ((oy === "auto" || oy === "scroll") && n.scrollHeight > n.clientHeight) return n;
  }
  return document.scrollingElement || document.documentElement;
}

// The topmost section still on screen — the one a reader would say they are looking at,
// and the one the page's own anchoring pins.
function anchor() {
  const first = rows().find((m) => m.getBoundingClientRect().bottom > 0);
  return first ? { section: first.dataset.section, top: Math.round(first.getBoundingClientRect().top) } : null;
}

function topOf(section) {
  const m = rows().find((x) => x.dataset.section === section);
  return m ? Math.round(m.getBoundingClientRect().top) : null;
}

function snap() {
  const nodes = new Map();   // section -> row node id
  const lazy = new Map();    // section -> <puredashboard-lazy> data-state
  const md = new Map();      // section -> id of the first element the markdown produced
  const clamp = new Map();   // section -> "<data-clamped>/<toggle label>", long rows only
  for (const m of rows()) {
    const sec = m.dataset.section;
    nodes.set(sec, idOf(m));
    const lz = m.querySelector("puredashboard-lazy");
    lazy.set(sec, lz ? lz.dataset.state : "none");
    // The markdown component re-parses by replacing its children, so a new identity here
    // means the body was parsed again — which is the cost the keyed list exists to avoid.
    const first = m.querySelector(".sec__content")?.firstElementChild;
    if (first) md.set(sec, idOf(first));
    const body = m.querySelector(".sec__body");
    const toggle = m.querySelector(".sec__expand");
    if (body && toggle && !toggle.hidden) clamp.set(sec, `${body.dataset.clamped}/${toggle.textContent.trim()}`);
  }
  const sc = scroller();
  const t = document.querySelector(".pad__transcript");
  return {
    nodes, lazy, md, clamp,
    scroll: { scrollTop: Math.round(sc.scrollTop), scrollHeight: Math.round(sc.scrollHeight) },
    // Where the transcript itself sits. Nothing the transcript does moves this except
    // "load older", so it is how the harness catches its OWN chrome shifting the subject
    // and being read as the reader's place moving.
    subjectTop: t ? Math.round(t.getBoundingClientRect().top) : null,
  };
}

let base = null;
let heldSection = null;
let heldTop = null;

// FOCUS, tracked separately and on purpose.
//
// The instrument used to report "rows kept 20/20, rebuilt 0" and stop there, and that
// number was read — by me, in three reports — as "the reader's state survived". It does not
// mean that. Keeping a node means the engine did not REBUILD it; it says nothing about
// whether the node was MOVED, and a move is `insertBefore`, which the DOM defines as
// remove-plus-insert. Focus does not survive that. Which rows get moved is a property of
// the diff, not of the action: removing rows so the survivors are no longer adjacent
// relocates them exactly as a reorder does, so an ordinary filter can throw a keyboard
// reader back to the top of the document while every count here reads clean.
//
// So the harness now watches the one piece of reader state that still lives in the DOM,
// and says so even when there is nothing to watch — an absent FAIL must not read as a PASS.
let heldFocus = null;

function focusInTranscript() {
  const a = document.activeElement;
  if (!a || typeof a.closest !== "function") return null;
  const row = a.closest(".chat > .msg");
  if (!row) return null;
  return {
    section: row.dataset.section,
    what: `${a.localName}${a.className ? `.${String(a.className).trim().split(/\s+/).join(".")}` : ""}`,
  };
}

function tally(map) {
  const out = {};
  for (const v of map.values()) out[v] = (out[v] || 0) + 1;
  return out;
}

// ── reporting ──────────────────────────────────────────────────────────────────

function baseline() {
  base = snap();
  const a = anchor();
  heldSection = a ? a.section : null;
  heldTop = a ? a.top : null;
  heldFocus = focusInTranscript();
  emit({
    what: "baseline",
    rows_on_screen: base.nodes.size,
    sections: [...base.nodes.keys()],
    lazy: tally(base.lazy),
    clamp: Object.fromEntries(base.clamp),
    held: heldSection,
    held_top: heldTop,
    focused: heldFocus,
    scroll: base.scroll,
  }, heldFocus ? [] : [line("info", "nothing in the transcript is focused — click or tab to a chip first if you want the focus check to mean anything")]);
}

function report() {
  if (!base) return emit({ what: "report", error: "press Baseline first" }, [line("fail", "no baseline — nothing to compare against")]);
  const now = snap();

  let kept = 0;
  const rebuilt = [];
  for (const [sec, id] of now.nodes) {
    if (!base.nodes.has(sec)) continue;              // arrived since the baseline
    if (base.nodes.get(sec) === id) kept++; else rebuilt.push(sec);
  }
  const carried = [...now.nodes.keys()].filter((s) => base.nodes.has(s)).length;

  const lazyBack = [];
  for (const [sec, st] of now.lazy) {
    if (base.lazy.get(sec) === "rendered" && st !== "rendered") lazyBack.push(`§${sec}:${st}`);
  }

  const reparsed = [];
  for (const [sec, id] of now.md) {
    if (base.md.has(sec) && base.md.get(sec) !== id) reparsed.push(sec);
  }

  const clampChanged = {};
  for (const [sec, v] of now.clamp) {
    const was = base.clamp.get(sec);
    if (was !== undefined && was !== v) clampChanged[`§${sec}`] = `${was} -> ${v}`;
  }

  const nowTop = heldSection ? topOf(heldSection) : null;
  const shift = heldSection && heldTop != null && nowTop != null ? nowTop - heldTop : null;
  const flipped = lastAction === "flip reading order";
  // The shift is a viewport measurement, so anything that moved the whole page under it
  // makes the number say something other than what it claims. Two ways that happens:
  // the page scrolled (focusing a control up here is enough), or the harness's own
  // chrome changed height and pushed the subject down. Both are checked, because a
  // number nobody can tell is contaminated is worse than no number.
  //
  // "load older" is the one action for which both legitimately move: the page grew above
  // the reader, and the page's own anchoring adjusted scrollTop precisely to keep them
  // still. That is the thing being measured, so the guard steps aside for it.
  const nowFocus = focusInTranscript();
  const focusKept = heldFocus
    ? !!(nowFocus && nowFocus.section === heldFocus.section && nowFocus.what === heldFocus.what)
    : null;

  const scrolled = now.scroll.scrollTop - base.scroll.scrollTop;
  const subjectMoved = now.subjectTop != null && base.subjectTop != null
    ? now.subjectTop - base.subjectTop : null;
  const shiftTrustworthy = lastAction === "load older" || (scrolled === 0 && subjectMoved === 0);

  emit({
    what: "report",
    after: lastAction,
    rows_now: now.nodes.size,
    rows_carried_from_baseline: carried,
    rows_kept: kept,
    rows_rebuilt: rebuilt.length,
    rebuilt_sections: rebuilt,
    lazy: tally(now.lazy),
    lazy_rendered_to_pending: lazyBack,
    bodies_reparsed: reparsed.length,
    reparsed_sections: reparsed,
    clamp: Object.fromEntries(now.clamp),
    clamp_changed: clampChanged,
    held: heldSection,
    held_top_before: heldTop,
    held_top_now: nowTop,
    held_shift_px: shift,
    page_scrolled_px: scrolled,
    subject_moved_px: subjectMoved,
    held_shift_trustworthy: shiftTrustworthy,
    focused_before: heldFocus,
    focus_kept: focusKept,
    focus_now: nowFocus,
    scroll: now.scroll,
  }, [
    carried === 0
      ? line("info", "no section from the baseline is still on screen — identity says nothing here")
      : line(rebuilt.length === 0 ? "pass" : "fail",
             `rows kept ${kept}/${carried}, rebuilt ${rebuilt.length} — NOT rebuilt, which is not the same as state survived`),
    heldFocus === null
      ? line("info", "focus: nothing in the transcript was focused, so this run says nothing about it")
      : line(focusKept ? "pass" : "fail",
             focusKept
               ? `focus stayed on ${heldFocus.what} in §${heldFocus.section}`
               : `focus LOST from ${heldFocus.what} in §${heldFocus.section} — the row can be kept and still be MOVED, and a move is remove-plus-insert`),
    line(lazyBack.length === 0 ? "pass" : "fail",
         `lazy rendered → pending: ${lazyBack.length}${lazyBack.length ? ` (${lazyBack.join(", ")})` : ""}`),
    // A flip used to cost a full re-parse: moving a node reconnects it, and the markdown
    // component re-rendered on every connect. It now repaints only when its value actually
    // changed, so 0 is the expected answer HERE TOO — and a non-zero count is worth
    // reporting rather than shrugging at, which is what the old "expected after a flip"
    // wording taught the reader to do.
    line(reparsed.length === 0 ? "pass" : "fail",
         reparsed.length === 0
           ? `bodies re-parsed: 0${flipped ? " (a flip no longer costs a re-parse)" : ""}`
           : `bodies re-parsed: ${reparsed.length}${flipped ? " — a flip should no longer cost this; check the vendored md.js" : ""}`),
    shift === null
      ? line("info", "held section is gone — no shift to report")
      : !shiftTrustworthy
        ? line("info", `held §${heldSection} moved ${shift} px — DISREGARD: the page scrolled ${scrolled} px and the subject moved ${subjectMoved} px between baseline and report, so this is not about the transcript`)
        : line(lastAction === "load older" && shift !== 0 ? "fail" : "info",
               `held §${heldSection} moved ${shift} px${lastAction === "load older" ? " (must be 0: the reader did not ask to move)" : ""}`),
  ]);

  // The report becomes the next baseline, so a sequence of actions can be walked one at
  // a time without pressing Baseline between each.
  //
  // The held section has to be re-taken WITH it. Promoting `base` and leaving `heldTop`
  // where it was measures the shift against the original baseline while the guards that
  // decide whether that shift is trustworthy are measured against the new one — so a
  // second "load older" would report the FIRST one's shift, call it trustworthy, and
  // FAIL an action that moved nothing.
  base = now;
  const a = anchor();
  heldSection = a ? a.section : null;
  heldTop = a ? a.top : null;
  heldFocus = nowFocus;
}

function line(kind, text) { return { kind, text }; }

function emit(obj, lines) {
  verdictBox.replaceChildren(...lines.map((l) => {
    const d = document.createElement("div");
    d.className = `hx__line hx__${l.kind}`;
    d.textContent = `${l.kind === "pass" ? "PASS" : l.kind === "fail" ? "FAIL" : "·"}  ${l.text}`;
    return d;
  }));
  outBox.textContent = `${JSON.stringify(obj, null, 1)}\n\n${outBox.textContent}`;
}

// ── driving the app ────────────────────────────────────────────────────────────
//
// Every one of these clicks a control a person would click. Programmatically, because
// the transcript's toolbar is sticky and covers the top of the scroll area — a click
// aimed at "load older" by coordinate lands on the toolbar instead.

function setAction(what) {
  lastAction = what;
  actionBox.textContent = what;
}

// Buttons in the pad toolbar carry no id; they are identified by the label a person
// reads, which is also what makes a failure here obvious rather than silent.
function toolbarButton(...labels) {
  const found = [...document.querySelectorAll(".pad__toolbar button")]
    .find((b) => labels.includes(b.textContent.trim()));
  if (!found) throw new Error(`no toolbar button labelled ${labels.join(" / ")} — has the toolbar changed?`);
  return found;
}

// act() reports for you after a settle, because the interesting change is asynchronous
// (a fetch, a parse, a layout) and reporting too early measures the render before it.
async function act(what, fn, settleMs = 1500) {
  // Acting without a baseline would still CHANGE the page, and the baseline taken
  // afterwards would then record the state the action produced — a run that looks clean
  // because the evidence was destroyed before it was collected.
  if (!base) {
    emit({ what: "refused", after: what, error: "press Baseline first" },
         [line("fail", `refused to ${what}: press Baseline first, or the baseline records the result instead of the starting point`)]);
    return;
  }
  try {
    setAction(what);
    fn();
  } catch (err) {
    emit({ what: "action failed", after: what, error: String(err) }, [line("fail", String(err))]);
    return;
  }
  await new Promise((r) => setTimeout(r, settleMs));
  report();
}

function mount() {
  const ref = refInput.value.trim();
  if (!ref) {
    emit({ what: "mount", error: "no ref" }, [line("fail", "type a pad ref — `scratchpad pad list` prints them")]);
    return;
  }
  teardown?.();
  teardown = null;
  base = null;
  heldSection = null;
  verdictBox.replaceChildren();
  // The same shape the router hands a page: params from the path, query from the URL.
  teardown = mountPad(view, { params: { ref }, query: {} });
  setAction("mounted");
  const url = new URL(location.href);
  url.searchParams.set("ref", ref);
  history.replaceState(null, "", url);
}

// The harness's own BUTTONS must not take focus, or the focus measurement is impossible by
// construction: pressing "Baseline" would move the active element off the transcript and
// record `focused: null` every single time — which reads as "nothing was focused" rather
// than "the instrument just destroyed the reading". preventDefault on mousedown suppresses
// the focus without suppressing the click. Text inputs are excluded, obviously; they are
// meant to be typed into, and focusing one is a deliberate act by the person, not a side
// effect of asking for a measurement.
//
// Same rule as the fixed panel heights above: nothing this page does may change the thing
// it is measuring.
document.querySelector(".hx__controls").addEventListener("mousedown", (e) => {
  if (e.target.closest("button")) e.preventDefault();
});

document.getElementById("hx-mount").addEventListener("click", mount);
document.getElementById("hx-baseline").addEventListener("click", baseline);
document.getElementById("hx-report").addEventListener("click", () => { setAction(`${lastAction} (manual report)`); report(); });
document.getElementById("hx-clear").addEventListener("click", () => {
  outBox.textContent = "";
  verdictBox.replaceChildren();
});

document.getElementById("hx-older").addEventListener("click", () => act("load older", () => {
  const b = document.querySelector(".loadmore");
  if (!b || b.hidden) throw new Error("no 'load older' control — this pad has no more history, or it is all loaded");
  b.click();
}, 2500));

document.getElementById("hx-flip").addEventListener("click", () =>
  act("flip reading order", () => toolbarButton("Oldest first", "Newest first").click()));

document.getElementById("hx-expandall").addEventListener("click", () =>
  act("Expand all / Collapse long", () => toolbarButton("Expand all", "Collapse long").click()));

document.getElementById("hx-toggle").addEventListener("click", () => act("toggle one section", () => {
  const n = secInput.value.trim();
  if (!n) throw new Error("type a section number first");
  const row = document.querySelector(`.msg[data-section="${CSS.escape(n)}"]`);
  if (!row) throw new Error(`§${n} is not on screen — jump to it in the page below first`);
  const toggle = row.querySelector(".sec__expand");
  if (!toggle || toggle.hidden) throw new Error(`§${n} is not long enough to clamp, so it has no toggle`);
  toggle.click();
}));

// A ref in the URL mounts straight away, so a run can be linked to.
const wanted = new URL(location.href).searchParams.get("ref");
if (wanted) {
  refInput.value = wanted;
  mount();
}
