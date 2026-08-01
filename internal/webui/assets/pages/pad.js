// Pad detail — reading one conversation.
//
// Two constraints shape this page. A pad is a TURN-TAKING transcript, so it reads as
// a timeline rather than a document. And a pad grows without bound — hundreds of
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

import "/vendor/puredashboard/timeline.js";
import "/vendor/puredashboard/md.js";
import "/vendor/puredashboard/alert.js";
import "/vendor/puredashboard/switch.js";
import "/vendor/puredashboard/tag.js";
import "/vendor/puredashboard/input.js";
import "/vendor/puredashboard/select.js";
import "/vendor/puredashboard/result.js";
import { toast } from "/vendor/puredashboard/toast.js";
import { confirm } from "/vendor/puredashboard/dialog.js";

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import * as wl from "/lib/watchlist.js";
import { el, pageHead, skeleton, errorView, copyButton } from "/lib/ui.js";
import { relTime, absTime, clockTime, bytes, authorColor } from "/lib/fmt.js";

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
  // timeline renders on a microtask, so the marker is applied while BUILDING each
  // node rather than by querying the DOM after render().
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
    await loadLatest();
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
    if (target) target.scrollIntoView({ block: "center", behavior: "smooth" });
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

    const timeline = el("puredashboard-timeline");
    // "right" puts the rail on the left and the prose to the right of it, reading
    // left-aligned like a transcript; "left" mirrors that and right-aligns the body,
    // which is unreadable for long agent prose.
    timeline.mode = "right";
    // The API hands back ascending sections; reverse for a newest-first read.
    timeline.items = [...visible].reverse().map((sec) => ({
      color: authorColor(sec.author, authors),
      // The section number lives in the header line below; the rail label carries
      // only the clock, so the two do not repeat each other.
      label: clockTime(sec.ts),
      content: sectionNode(sec),
    }));
    body.append(timeline);

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
      pageHead(pad.title || ref, null, copyButton(ref)),
      metaRow(),
      turnBanner(),
      toolbar(authors),
      body,
      dangerZone(),
    );
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

  // turnBanner states the rule the pad actually enforces: the last author is blocked,
  // anyone else may post. That is the single most useful line on the page.
  function turnBanner() {
    const last = pad.turn?.last_author || "—";
    const a = el("puredashboard-alert");
    a.type = "info";
    a.showIcon = true;
    a.title = `Turn: waiting on anyone but ${last}`;
    a.message = `${last} posted section ${pad.section_count} ${relTime(pad.sections.at(-1)?.ts)}.`;
    return a;
  }

  function toolbar(authors) {
    const range = loaded.length
      ? `showing #${loaded[0].n}–#${loaded.at(-1).n} of ${pad.section_count}`
      : `${pad.section_count} sections`;

    const filter = el("puredashboard-select", { placeholder: "All authors" });
    filter.options = [{ value: "", label: "All authors" },
      ...authors.map((a) => ({ value: a, label: a }))];
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

  // sectionNode builds one entry: a compact header line, then the body — clamped when
  // it is long, so a 5000-word section does not bury the sections around it.
  function sectionNode(sec) {
    const long = sec.content.length > CLAMP_BYTES;
    const clamped = long && !expandAll && sec.n !== pad.section_count;

    const md = el("puredashboard-markdown", { class: "sec__content" });
    md.value = sec.content;

    const bodyBox = el("div", { class: "sec__body", dataset: { clamped: String(clamped) } }, md);

    const wrap = el("div", {
      dataset: { section: String(sec.n) },
      class: justArrived.has(sec.n) ? "sec--new" : null,
    },
      el("div", { class: "sec__head" },
        el("span", { class: "sec__n", text: `#${sec.n}` }),
        el("span", { class: "sec__author", text: sec.author }),
        el("span", { class: "sec__title", text: sec.title }),
        el("span", { text: bytes(sec.content.length) }),
      ),
      bodyBox,
    );

    if (long) {
      const toggle = el("button", {
        type: "button", class: "sec__expand",
        text: clamped ? "Expand" : "Collapse",
      });
      toggle.addEventListener("click", () => {
        const nowClamped = bodyBox.dataset.clamped !== "true";
        bodyBox.dataset.clamped = String(nowClamped);
        toggle.textContent = nowClamped ? "Expand" : "Collapse";
      });
      wrap.append(toggle);
    }
    return wrap;
  }

  // The pad's page is the ONLY place a pad can be deleted. A destructive action
  // belongs where the thing it destroys is on screen — the title, the participants and
  // how much history there is — not behind a button in a list that reorders itself.
  function dangerZone() {
    return el("div", { class: "danger-zone" },
      el("p", { class: "muted", text: "Deleting removes the pad file and its whole history. There is no undo." }),
      el("button", {
        type: "button", class: "ghost-btn danger-btn", text: "Delete this pad",
        onclick: async () => {
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
        },
      }),
    );
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

  return () => { disposed = true; off(); };
}
