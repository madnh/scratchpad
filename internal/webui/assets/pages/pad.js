// Pad detail — reading one conversation.
//
// Two constraints shape this page. A pad is a TURN-TAKING transcript, so it reads as
// a chat rather than a document. And a pad grows without bound — hundreds of
// sections, each of them potentially thousands of words of agent prose — so nothing
// here ever loads a whole pad:
//
//   * the table of contents arrives without any bodies, and drives the counters,
//     the author filter and "jump to #N";
//   * bodies arrive ONE PAGE AT A TIME, newest first, and older pages are fetched
//     only when asked for;
//   * a long section renders CLAMPED, because a screen of unbroken prose hides the
//     structure of the conversation.
//
// Newest-first is the default because opening a pad to see what just happened is the
// common visit, and because it means "load older" appends DOWNWARD, away from the
// reader. A person reading a pad as a conversation wants the other direction, so the
// order is switchable — at the cost of anchoring the scroll position by hand when an
// older page lands above the viewport (captureScroll below).

import "/vendor/puredashboard/lazy.js";
import "/vendor/puredashboard/md.js";
import "/vendor/puredashboard/switch.js";
import "/vendor/puredashboard/tag.js";
import "/vendor/puredashboard/input.js";
import "/vendor/puredashboard/select.js";
import "/vendor/puredashboard/result.js";
import { toast } from "/vendor/puredashboard/toast.js";
import { confirm } from "/vendor/puredashboard/dialog.js";
import { menu } from "/vendor/puredashboard/menu.js";

import "/components/pad-outline.js";
import "/components/pad-tasks.js";
import { rulesChip, showRules } from "/components/rules-dialog.js";

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import * as wl from "/lib/watchlist.js";
import * as prefs from "/lib/prefs.js";
import { el, pageHead, skeleton, errorView, copyButton, copyIconButton, setChildren } from "/lib/ui.js";
import { relTime, absTime, clockTime, bytes, agentInitials, agentColorIndex, safeText, cutChars } from "/lib/fmt.js";

// Menu icons. Inline SVG, following the library's own rule that a component carries
// its own icons rather than pulling in an icon set — three glyphs do not justify a
// dependency, and these are ours, not author input.
const icon = (body) =>
  `<svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor" ` +
  `stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
const ICON_COPY = icon('<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16V4a2 2 0 0 1 2-2h10"/>');
const ICON_BELL = icon('<path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/>');
const ICON_TRASH = icon('<path d="M3 6h18"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/>');

// CLAMP_BYTES is where a section stops being rendered in full. Roughly a screenful of
// prose: below it the whole point is visible, above it the fold plus an explicit
// expand keeps the conversation's shape readable.
const CLAMP_BYTES = 1200;

const PAGE = 20;

export default function mount(outlet, ctx) {
  const ref = ctx.params.ref;
  outlet.replaceChildren(skeleton(6));

  let disposed = false;
  let pad = null;          // the compact view: header, turn, TOC
  let loaded = [];         // section bodies currently on screen, ascending by n
  let hasOlder = false;
  let showingLatest = true; // false once the person jumps back into the history
  let pendingNew = 0;       // sections that arrived while reading further back
  let authorFilter = "";
  // The task whose thread the transcript is narrowed to, 0 for the whole pad. It is the
  // person's version of what pad_tasks gives an agent: read the eight sections about
  // one piece of work instead of the six hundred around them.
  let taskFilter = 0;
  let tasks = [];
  let rail = prefs.rail();   // "outline" | "tasks"
  let tasksOpenOnly = false;
  let expandAll = false;
  // Reading direction. Newest-first is the default — opening a pad to see what just
  // happened is the common visit — but a pad read from the start is a conversation,
  // and some people want it that way round. The choice is per person, not per pad, so
  // it is remembered across pads and sessions.
  let order = prefs.order();
  let outlineOpen = prefs.outline();
  // Sections that arrived while this page was open, so they can be flashed. The
  // marker is applied while BUILDING each node rather than by querying the DOM
  // after render(), so it survives a re-render from any source.
  const justArrived = new Set();

  // The page is built ONCE and then updated in place. Only the transcript is rebuilt
  // on a re-render; the frame around it — the header, the toolbar and the outline —
  // are long-lived nodes. That is not an optimisation: rebuilding them was throwing
  // away the outline's scroll position, the half-typed number in "Jump to #" and the
  // focus of whoever was typing it, every time an agent posted a section.
  const body = el("div", { class: "pad__transcript" });
  const outline = el("pad-outline");
  // The board shares the rail with the outline rather than claiming one of its own:
  // both are indexes of the same pad, and a second rail would take the room the
  // transcript needs. A two-button strip switches between them.
  const taskPanel = el("pad-tasks");
  const railTabs = el("div", { class: "rail__tabs", role: "tablist" });
  const railTab = (id, label) => {
    const b = el("button", {
      type: "button", class: "rail__tab", role: "tab", text: label,
      onclick: () => setRail(id),
    });
    b.dataset.rail = id;
    return b;
  };
  // Closing belongs to the rail, not to the panel inside it: what disappears is the
  // whole column — tabs, outline and board — so the control sits in the strip that
  // frames them and works whichever tab is showing.
  const railClose = el("button", {
    type: "button", class: "rail__collapse", text: "«",
    title: "Hide the rail", "aria-label": "Hide the rail",
    onclick: () => setOutline(false),
  });
  railTabs.append(railTab("outline", "Outline"), railTab("tasks", "Tasks"), railClose);
  const railBox = el("div", { class: "pad__rail" }, railTabs, outline, taskPanel);
  // The way back. A rail that closes with a « and leaves nothing behind is a rail you
  // have to go looking for — the toolbar's toggle is across the page and reads like
  // its neighbours. This puts the opener exactly where the closer was.
  const outlineReopen = el("button", {
    type: "button", class: "outline-reopen", text: "»",
    title: "Show the outline", "aria-label": "Show the outline",
    onclick: () => setOutline(true),
  });
  const layout = el("div", { class: "pad__layout" }, railBox, outlineReopen, body);
  // The toolbar's live parts, filled in by mountFrame().
  let frame = null;
  let watchSwitch = null;
  // The rules chip is built once with the rest of the meta row and repainted in place,
  // like everything else in the frame: rebuilding it would lose nothing visible but would
  // break the rule that only the transcript is ever rebuilt.
  let rulesEntry = null;
  let peopleStrip = null;   // the participants strip, which is also the pad's roster
  let peopleKey = "";       // what that strip was last painted from
  let authorOptions = "";   // the author list the filter was last built from

  outline.loadPreview = (n, opts) => api.sectionPreview(ref, n, opts);
  outline.addEventListener("pick", (e) => pickSection(e.detail));

  // ── data ───────────────────────────────────────────────────────────────────

  async function loadPad() {
    try {
      pad = await api.pad(ref);
    } catch (err) {
      if (!disposed) outlet.replaceChildren(errorView(err, loadPad));
      return;
    }
    if (disposed) return;
    if (pad.locked) {
      renderLocked();
      return;
    }
    // Only once the pad is known to be readable. A tab title outlives the tab — it
    // goes into history and session restore — so a protected pad the person never
    // unlocked must not leave its title there.
    setDocTitle();
    loadTasks();
    await loadLatest();
  }

  // The router can only title this page with the ref, because the ref is all the URL
  // carries — the title arrives with the pad. A tab strip of "default-b5i2cj" tells a
  // person nothing, so name the tab after the pad as soon as we know its name, cut
  // short: a browser tab shows a few words and the ref stays in the page itself.
  //
  // The title is written by an agent, so it is sanitised first: control characters and
  // bidi overrides are stripped (a title containing U+202E can reverse the text the
  // browser renders in the tab and the history entry, which is how a decoy is built),
  // and the cut is made on CHARACTERS so it cannot split a surrogate pair and leave a
  // lone half behind.
  function setDocTitle() {
    const t = padTitleForTab(pad.title);
    if (!t) return;
    // The deployment's own name, which the shell already put in the brand mark — an
    // operator who renamed this instance sees that name in the tab too.
    const app = document.getElementById("brand-name")?.textContent.trim() || "Scratchpad";
    document.title = `${t} · ${app}`;
  }

  const TAB_TITLE_CHARS = 48;

  // safeText strips the controls, bidi overrides and zero-width characters; cutChars
  // cuts by code point, so an emoji is never left as half a surrogate pair. Both live
  // in fmt.js because the outline shows the same agent-written titles.
  function padTitleForTab(raw) {
    return cutChars(safeText(raw), TAB_TITLE_CHARS);
  }

  async function loadLatest() {
    try {
      const page = await api.sections(ref, { limit: PAGE });
      if (disposed) return;
      loaded = page.sections;
      hasOlder = page.has_older;
      showingLatest = true;
      pendingNew = 0;
      wl.markSeen(ref, pad.section_count);
      render();
      scrollToNewest();
    } catch (err) {
      if (!disposed) outlet.replaceChildren(errorView(err, loadPad));
    }
  }

  async function loadOlder() {
    if (!loaded.length) return;
    const page = await api.sections(ref, { before: loaded[0].n, limit: PAGE });
    if (disposed) return;
    loaded = [...page.sections, ...loaded];
    hasOlder = page.has_older;
    // Reading oldest-first, the older page lands ABOVE what is on screen: without
    // this the reader's place would jump down by however tall the new page turned out
    // to be. Reading newest-first it lands below, and nothing moves.
    const keep = order === "oldest" ? captureScroll() : null;
    render();
    keep?.restore();
  }

  // ── scroll position ────────────────────────────────────────────────────────
  //
  // The page scrolls in the layout's own container, not the window, so the anchoring
  // helpers have to find it. It is the nearest ancestor that actually scrolls.
  function scroller() {
    for (let n = body.parentElement; n; n = n.parentElement) {
      const oy = getComputedStyle(n).overflowY;
      if ((oy === "auto" || oy === "scroll") && n.scrollHeight > n.clientHeight) return n;
    }
    return document.scrollingElement || document.documentElement;
  }

  // captureScroll pins the reader to a SECTION rather than to a scroll offset: the
  // sections arriving above it start at an estimated height and grow to their real one
  // as they are parsed, so an offset computed once would be wrong a moment later.
  // Re-pinning on every height change holds the section still through all of it.
  function captureScroll() {
    const first = [...body.querySelectorAll(".msg")].find((m) => m.getBoundingClientRect().bottom > 0);
    const sec = first?.dataset.section;
    const top = first ? first.getBoundingClientRect().top : 0;
    return { restore: () => { if (sec) holdSection(sec, top); } };
  }

  // holdSection keeps one section at a fixed distance from the top of the viewport
  // while the content above it settles — then gets out of the way. It stops the moment
  // the reader scrolls themselves: their intent outranks the anchor.
  // The anchor is short-lived, but the page can be left while one is still running —
  // so it is registered here and torn down with everything else. Left alone it would
  // keep an observer on a transcript nobody is reading, and hold that whole subtree
  // alive, until its timer happened to fire.
  let holdStop = null;

  function holdSection(sec, top, ms = 2500) {
    holdStop?.();               // only ever one anchor at a time
    const sc = scroller();
    let expected = -1;
    const fix = () => {
      if (expected >= 0 && Math.abs(sc.scrollTop - expected) > 2) return stop(); // they scrolled
      const el = body.querySelector(`[data-section="${sec}"]`);
      if (!el) return;
      const delta = el.getBoundingClientRect().top - top;
      if (Math.abs(delta) > 1) sc.scrollTop += delta;
      expected = sc.scrollTop;
    };
    let ro = null;
    let timer = 0;
    const stop = () => {
      ro?.disconnect();
      ro = null;
      if (timer) { clearTimeout(timer); timer = 0; }
      holdStop = null;
    };
    holdStop = stop;
    fix();
    if (typeof ResizeObserver === "function") {
      ro = new ResizeObserver(fix);
      ro.observe(body);
      timer = setTimeout(stop, ms);
    }
  }

  // scrollToNewest puts the newest section in view — which END that is depends on the
  // reading order: the BOTTOM of the transcript oldest-first (the same place a chat app
  // opens at), the TOP newest-first. Flipping the order and pressing "Latest" both mean
  // "show me the latest turn", and before this they only did so in one direction.
  function scrollToNewest() {
    const sc = scroller();
    sc.scrollTop = order === "oldest" ? sc.scrollHeight : 0;
    // The reading line has moved, and assigning scrollTop is not guaranteed to have
    // told the listener yet.
    updateActive();
  }

  // atBottom allows a little slack: a reader who stopped a line short of the end is
  // still "at the end" as far as following new turns goes.
  function atBottom(slack = 120) {
    const sc = scroller();
    return sc.scrollHeight - sc.scrollTop - sc.clientHeight <= slack;
  }

  // jumpTo centres the history ON one section: the page ends a few sections PAST it,
  // so what led up to it and what came after both arrive. Ending the page at the
  // target instead would land a jump to #1 on a screen holding exactly one section,
  // with no way forward except going back to the latest — and with the outline beside
  // the transcript, jumping is no longer the rare case it was.
  async function jumpTo(n) {
    const after = Math.floor(PAGE / 3);
    const before = Math.min(n + 1 + after, pad.section_count + 1);
    const page = await api.sections(ref, { before, limit: PAGE });
    if (disposed) return;
    loaded = page.sections;
    hasOlder = page.has_older;
    showingLatest = page.sections.at(-1)?.n === pad.section_count;
    outline.active = n;
    render();
    const target = body.querySelector(`[data-section="${n}"]`);
    // Instant, not smooth: the jump can span twenty screens of history, and a smooth
    // run through them is both useless to watch and unreliable — the animation
    // competes with the bodies being parsed along the way and can end short of the
    // target. Landing directly is what "jump" means anyway.
    //
    // To the TOP of the reading area, not the middle: the outline highlights whatever
    // is up there, so landing a jump mid-screen lit up a different section than the
    // one that was asked for. `scroll-margin-top` on .msg keeps it clear of the
    // sticky toolbar.
    if (target) target.scrollIntoView({ block: "start" });
  }

  // ── render ─────────────────────────────────────────────────────────────────

  function renderLocked() {
    const input = el("puredashboard-input", { type: "password", placeholder: "Pad password" });
    const submit = el("button", {
      type: "button", text: "Unlock",
      onclick: async () => {
        try {
          await api.unlock(ref, input.value);
          toast("Unlocked for this session", { type: "success" });
          await loadPad();
        } catch (err) {
          toast(err.message, { type: "error" });
        }
      },
    });
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") submit.click(); });

    const res = el("puredashboard-result", {
      status: "warning",
      title: "This pad is protected",
      subtitle: "Enter the password the pad was created with. It is kept for this browser session only.",
    });
    res.append(el("div", {}, input, submit));

    outlet.replaceChildren(
      pageHead(pad.title || ref, ref, copyButton(ref)),
      res,
    );
  }

  // render() is the whole-page update. The frame is mounted once; after that this
  // brings the parts that can change into line with the state — and only the
  // transcript is actually rebuilt.
  function render() {
    if (!frame) mountFrame();
    syncToolbar();
    syncOutline();
    renderTranscript();
    // Only now are the bodies in the document and measurable against the viewport.
    observeLazy();
    observeStuck();
    // The bodies just changed height, so where the reading line falls has changed too.
    updateActive();
  }

  // How many sections the pad has about one task — counted over the TOC, so it is the
  // whole thread and not merely the part currently paged in.
  function countInTask(n) {
    return pad.sections.filter((s) => s.task === n).length;
  }

  function renderTranscript() {
    // Two independent narrowings: by who is speaking, and by which piece of work is
    // being spoken about. The task one is matched against the TOC, because a section
    // belongs to a task whether or not its body happens to be loaded.
    const inTask = new Set(taskFilter ? pad.sections.filter((s) => s.task === taskFilter).map((s) => s.n) : []);
    let visible = loaded;
    if (authorFilter) visible = visible.filter((s) => s.author === authorFilter);
    if (taskFilter) visible = visible.filter((s) => inTask.has(s.n));
    const newestFirst = order === "newest";

    body.replaceChildren();

    if (pendingNew > 0) {
      body.append(el("button", {
        class: "newpill", type: "button",
        text: `${pendingNew} new section${pendingNew === 1 ? "" : "s"} ${newestFirst ? "↑" : "↓"}`,
        onclick: () => loadLatest(),
      }));
    }

    // Where the history continues, and where the pad begins, swap ends with the order:
    // "older" is always AWAY from the newest section, so the control sits below the
    // transcript reading newest-first and above it reading oldest-first.
    const edge = el("div");
    if (hasOlder) {
      edge.append(el("button", {
        class: "loadmore", type: "button", text: "Load 20 older sections",
        onclick: (e) => { e.currentTarget.disabled = true; loadOlder(); },
      }));
    } else if (loaded.length) {
      edge.append(el("p", { class: "muted", text: "Beginning of the pad." }));
    }
    if (!newestFirst) body.append(edge);

    // The API hands back ascending sections; reverse them for a newest-first read.
    const ordered = newestFirst ? [...visible].reverse() : visible;
    const chat = el("div", { class: "chat", dataset: { order } },
      ordered.map((sec) => sectionNode(sec)));
    body.append(chat);

    if (!visible.length) {
      body.append(el("p", { class: "muted", text: "No sections match this filter." }));
    }
    if (newestFirst) body.append(edge);
  }

  // mountFrame builds everything that is NOT the transcript, exactly once.
  function mountFrame() {
    const sentinel = el("div", { class: "pad__sticky-sentinel" });
    frame = { sentinel, ...buildToolbar() };
    // Rules belong with the pad's ACTIONS, beside Copy ref: they are something you open
    // and change, not a property of the pad like its project or its age. Down in the meta
    // row a person reads past them.
    rulesEntry = rulesChip({ kind: "pad", ref, project: pad.project }, () => pad.rules, {
      onSection: (n) => pickSection(n),
      onChange: (rules) => { pad.rules = rules; rulesEntry.repaint(); },
    });
    setChildren(outlet,
      breadcrumb(),
      pageHead(pad.title || ref, null, watchToggle(), rulesEntry, padMenuButton()),
      metaRow(),
      peopleRow(),
      sentinel,
      frame.toolbar,
      layout,
    );
  }

  // ── the outline ────────────────────────────────────────────────────────────
  //
  // The outline is handed the TOC and the two choices the transcript is showing, and
  // works out the rest itself. It is a property assignment rather than a render: the
  // component diffs its own keyed rows, so a section arriving inserts one row and
  // leaves the rail's scroll position — and the reader's place in it — alone.
  function syncOutline() {
    outline.sections = pad.sections;
    outline.order = order;
    outline.filter = authorFilter;
    outline.range = loaded.length ? { from: loaded[0].n, to: loaded.at(-1).n } : null;
    taskPanel.tasks = tasks;
    taskPanel.active = taskFilter;
    taskPanel.openOnly = tasksOpenOnly;
    applyRail();
    applyOutline();
  }

  // Which half of the rail is showing. It is a property of the reader, not of the pad,
  // so it survives navigating to another pad.
  function applyRail() {
    railBox.dataset.rail = rail;
    for (const b of railTabs.children) b.setAttribute("aria-selected", String(b.dataset.rail === rail));
  }

  // Switching indexes drops the task filter with it. The rail's two halves are two ways
  // of indexing the same pad, and the task filter belongs to one of them: leaving the
  // board with the transcript still narrowed to T3 left the outline listing every
  // section beside a transcript showing five, with nothing on screen to say why — and
  // clicking an outline row then appeared to do nothing at all.
  function setRail(which) {
    rail = which;
    prefs.setRail(which);
    if (which !== "tasks" && taskFilter) void selectTask(0);
    applyRail();
  }

  // Selecting a task FETCHES its thread rather than filtering the page that happens to
  // be loaded. The point of the filter is to read the eight sections about one piece of
  // work instead of the six hundred around them — and a task opened last week has none
  // of its sections in the newest page, so filtering locally showed an empty transcript
  // and a "Load older" button, which is the opposite of the promise.
  //
  // The thread comes back whole and with bodies, so it also ends paging while it is on:
  // there is no older page of a thread to fetch.
  async function selectTask(n) {
    taskFilter = n;
    taskPanel.active = n;
    if (!n) {
      await loadLatest();
      return;
    }
    try {
      const res = await api.tasks(ref, { task: n });
      if (disposed || taskFilter !== n) return; // the person moved on while it loaded
      loaded = res?.thread || [];
      hasOlder = false;
      showingLatest = false; // a thread is not the tail of the pad
      render();
    } catch (err) {
      taskFilter = 0;
      taskPanel.active = 0;
      toast(`Could not load T${n}: ${err.message}`, { type: "error" });
      render();
    }
  }

  taskPanel.addEventListener("pick", (e) => { void selectTask(Number(e.detail) || 0); });
  taskPanel.addEventListener("open-only", (e) => {
    tasksOpenOnly = !!e.detail;
    taskPanel.openOnly = tasksOpenOnly;
  });

  // The board is fetched beside the pad view rather than inside it, and refreshed on
  // every change event: a status event arrives on any agent post, and a board that
  // lags the transcript beside it is worse than no board.
  async function loadTasks() {
    try {
      const res = await api.tasks(ref);
      if (disposed) return;
      tasks = res?.tasks || [];
      taskPanel.tasks = tasks;
    } catch {
      // A protected pad that is still locked, or a transient failure: the outline and
      // the transcript are unaffected, so this stays silent rather than taking the
      // page down with it.
    }
  }

  // Below the breakpoint there is no room for a rail beside the transcript, so the
  // outline becomes something you open OVER the page. That is a fact about the window,
  // not a decision by the reader: it is tracked separately and never written to the
  // preference, or one narrow window would erase the choice for every screen.
  const narrow = window.matchMedia("(max-width: 1100px)");
  let overlayOpen = false;

  const outlineShown = () => (narrow.matches ? overlayOpen : outlineOpen);

  function applyOutline() {
    layout.dataset.outline = String(outlineShown());
    layout.dataset.narrow = String(narrow.matches);
  }

  function setOutline(on) {
    if (narrow.matches) overlayOpen = !!on;
    else {
      outlineOpen = !!on;
      prefs.setOutline(outlineOpen);   // Settings shows the same choice
    }
    applyOutline();
  }

  // As an overlay the rail covers the page, so it dismisses like one: a click anywhere
  // outside it, or Escape, puts it away. As a rail neither does anything — it is part
  // of the page, and closing it because someone clicked the transcript would be absurd.
  function dismissOverlay(e) {
    if (!narrow.matches || !overlayOpen) return;
    if (e.type === "keydown") {
      if (e.key === "Escape") setOutline(false);
      return;
    }
    // The whole rail, not just the outline: the tab strip and its close button are
    // part of the overlay, and dismissing on a click there would fight the control
    // the click was aimed at.
    if (railBox.contains(e.target) || outlineReopen.contains(e.target)) return;
    setOutline(false);
  }
  document.addEventListener("pointerdown", dismissOverlay, true);
  document.addEventListener("keydown", dismissOverlay);

  // Crossing the breakpoint closes an overlay that would otherwise reopen itself as a
  // rail — and vice versa.
  const onNarrowChange = () => { overlayOpen = false; applyOutline(); };
  narrow.addEventListener("change", onNarrowChange);

  // pickSection is what a click in the outline means. A section whose body is already
  // on screen is one scroll away; anything else needs its page of history fetched
  // first, which is exactly what "jump to #N" already does.
  function pickSection(n) {
    if (!Number.isInteger(n)) return;
    outline.active = n;
    // As an overlay it is covering the very thing it just jumped to.
    if (narrow.matches) setOutline(false);

    // Asking for a section by number is unambiguous, so it beats a filter that would
    // hide it. Without this the jump fetched the right page, the filter dropped it on
    // the way to the screen, and the click did nothing and said nothing — the outline
    // looked broken while the transcript was merely narrowed. The outline is not task
    // filtered, so it can offer rows the transcript is currently hiding.
    if (taskFilter && !pad.sections.some((s) => s.n === n && s.task === taskFilter)) {
      taskFilter = 0;
      taskPanel.active = 0;
      // `loaded` is that task's thread, so the section being asked for is not in it:
      // jumpTo below fetches the page it lives on and re-renders. No interim paint.
    }

    const target = body.querySelector(`[data-section="${n}"]`);
    if (target) {
      target.scrollIntoView({ block: "start" });
      return;
    }
    jumpTo(n);
  }

  // ── which section is being read ────────────────────────────────────────────
  //
  // "Where am I?" is answered against a READING LINE — the first line of transcript
  // the reader can actually see, just under the sticky toolbar — and the section being
  // read is the one that line falls inside.
  //
  // The obvious implementation, an IntersectionObserver over a band with the topmost
  // intersecting section winning, is wrong in the case that matters: a section scrolled
  // almost entirely behind the toolbar still pokes into the band, so it keeps the
  // highlight while the section actually filling the screen does not get it. Measuring
  // against a line has no such ambiguity — exactly one section contains it.
  //
  // Rects have to be read at scroll time (a section's height changes as its markdown
  // is parsed), so this runs from a scroll listener throttled to one frame. It is a
  // loop over the ~20 loaded messages, not the whole pad.
  let activeFrame = 0;

  // readingLine is where the transcript starts being visible: below the toolbar when
  // it is stuck to the top, otherwise at the top edge of the scrolling area.
  function readingLine() {
    const sc = scroller();
    const top = sc === document.scrollingElement ? 0 : sc.getBoundingClientRect().top;
    const bar = frame?.toolbar;
    const barBottom = bar && prefs.stickyBar() ? bar.getBoundingClientRect().bottom : -Infinity;
    return Math.max(top, barBottom) + 8;
  }

  function updateActive() {
    const msgs = [...body.querySelectorAll(".msg")];
    if (!msgs.length) return;
    const line = readingLine();

    let pick = null;
    for (const m of msgs) {
      const r = m.getBoundingClientRect();
      if (r.top <= line && r.bottom > line) { pick = m; break; }
    }
    // Nothing contains the line — a short pad whose sections all sit below it, or a
    // scroll position past the last one. Fall back to the nearest section on the side
    // where the reader is looking, so a pad with two sections still highlights one.
    if (!pick) {
      const below = msgs.filter((m) => m.getBoundingClientRect().top > line);
      pick = below.length
        ? below.reduce((a, b) => (b.getBoundingClientRect().top < a.getBoundingClientRect().top ? b : a))
        : msgs.reduce((a, b) => (b.getBoundingClientRect().bottom > a.getBoundingClientRect().bottom ? b : a));
    }
    const n = Number(pick.dataset.section);
    if (n && n !== outline.active) outline.active = n;
  }

  // At most one recomputation per frame, however many events arrive in it.
  const scheduleActive = () => {
    if (activeFrame) return;
    activeFrame = requestAnimationFrame(() => { activeFrame = 0; updateActive(); });
  };

  // Capture phase: the transcript scrolls in the layout's container, not the window,
  // and scroll events do not bubble — so this listener sees EVERY scrollable element
  // on the page, including the outline's own list. Scrolling the index moves the
  // index; the transcript has not moved, so the reading line still falls on the same
  // section and there is nothing to work out. Only a scroller that CONTAINS the
  // transcript can have changed the answer.
  const onAnyScroll = (e) => {
    const t = e.target;
    if (t && t.nodeType === 1 && !t.contains(body)) return;
    scheduleActive();
  };
  document.addEventListener("scroll", onAnyScroll, true);
  window.addEventListener("resize", scheduleActive);

  // Sections grow as their markdown is parsed and when one is expanded, which moves
  // every section below them past the reading line without anyone scrolling.
  const sizeWatch = typeof ResizeObserver === "function" ? new ResizeObserver(scheduleActive) : null;
  sizeWatch?.observe(body);

  // The participants strip: per agent, when they were last heard from and what is
  // waiting on them. It is the first thing worth looking at in a pad with five agents
  // — a person opens the UI to find out where coordination has broken, not to read.
  //
  // It deliberately shows LAST ACTIVITY, not presence. Presence cannot be derived from
  // an append-only transcript, and it would lie in both directions: an agent busy
  // working is not inside a wait, and an agent parked in a wait with the wrong
  // selectors is not listening for you.
  // It is also the pad's ROSTER, and the only one: a second row listing the same agents
  // — one with faces, one with their standing — asks the reader to look twice to learn
  // less. So the faces live here, on the row that also says when each was last heard
  // from, and an agent who was addressed and never answered appears too. That last part
  // is why this is the row that survived: it is the only one that can show them.
  function peopleRow() {
    peopleStrip = el("div", { class: "pad__people" });
    paintPeople();
    return peopleStrip;
  }

  // Repainted from a key rather than on every call: syncToolbar runs on scrolls and
  // filter changes too, and rebuilding this strip under a hovering cursor for a scroll
  // event would be work nobody asked for.
  //
  // The key is what is ON SCREEN, not the data behind it. Every age here is relative,
  // so the two things that change it are an agent moving (new data) and time passing
  // (same data, different words) — and a key over the raw participants would catch only
  // the first, which is how a strip that says "2 minutes ago" comes to say it an hour
  // later. Keying on the rendered strings covers both, and still skips the rebuild in
  // the common case where a minute has ticked and no wording actually changed.
  function paintPeople() {
    const view = (pad.participants || []).map((p) => ({
      author: p.author,
      last: p.last_section ? `§${p.last_section} · ${relTime(p.last_ts)}` : "never posted",
      lastTitle: p.last_ts ? absTime(p.last_ts) : "has never posted in this pad",
      // Said as a debt, not as a second timestamp. Both halves of a person's entry are
      // "a section and a time", and unlabelled they read as the same fact written twice
      // — the more so when the two times agree, which they do exactly when someone has
      // just been asked something.
      owes: (p.owes || []).length
        ? `owes ${(p.owes || []).map((o) => `${o.what} (${relTime(o.ts)})`).join(", ")}`
        : "",
      owesTitle: (p.owes || []).map((o) => `${o.what}: ${o.title || ""}`).join("\n"),
    }));
    const key = JSON.stringify(view);
    if (key === peopleKey) return;
    peopleKey = key;

    const cells = view.map((v) => {
      const face = el("span", {
        class: "person__avatar", text: agentInitials(v.author), "aria-hidden": "true",
      });
      face.style.setProperty("--avatar-bg", `var(--avatar-c${agentColorIndex(v.author)})`);
      const cell = el("div", { class: "person", dataset: { owing: String(!!v.owes) } },
        face,
        el("span", { class: "person__name", text: v.author }),
        el("span", { class: "person__last", title: v.lastTitle, text: v.last }),
      );
      if (v.owes) {
        cell.append(el("span", { class: "person__owes", title: v.owesTitle, text: v.owes }));
      }
      return cell;
    });
    peopleStrip.replaceChildren(...cells);
    peopleStrip.hidden = !cells.length;
  }

  // Where this pad sits, above its title: Projects → the project. The pad itself is NOT
  // a crumb — the title right underneath already says which pad you are on, and repeating
  // it costs a line to tell you something you can read at twice the size.
  //
  // Hand-built rather than <puredashboard-breadcrumb>, because that component treats its
  // LAST crumb as the current page and renders it as plain text. Here the last crumb is
  // the project, which is precisely the one that has to stay clickable.
  function breadcrumb() {
    return el("nav", { class: "pad__crumbs", "aria-label": "Breadcrumb" },
      el("ol", {},
        el("li", {}, el("a", { href: "#/projects", text: "Projects" })),
        el("li", {}, el("a", {
          href: `#/projects/${encodeURIComponent(pad.project)}`, text: pad.project,
        })),
      ),
    );
  }

  function metaRow() {
    // Copy sits ON the id it copies. As a labelled button among the page's actions it
    // said "Copy ref" and left you to work out which ref; here it touches the thing.
    const row = el("div", { class: "pad__meta" },
      el("span", { class: "pad__id" },
        el("span", { class: "ref", text: ref }),
        copyIconButton(ref, "Copy this pad's ref"),
      ),
      el("span", { class: "muted", title: absTime(pad.created_ts), text: `created ${relTime(pad.created_ts)}` }),
    );
    if (pad.protected) row.append(el("puredashboard-tag", { color: "warning", size: "sm", text: "protected" }));
    // No roster here: the participants strip below IS this pad's roster, and it is shown
    // whole — this page is where you come to find out exactly who is on a conversation,
    // so a "+2" would be hiding the answer. The pads TABLE still caps its own list,
    // because a row there has one line's worth of room.
    return row;
  }

  // watchToggle is an action on the pad, so it belongs with the other actions in the
  // header rather than trailing the facts about it in the meta row.
  function watchToggle() {
    watchSwitch = el("puredashboard-switch");
    watchSwitch.label = "Watch this pad";
    watchSwitch.checked = wl.isWatched(ref);
    watchSwitch.addEventListener("change", (e) => {
      wl.setWatched(ref, e.target.checked);
      toast(e.target.checked ? `Watching ${ref}` : `Stopped watching ${ref}`, { type: "info" });
    });
    return watchSwitch;
  }

  // buildToolbar creates the controls ONCE and hands back the parts that later have
  // something to say. Nothing in here is rebuilt afterwards — which is what lets a
  // person keep typing into "Jump to #" while an agent is posting.
  function buildToolbar() {
    // The placeholder IS the empty option — adding a second "All authors" entry would
    // put two identical, identically-valued rows in the list.
    const filter = el("puredashboard-select", { placeholder: "All authors" });
    filter.addEventListener("change", (e) => { authorFilter = e.target.value; render(); });

    const jump = el("puredashboard-input", { type: "number", placeholder: "Jump to §" });
    jump.addEventListener("keydown", (e) => {
      if (e.key !== "Enter") return;
      const n = Number(jump.value);
      if (Number.isInteger(n) && n >= 1 && n <= pad.section_count) jumpTo(n);
      else toast(`This pad has sections 1–${pad.section_count}`, { type: "warn" });
    });

    // The pad's name, shown only once the real title has scrolled away: the toolbar is
    // then the only thing on screen, and a row of controls with no subject is a row of
    // controls for whatever you last had open.
    const stuckTitle = el("span", { class: "pad__toolbar-title stuck-only", text: pad.title || ref, title: pad.title || ref });
    const range = el("span", { class: "muted pad__toolbar-range" });

    // No outline toggle here. The rail closes with the « on the rail and opens with
    // the » left in its place — one control, where the thing it controls is. A second
    // one in this row said the same thing from across the page, in a button that looked
    // like the four beside it.

    // Labelled with what it DOES, like "Expand all" beside it, rather than with the
    // state it is in — the current order is visible in the transcript itself.
    const orderBtn = el("button", {
      type: "button", class: "ghost-btn",
      onclick: () => {
        order = order === "newest" ? "oldest" : "newest";
        prefs.setOrder(order);   // Settings shows the same choice
        render();
        // Flipping to oldest-first would otherwise leave the reader at the top of a
        // long history; the newest turn is what they were just looking at.
        scrollToNewest();
      },
    });

    const expandBtn = el("button", {
      type: "button", class: "ghost-btn",
      onclick: () => { expandAll = !expandAll; render(); },
    });

    const latestBtn = el("button", {
      type: "button", class: "ghost-btn", text: "Latest", hidden: true,
      onclick: () => loadLatest(),
    });

    // The full menu, for when the header — and with it Copy ref and the Watch switch —
    // has scrolled away. Hidden until that happens, so nothing is on screen twice.
    const menu = padMenuButton({ full: true });
    menu.classList.add("stuck-only");

    const bar = el("div", { class: "pad__toolbar" },
      stuckTitle, range, el("span", { class: "page__spacer" }),
      filter, jump, orderBtn, expandBtn, latestBtn, menu,
    );
    return { toolbar: bar, filter, jump, range, stuckTitle, orderBtn, expandBtn, latestBtn };
  }

  // syncToolbar writes the current state onto controls that already exist. Everything
  // here is a string, a flag or a value — never a new node.
  function syncToolbar() {
    const f = frame;
    // A filter must never be invisible. The range describes the PAGE, so while the
    // transcript is narrowed to one task it was describing sections it was not showing
    // — "showing #27–#46 of 46" over three messages — which is how a filter left on by
    // accident reads as a broken page rather than as a filter.
    f.range.textContent = taskFilter
      ? `T${taskFilter} only · ${countInTask(taskFilter)} of ${pad.section_count} sections`
      : loaded.length
        ? `showing §${loaded[0].n}–§${loaded.at(-1).n} of ${pad.section_count}`
        : `${pad.section_count} sections`;

    // Authors only ever grow, and re-assigning the list would close an open dropdown,
    // so it is written only when it actually changed. The roster comes from the server
    // (`authors` on the pad response) rather than being re-derived here, so the filter,
    // the pads table and the CLI all agree on who is on a pad.
    //
    // U+001F joins the comparison key because an author name cannot contain it — a
    // plain separator would let two different lists compare equal — and unlike a NUL it
    // leaves this file text, which is what keeps grep, rg and diff viewers reading it.
    const authors = pad.authors || [];
    if (authors.join("\u001f") !== authorOptions) {
      authorOptions = authors.join("\u001f");
      f.filter.options = authors.map((a) => ({ value: a, label: a }));
    }
    // The strip has its own key, because it moves on more than the roster does: an agent
    // falling behind changes it while the list of names stays exactly the same.
    paintPeople();
    if (f.filter.value !== authorFilter) f.filter.value = authorFilter;

    f.orderBtn.textContent = order === "newest" ? "Oldest first" : "Newest first";
    f.orderBtn.title = order === "newest"
      ? "Read the pad from its first section"
      : "Read the pad newest section first";
    f.expandBtn.textContent = expandAll ? "Collapse long" : "Expand all";
    f.latestBtn.hidden = showingLatest;
    f.toolbar.dataset.sticky = String(prefs.stickyBar());
    if (watchSwitch) watchSwitch.checked = wl.isWatched(ref);
  }

  // ── sticky toolbar ─────────────────────────────────────────────────────────
  //
  // The toolbar is sticky, but "am I stuck?" is not something CSS can answer, and the
  // bar has to look and contain different things once it is: a shadow to lift it off
  // the transcript, the pad's name, and the pad's actions that scrolled away with the
  // header. A zero-height sentinel just above it goes out of view exactly when the bar
  // reaches the top, which is the signal.
  let stuckObserver = null;

  function observeStuck() {
    stuckObserver?.disconnect();
    if (!prefs.stickyBar() || typeof IntersectionObserver !== "function" || !frame) return;
    const { sentinel, toolbar: bar } = frame;
    stuckObserver = new IntersectionObserver(([e]) => {
      bar.dataset.stuck = String(!e.isIntersecting);
    }, { root: scroller(), threshold: 0 });
    stuckObserver.observe(sentinel);
  }

  // ── lazy markdown ──────────────────────────────────────────────────────────
  //
  // Parsing markdown is the expensive part of this page: a section can be thousands of
  // words, and a page of them lands at once. <puredashboard-lazy> does the deferring —
  // it holds the reserved height, the placeholder, the IntersectionObserver and the
  // print hook — so what is left here is only the policy this page wants on top of it:
  // parse what is already on screen at once, and fill the rest in while idle.
  const LAZY_MARGIN = "800px";   // start parsing roughly a screen ahead
  let idleHandle = 0;

  // pendingLazy finds the boxes still holding a placeholder. data-state is the
  // component's own reflected state, so this stays true however it was rendered.
  const pendingLazy = () => body.querySelectorAll('puredashboard-lazy[data-state="pending"]');

  function deferMarkdown(box, content, clamped) {
    const lz = el("puredashboard-lazy", { class: "sec__lazy" });
    lz.rootMargin = LAZY_MARGIN;
    // A guessed height keeps the scrollbar still while bodies materialise above the
    // reader; the component swaps in the real height as soon as it renders.
    lz.height = estimateHeight(content.length, clamped);
    lz.render = () => {
      const md = el("puredashboard-markdown", { class: "sec__content" });
      md.value = content;
      return md;
    };
    box.replaceChildren(lz);
  }

  // A clamped body is capped at the clamp height; an open one is guessed from its
  // length. Both are rough on purpose — the box shrinks to its real height the
  // moment it is parsed, and over-reserving is worse than under-reserving.
  function estimateRem(len, clamped) {
    if (clamped) return 15;
    const lines = Math.min(40, Math.max(2, Math.ceil(len / 90)));
    return lines * 1.5;
  }

  function estimateHeight(len, clamped) {
    return `${estimateRem(len, clamped).toFixed(1)}rem`;
  }

  // Called after each render(). Two things the component cannot decide for itself:
  //
  // Anything already on screen is parsed NOW. The component's observer fires on the
  // next frame, and showing a placeholder for a frame in the message the reader is
  // looking at is a flicker for no reason.
  //
  // Then the rest is filled in while the page is idle, because a body that is not in
  // the DOM cannot be found by ⌘F, selected by ⌘A or saved with the page — browser
  // behaviour a reading surface has no business breaking. Deferring was only ever
  // about keeping the work off the critical path, not about leaving it undone.
  function observeLazy() {
    const fold = window.innerHeight;
    for (const lz of pendingLazy()) {
      if (lz.getBoundingClientRect().top < fold) lz.renderNow("eager");
    }
    scheduleIdleParse();
  }

  function scheduleIdleParse() {
    if (typeof requestIdleCallback !== "function") return; // Safari: the observer alone
    if (idleHandle) cancelIdleCallback(idleHandle);
    idleHandle = requestIdleCallback((deadline) => {
      idleHandle = 0;
      // 8ms of headroom left in the slice: enough for one body without overrunning it.
      for (const lz of pendingLazy()) {
        if (deadline.timeRemaining() <= 8) break;
        lz.renderNow("idle");
      }
      if (pendingLazy().length) scheduleIdleParse();
    }, { timeout: 3000 });
  }

  // sectionNode builds one message: the author's avatar, then a bubble holding the
  // section's title and prose — clamped when it is long, so a 5000-word section does
  // not bury the messages around it.
  //
  // Every message sits on the same side. A pad is a group conversation between N
  // agents with no "me" to mirror against, so the left/right split of a two-party
  // chat has nothing to encode here; the avatar carries the identity instead, on a
  // colour hashed from the author's name so an agent looks the same in every pad.
  function sectionNode(sec) {
    const long = sec.content.length > CLAMP_BYTES;
    const clamped = long && !expandAll && sec.n !== pad.section_count;

    const bodyBox = el("div", { class: "sec__body", dataset: { clamped: String(clamped) } });
    deferMarkdown(bodyBox, sec.content, clamped);

    const bubble = el("div", { class: "msg__bubble" },
      sec.title ? el("div", { class: "msg__title", text: sec.title }) : null,
      bodyBox,
    );

    if (long) {
      const toggle = el("button", {
        type: "button", class: "sec__expand",
        text: clamped ? "Expand" : "Collapse",
      });
      toggle.addEventListener("click", () => {
        // Expanding is a request to read this body NOW, so stop deferring it — the
        // observer may not have reached it if the click came from a keyboard focus.
        bodyBox.querySelector('puredashboard-lazy[data-state="pending"]')?.renderNow("manual");
        const nowClamped = bodyBox.dataset.clamped !== "true";
        bodyBox.dataset.clamped = String(nowClamped);
        toggle.textContent = nowClamped ? "Expand" : "Collapse";
      });
      bubble.append(toggle);
    }

    // The avatar is written by hand rather than taken from the component library:
    // that one derives initials from a PERSON's name (first + last word), which for
    // a one-word handle like "backend" yields a bare "B". agentInitials knows what
    // agent handles look like. It is aria-hidden — the author's name is right next
    // to it, so a screen reader would only hear the same thing twice.
    const avatar = el("span", {
      class: "msg__avatar", text: agentInitials(sec.author),
      title: sec.author, "aria-hidden": "true",
    });
    avatar.style.setProperty("--avatar-bg", `var(--avatar-c${agentColorIndex(sec.author)})`);

    const node = el("article", {
      class: justArrived.has(sec.n) ? "msg msg--new" : "msg",
      dataset: { section: String(sec.n) },
    },
      avatar,
      el("div", { class: "msg__col" },
        el("div", { class: "msg__head" },
          el("span", { class: "msg__author", text: sec.author }),
          el("span", { class: "msg__n", text: `§${sec.n}` }),
          el("span", { class: "msg__time", title: absTime(sec.ts), text: clockTime(sec.ts) }),
          el("span", { text: bytes(sec.content.length) }),
          ...routingChips(sec),
        ),
        bubble,
      ),
    );
    // Placeholder height for `contain-intrinsic-size` while the message is off-screen
    // and its rendering is skipped: the body's estimate plus the header, the title and
    // the bubble's padding. The browser replaces it with the real height on first
    // render and remembers that afterwards.
    node.style.setProperty("--msg-est", `${(estimateRem(sec.content.length, clamped) + 4.5).toFixed(1)}rem`);
    return node;
  }

  // Routing, shown where the section is. This is what turns a transcript into a
  // conversation you can follow: who a section was for, what it answers, and whether it
  // is a task event (which behaves differently — it does not take the turn).
  //
  // All of it is free: /api/pads/{ref} already returns the pad's ENTIRE table of
  // contents, so the reply links and the reply count come from data the page holds, with
  // no extra request.
  //
  // A broadcast draws no chip. Absent `to` already means everyone, and a chip on every
  // section written before addressing existed would be noise, not information.
  function routingChips(sec) {
    const meta = pad.sections.find((s) => s.n === sec.n) || sec;
    const out = [];
    // A rules section is marked where it sits, because it behaves differently from the
    // prose around it: it does not take the turn, and only the LAST one is in force —
    // which is why an older one says so rather than looking like current policy.
    if (meta.kind === "rules") {
      const inForce = pad.rules?.layers?.find((l) => l.level === "pad")?.section === meta.n;
      out.push(el("button", {
        type: "button", class: "chip chip--rules" + (inForce ? "" : " chip--rules-old"),
        text: inForce ? "RULES" : "RULES (superseded)",
        title: inForce ? "The rules in force on this pad" : "An earlier version of the pad's rules",
        onclick: () => showRules({ kind: "pad", ref, project: pad.project }, pad.rules, {
          onSection: (n) => pickSection(n),
          onChange: (rules) => { pad.rules = rules; rulesEntry?.repaint(); },
        }),
      }));
    }
    if (meta.task) {
      const chip = el("button", {
        type: "button", class: "chip chip--task", dataset: { status: meta.status || "" },
        title: `Show only what concerns T${meta.task}`,
        text: meta.status ? `T${meta.task} ${meta.status}` : `T${meta.task}`,
        onclick: () => { void selectTask(taskFilter === meta.task ? 0 : meta.task); },
      });
      out.push(chip);
    }
    for (const target of meta.to || []) {
      out.push(el("span", { class: "chip chip--to", text: `→ ${target}` }));
    }
    if (meta.re) {
      out.push(el("button", {
        type: "button", class: "chip chip--re", text: `↩ §${meta.re}`,
        title: `Go to the section this answers`,
        onclick: () => pickSection(meta.re),
      }));
    }
    const replies = pad.sections.filter((s) => s.re === sec.n);
    if (replies.length) {
      out.push(el("button", {
        type: "button", class: "chip chip--replies",
        text: `${replies.length} ${replies.length === 1 ? "reply" : "replies"}`,
        title: replies.map((r) => `§${r.n} ${r.author}: ${r.title}`).join("\n"),
        onclick: () => pickSection(replies[0].n),
      }));
    }
    return out;
  }

  // The pad's page is the ONLY place a pad can be deleted. A destructive action
  // belongs where the thing it destroys is on screen — the title, the participants and
  // how much history there is — not behind a button in a list that reorders itself.
  // It lives in the header's overflow menu rather than under the transcript: deleting
  // is rare, and the confirm dialog — not proximity to the text — is what guards it.
  // padMenuButton builds the overflow menu — and an overflow menu holds what is NOT
  // already on screen, which differs by where the button is. Beside the header, Copy
  // ref is a button and Watch is a switch an inch away, so the menu is the one action
  // that has neither: delete. In the sticky toolbar the header is gone, so the same
  // menu is the only way to reach any of them and carries all three.
  function padMenuButton({ full = false } = {}) {
    const btn = el("button", {
      type: "button", class: "ghost-btn icon-btn", text: "⋯",
      title: "Pad actions", "aria-label": "Pad actions", "aria-haspopup": "menu",
    });
    btn.addEventListener("click", async () => {
      const watching = wl.isWatched(ref);
      const items = [];
      if (full) {
        items.push(
          { label: "Copy ref", value: "copy", icon: ICON_COPY, shortcut: ref },
          {
            label: "Watch this pad", value: "watch", icon: ICON_BELL,
            // A checkbox item states what IS, so the label stays put while the tick moves.
            checked: watching,
            // Checkbox items deliberately keep the menu open (you may be ticking
            // several), which means the menu's promise does not resolve for them — the
            // work belongs in onSelect. Closing anyway: only one thing to tick here.
            closeOnSelect: true,
            onSelect: () => {
              wl.setWatched(ref, !watching);
              toast(!watching ? `Watching ${ref}` : `Stopped watching ${ref}`, { type: "info" });
              render(); // the header's switch shows the same state
            },
          },
          { separator: true },
        );
      }
      items.push({ label: "Delete this pad", value: "delete", icon: ICON_TRASH, danger: true });

      const picked = await menu(btn, items, { placement: "bottom-end" });
      if (picked === "copy") {
        try {
          await navigator.clipboard.writeText(ref);
          toast(`Copied ${ref}`, { type: "success" });
        } catch {
          toast("The browser blocked the clipboard; the ref is in the page header", { type: "warn" });
        }
      } else if (picked === "delete") {
        await deletePad();
      }
    });
    return btn;
  }

  async function deletePad() {
    const authors = (pad.authors || []).join(", ");
    const ok = await confirm(
      `${ref} — “${pad.title || "untitled"}”\n` +
      `${pad.section_count} section${pad.section_count === 1 ? "" : "s"} between ${authors}.\n\n` +
      "The file and its whole history are removed. This cannot be undone.",
      { title: "Delete this pad?", okText: "Delete", danger: true },
    );
    if (!ok) return;
    try {
      await api.deletePad(ref);
      wl.setWatched(ref, false);
      toast(`Deleted ${ref}`, { type: "success" });
      location.hash = "#/pads";
    } catch (err) {
      toast(err.message, { type: "error" });
    }
  }

  // ── live ───────────────────────────────────────────────────────────────────

  const off = onPad(async (ev) => {
    if (ev.ref !== ref) return;
    if (ev.type === "removed") {
      outlet.replaceChildren(errorView({ code: "pad_not_found", status: 404, message: "This pad was deleted." }));
      return;
    }
    if (!pad || pad.locked) return;

    const previous = pad.section_count;
    try {
      pad = await api.pad(ref);
    } catch { return; }
    if (disposed) return;
    loadTasks();

    const added = pad.section_count - previous;
    if (added <= 0) { render(); return; }

    // Reading history: do NOT move the viewport. Offer the jump instead.
    if (!showingLatest) {
      pendingNew += added;
      render();
      return;
    }
    if (added > PAGE) { await loadLatest(); return; }

    try {
      const page = await api.sections(ref, { limit: added });
      if (disposed) return;
      const fresh = page.sections.filter((s) => !loaded.some((l) => l.n === s.n));
      // Reading oldest-first the new turn lands at the BOTTOM, so follow it only if
      // the reader was already there — someone who scrolled up is reading, not
      // waiting, and yanking them down mid-sentence is worse than a missed line.
      const follow = order === "oldest" && atBottom();
      loaded = [...loaded, ...fresh];
      wl.markSeen(ref, pad.section_count);
      // Flash what just arrived, so a reply landing mid-read is obvious.
      for (const s of fresh) justArrived.add(s.n);
      render();
      if (follow) scrollToNewest();
      setTimeout(() => { for (const s of fresh) justArrived.delete(s.n); }, 3000);
    } catch { /* the next event will resync */ }
  });

  // Settings is a different route, so a change made there has to be pushed here — the
  // pad page does not re-mount when the person navigates back to it from the same tab.
  const offPrefs = prefs.onChange((name, value) => {
    if (disposed || !pad || pad.locked) return;
    if (name === "order") {
      if (value === order) return;
      order = value;
      render();
      scrollToNewest();
    } else if (name === "stickyBar") {
      render();
    } else if (name === "outline") {
      if (value === outlineOpen) return;
      outlineOpen = value;
      applyOutline();
    }
  });

  loadPad();

  // Nothing arrives on a quiet pad, and "last heard from 2 minutes ago" is exactly the
  // reading a person acts on — left ticking at whatever it said when the tab opened, it
  // reports a team that is all present. The sidebar already keeps its own ages honest
  // this way; a minute is close enough here too, and the display key means a tick that
  // changes no wording costs one comparison.
  const ageTick = setInterval(() => { if (!disposed && peopleStrip) paintPeople(); }, 60_000);

  return () => {
    disposed = true;
    clearInterval(ageTick);
    off();
    offPrefs();
    // The lazy elements disconnect their own observers as they leave the DOM.
    stuckObserver?.disconnect();
    holdStop?.();
    if (activeFrame) cancelAnimationFrame(activeFrame);
    sizeWatch?.disconnect();
    document.removeEventListener("scroll", onAnyScroll, true);
    window.removeEventListener("resize", scheduleActive);
    narrow.removeEventListener("change", onNarrowChange);
    document.removeEventListener("pointerdown", dismissOverlay, true);
    document.removeEventListener("keydown", dismissOverlay);
    // The outline's hover popup lives on document.body, so it does not leave with the
    // page's own subtree — the component removes it when it disconnects.
    outline.remove();
    if (idleHandle && typeof cancelIdleCallback === "function") cancelIdleCallback(idleHandle);
  };
}
