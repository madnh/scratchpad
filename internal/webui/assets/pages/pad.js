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
// Newest-first ordering is not only about relevance: it means "load older" appends
// DOWNWARD, so the page never has to preserve scroll position around content
// inserted above the viewport.

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
    render();
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

    body.replaceChildren();

    if (pendingNew > 0) {
      body.append(el("button", {
        class: "newpill", type: "button",
        text: `${pendingNew} new section${pendingNew === 1 ? "" : "s"} ↑`,
        onclick: () => loadLatest(),
      }));
    }

    // The API hands back ascending sections; reverse for a newest-first read.
    const chat = el("div", { class: "chat" },
      [...visible].reverse().map((sec) => sectionNode(sec)));
    body.append(chat);

    if (!visible.length) {
      body.append(el("p", { class: "muted", text: "No sections match this filter." }));
    }

    if (hasOlder) {
      body.append(el("button", {
        class: "loadmore", type: "button", text: "Load 20 older sections",
        onclick: (e) => { e.currentTarget.disabled = true; loadOlder(); },
      }));
    } else if (loaded.length) {
      body.append(el("p", { class: "muted", text: "Beginning of the pad." }));
    }

    outlet.replaceChildren(
      pageHead(pad.title || ref, null, copyButton(ref), padMenuButton()),
      metaRow(),
      toolbar(authors),
      body,
    );
    // Only now are the bodies in the document and measurable against the viewport.
    observeLazy();
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

    const bar = el("div", { class: "pad__toolbar" },
      el("span", { class: "muted", text: range }),
      el("span", { class: "page__spacer" }),
      filter,
      jump,
      el("button", {
        type: "button", class: "ghost-btn",
        text: expandAll ? "Collapse long" : "Expand all",
        onclick: () => { expandAll = !expandAll; render(); },
      }),
    );
    if (!showingLatest) {
      bar.append(el("button", { type: "button", class: "ghost-btn", text: "Latest", onclick: () => loadLatest() }));
    }
    return bar;
  }

  // ── lazy markdown ──────────────────────────────────────────────────────────
  //
  // Parsing markdown is the expensive part of this page: a section can be thousands
  // of words, and a page of them lands at once. So a body is parsed only when it is
  // about to be seen — the box goes into the DOM empty and an IntersectionObserver
  // fills it in as the message approaches the viewport. Once parsed it STAYS parsed:
  // this defers work, it never throws it away and redoes it on the way back.
  //
  // The empty box reserves a height guessed from the section's byte count, so the
  // scrollbar and "load older" do not lurch as bodies materialise above the reader.
  const LAZY_MARGIN = "800px 0px";   // start parsing roughly a screen ahead
  const pendingMarkdown = new WeakMap();
  let lazyObserver = null;
  let idleHandle = 0;

  function deferMarkdown(box, content, clamped) {
    // No observer (very old engine, jsdom): parse immediately — correctness first.
    if (typeof IntersectionObserver !== "function") {
      paintMarkdown(box, content);
      return;
    }
    box.dataset.lazy = "pending";
    box.style.minHeight = estimateHeight(content.length, clamped);
    pendingMarkdown.set(box, content);
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

  function paintMarkdown(box, content) {
    const md = el("puredashboard-markdown", { class: "sec__content" });
    md.value = content;
    box.replaceChildren(md);
    box.style.minHeight = "";
    box.dataset.lazy = "done";
  }

  // Called after each render(): the previous observer's targets are detached nodes,
  // so it is dropped wholesale rather than unobserved one by one.
  //
  // The observed element is the MESSAGE, not the body inside it. Off-screen messages
  // carry `content-visibility: auto`, which skips their subtree's layout — a body
  // inside a skipped subtree has no box to intersect with, so watching it directly
  // would defeat the head start rootMargin is there to buy. The message itself always
  // has a box, because `contain-intrinsic-size` gives it one.
  function observeLazy() {
    if (typeof IntersectionObserver !== "function") return;
    lazyObserver?.disconnect();
    lazyObserver = new IntersectionObserver((entries, obs) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        obs.unobserve(e.target);
        const box = e.target.querySelector('.sec__body[data-lazy="pending"]');
        const content = box && pendingMarkdown.get(box);
        // Gone from the map = a re-render replaced this node while it was queued.
        if (content != null) {
          pendingMarkdown.delete(box);
          paintMarkdown(box, content);
        }
      }
    }, { rootMargin: LAZY_MARGIN });
    const fold = window.innerHeight;
    for (const msg of body.querySelectorAll('.msg:has(.sec__body[data-lazy="pending"])')) {
      // Anything already on screen is parsed NOW, synchronously. Observer callbacks
      // only run after the next frame, and waiting one frame to fill in the message
      // the reader is looking at shows them a skeleton for no reason.
      if (msg.getBoundingClientRect().top < fold) {
        const box = msg.querySelector('.sec__body[data-lazy="pending"]');
        const content = pendingMarkdown.get(box);
        if (content != null) {
          pendingMarkdown.delete(box);
          paintMarkdown(box, content);
          continue;
        }
      }
      lazyObserver.observe(msg);
    }
    scheduleIdleParse();
  }

  // Deferring the parse must not COST the reader anything permanent, and a body that
  // is not in the DOM cannot be found by ⌘F, selected by ⌘A, or included in "save
  // page" — browser behaviour a reading surface has no business breaking. So once the
  // page is idle, the rest is parsed anyway, a few bodies per idle slice: the point of
  // deferring was never to leave the work undone, only to keep it off the critical
  // path while the reader is waiting for the first screen.
  //
  // Messages already parsed this way still cost nothing to lay out — they are the ones
  // carrying content-visibility: auto.
  function scheduleIdleParse() {
    if (typeof requestIdleCallback !== "function") return; // Safari: the observer alone
    if (idleHandle) cancelIdleCallback(idleHandle);
    idleHandle = requestIdleCallback((deadline) => {
      idleHandle = 0;
      let box;
      // 8ms of headroom left in the slice: enough for one body without overrunning it.
      while (deadline.timeRemaining() > 8 && (box = body.querySelector('.sec__body[data-lazy="pending"]'))) {
        const content = pendingMarkdown.get(box);
        if (content == null) {
          box.dataset.lazy = "done"; // stale node from a re-render; do not spin on it
          continue;
        }
        pendingMarkdown.delete(box);
        const msg = box.closest(".msg");
        if (msg) lazyObserver?.unobserve(msg);
        paintMarkdown(box, content);
      }
      if (body.querySelector('.sec__body[data-lazy="pending"]')) scheduleIdleParse();
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
        // Expanding is a request to read this body now, so stop deferring it — the
        // observer may not have reached it if the click came from a keyboard focus.
        const queued = pendingMarkdown.get(bodyBox);
        if (queued != null) {
          pendingMarkdown.delete(bodyBox);
          lazyObserver?.unobserve(bodyBox);
          paintMarkdown(bodyBox, queued);
        }
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
      loaded = [...loaded, ...fresh];
      wl.markSeen(ref, pad.section_count);
      // Flash what just arrived, so a reply landing mid-read is obvious.
      for (const s of fresh) justArrived.add(s.n);
      render();
      setTimeout(() => { for (const s of fresh) justArrived.delete(s.n); }, 3000);
    } catch { /* the next event will resync */ }
  });

  loadPad();

  return () => {
    disposed = true;
    off();
    lazyObserver?.disconnect();
    if (idleHandle && typeof cancelIdleCallback === "function") cancelIdleCallback(idleHandle);
  };
}
