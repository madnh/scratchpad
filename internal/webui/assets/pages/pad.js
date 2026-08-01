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

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import * as wl from "/lib/watchlist.js";
import * as prefs from "/lib/prefs.js";
import { el, pageHead, skeleton, errorView, copyButton } from "/lib/ui.js";
import { relTime, absTime, clockTime, bytes, agentInitials, agentColorIndex } from "/lib/fmt.js";

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
  let expandAll = false;
  // Reading direction. Newest-first is the default — opening a pad to see what just
  // happened is the common visit — but a pad read from the start is a conversation,
  // and some people want it that way round. The choice is per person, not per pad, so
  // it is remembered across pads and sessions.
  let order = prefs.order();
  // Sections that arrived while this page was open, so they can be flashed. The
  // marker is applied while BUILDING each node rather than by querying the DOM
  // after render(), so it survives a re-render from any source.
  const justArrived = new Set();

  const body = el("div");

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
  // C0/C1 controls, the bidi overrides and isolates, and the zero-width/BOM characters
  // that let a string render as something other than what it contains.
  const UNSAFE_TITLE_CHARS =
    /[\u0000-\u001F\u007F-\u009F\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF]/g;

  function padTitleForTab(raw) {
    const cleaned = String(raw ?? "").replace(UNSAFE_TITLE_CHARS, " ").replace(/\s+/g, " ").trim();
    if (!cleaned) return "";
    // Array.from splits by code point, so an emoji or a non-BMP character counts once
    // and is never cut in half.
    const chars = Array.from(cleaned);
    if (chars.length <= TAB_TITLE_CHARS) return cleaned;
    return `${chars.slice(0, TAB_TITLE_CHARS - 1).join("").trimEnd()}\u2026`;
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
  function holdSection(sec, top, ms = 2500) {
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
    const stop = () => { ro?.disconnect(); ro = null; };
    fix();
    if (typeof ResizeObserver === "function") {
      ro = new ResizeObserver(fix);
      ro.observe(body);
      setTimeout(stop, ms);
    }
  }

  // scrollToNewest puts the newest section in view. Reading oldest-first that is the
  // BOTTOM of the transcript — the same place a chat app opens at, because the latest
  // turn is what a person came to see.
  function scrollToNewest() {
    if (order !== "oldest") return;
    const sc = scroller();
    sc.scrollTop = sc.scrollHeight;
  }

  // atBottom allows a little slack: a reader who stopped a line short of the end is
  // still "at the end" as far as following new turns goes.
  function atBottom(slack = 120) {
    const sc = scroller();
    return sc.scrollHeight - sc.scrollTop - sc.clientHeight <= slack;
  }

  // jumpTo centres the history on one section: load the page ENDING at it, so the
  // section and what led up to it arrive together.
  async function jumpTo(n) {
    const page = await api.sections(ref, { before: n + 1, limit: PAGE });
    if (disposed) return;
    loaded = page.sections;
    hasOlder = page.has_older;
    showingLatest = page.sections.at(-1)?.n === pad.section_count;
    render();
    const target = body.querySelector(`[data-section="${n}"]`);
    // Instant, not smooth: the jump can span twenty screens of history, and a smooth
    // run through them is both useless to watch and unreliable — the animation
    // competes with the bodies being parsed along the way and can end short of the
    // target. Landing directly is what "jump" means anyway.
    if (target) target.scrollIntoView({ block: "center" });
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

  function render() {
    const authors = [...new Set(pad.sections.map((s) => s.author))];
    const visible = authorFilter ? loaded.filter((s) => s.author === authorFilter) : loaded;
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

    outlet.replaceChildren(
      pageHead(pad.title || ref, null, copyButton(ref), padMenuButton()),
      metaRow(),
      el("div", { class: "pad__sticky-sentinel" }),
      toolbar(authors),
      body,
    );
    // Only now are the bodies in the document and measurable against the viewport.
    observeLazy();
    observeStuck();
  }

  function metaRow() {
    const row = el("div", { class: "pad__meta" },
      el("span", { class: "ref", text: ref }),
      el("puredashboard-tag", { color: "info", size: "sm", text: pad.project }),
      el("span", { class: "muted", title: absTime(pad.created_ts), text: `created ${relTime(pad.created_ts)}` }),
    );
    if (pad.protected) row.append(el("puredashboard-tag", { color: "warning", size: "sm", text: "protected" }));

    const sw = el("puredashboard-switch");
    sw.label = "Watch this pad";
    sw.checked = wl.isWatched(ref);
    sw.addEventListener("change", (e) => {
      wl.setWatched(ref, e.target.checked);
      toast(e.target.checked ? `Watching ${ref}` : `Stopped watching ${ref}`, { type: "info" });
    });
    row.append(sw);
    return row;
  }

  function toolbar(authors) {
    const range = loaded.length
      ? `showing #${loaded[0].n}–#${loaded.at(-1).n} of ${pad.section_count}`
      : `${pad.section_count} sections`;

    // The placeholder IS the empty option — adding a second "All authors" entry would
    // put two identical, identically-valued rows in the list.
    const filter = el("puredashboard-select", { placeholder: "All authors" });
    filter.options = authors.map((a) => ({ value: a, label: a }));
    filter.value = authorFilter;
    filter.addEventListener("change", (e) => { authorFilter = e.target.value; render(); });

    const jump = el("puredashboard-input", { type: "number", placeholder: "Jump to #" });
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

    const bar = el("div", {
      class: "pad__toolbar",
      dataset: { sticky: String(prefs.stickyBar()) },
    },
      stuckTitle,
      el("span", { class: "muted pad__toolbar-range", text: range }),
      el("span", { class: "page__spacer" }),
      filter,
      jump,
      // Labelled with what it DOES, like "Expand all" beside it, rather than with the
      // state it is in — the current order is visible in the transcript itself.
      el("button", {
        type: "button", class: "ghost-btn",
        text: order === "newest" ? "Oldest first" : "Newest first",
        title: order === "newest"
          ? "Read the pad from its first section"
          : "Read the pad newest section first",
        onclick: () => {
          order = order === "newest" ? "oldest" : "newest";
          prefs.setOrder(order);   // Settings shows the same choice
          render();
          // Flipping to oldest-first would otherwise leave the reader at the top of a
          // long history; the newest turn is what they were just looking at.
          scrollToNewest();
        },
      }),
      el("button", {
        type: "button", class: "ghost-btn",
        text: expandAll ? "Collapse long" : "Expand all",
        onclick: () => { expandAll = !expandAll; render(); },
      }),
    );
    if (!showingLatest) {
      bar.append(el("button", { type: "button", class: "ghost-btn", text: "Latest", onclick: () => loadLatest() }));
    }
    // The pad's own actions, duplicated into the toolbar for when the header is gone.
    // Hidden until then, so they never appear twice on screen at once.
    const menu = padMenuButton();
    menu.classList.add("stuck-only");
    bar.append(menu);
    return bar;
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
    if (!prefs.stickyBar() || typeof IntersectionObserver !== "function") return;
    const sentinel = body.parentElement?.querySelector(".pad__sticky-sentinel");
    const bar = body.parentElement?.querySelector(".pad__toolbar");
    if (!sentinel || !bar) return;
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
          el("span", { class: "msg__n", text: `#${sec.n}` }),
          el("span", { class: "msg__time", title: absTime(sec.ts), text: clockTime(sec.ts) }),
          el("span", { text: bytes(sec.content.length) }),
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

  // The pad's page is the ONLY place a pad can be deleted. A destructive action
  // belongs where the thing it destroys is on screen — the title, the participants and
  // how much history there is — not behind a button in a list that reorders itself.
  // It lives in the header's overflow menu rather than under the transcript: deleting
  // is rare, and the confirm dialog — not proximity to the text — is what guards it.
  function padMenuButton() {
    const btn = el("button", {
      type: "button", class: "ghost-btn icon-btn", text: "⋯",
      title: "Pad actions", "aria-label": "Pad actions", "aria-haspopup": "menu",
    });
    btn.addEventListener("click", async () => {
      const picked = await menu(btn, [
        { label: "Delete this pad", value: "delete", danger: true },
      ], { placement: "bottom-end" });
      if (picked === "delete") await deletePad();
    });
    return btn;
  }

  async function deletePad() {
    const authors = [...new Set(pad.sections.map((s) => s.author))].join(", ");
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
    }
  });

  loadPad();

  return () => {
    disposed = true;
    off();
    offPrefs();
    // The lazy elements disconnect their own observers as they leave the DOM.
    stuckObserver?.disconnect();
    if (idleHandle && typeof cancelIdleCallback === "function") cancelIdleCallback(idleHandle);
  };
}
