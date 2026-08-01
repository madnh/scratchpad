// sidebar.js — the sidebar is a PAD BROWSER, not just a menu.
//
// Four menu entries left most of a 220px column empty while the thing a person
// actually navigates by — which pad moved, and when — was a page away. So the
// sidebar carries the pad list itself, grouped, and stays in step with the live
// stream: every section that lands re-buckets its pad without a reload.
//
// Two groupings, because two questions get asked. "What just moved?" wants recency
// buckets (the default). "What is going on in project X?" wants project buckets —
// useful once a store has real projects, and deliberately not the default, since a
// store where everything sits in `default` would show one meaningless group.

import "/vendor/puredashboard/nav.js";
import "/vendor/puredashboard/segmented.js";

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import * as wl from "/lib/watchlist.js";
import { el } from "/lib/ui.js";
import { shortRel, absTime } from "/lib/fmt.js";

// SIDEBAR_LIMIT caps how much of the store the sidebar mirrors. It is a navigation
// aid, not the pads page: past this, "See all pads" is the honest answer.
const SIDEBAR_LIMIT = 40;

const MODE_KEY = "scratchpad.sidebarGroup";

// Recency buckets, newest first. Anchoring on the local start-of-day (rather than a
// rolling 24h) is what makes "Today" and "Yesterday" mean what a person expects.
function recencyBuckets(now = new Date()) {
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime() / 1000;
  return [
    { label: "Today", from: startOfToday },
    { label: "Yesterday", from: startOfToday - 86400 },
    { label: "Earlier this week", from: startOfToday - 6 * 86400 },
    { label: "This month", from: startOfToday - 29 * 86400 },
    { label: "Older", from: -Infinity },
  ];
}

function groupByRecency(pads) {
  const buckets = recencyBuckets();
  const out = buckets.map((b) => ({ label: b.label, pads: [] }));
  for (const pad of pads) {
    const idx = buckets.findIndex((b) => pad.last_ts >= b.from);
    out[idx < 0 ? out.length - 1 : idx].pads.push(pad);
  }
  return out.filter((g) => g.pads.length);
}

function groupByProject(pads) {
  const groups = new Map();
  for (const pad of pads) {
    if (!groups.has(pad.project)) groups.set(pad.project, []);
    groups.get(pad.project).push(pad);
  }
  // Projects ordered by their most recent activity — pads already arrive newest first.
  return [...groups.entries()].map(([label, ps]) => ({ label, pads: ps, href: `#/projects/${encodeURIComponent(label)}` }));
}

// padLabel is a DOM node, not a string: two lines fit a 220px column and turn a bare
// list of titles into something scannable — which pad, whose turn, how long ago.
function padLabel(pad) {
  const unread = wl.isWatched(pad.ref) && pad.section_count > wl.seenCount(pad.ref);
  return el("span", { class: "padnav", title: `${pad.ref}\n${pad.title}\nlast activity ${absTime(pad.last_ts)}` },
    el("span", { class: "padnav__row" },
      unread ? el("span", { class: "padnav__dot", "aria-label": "unread" }) : null,
      el("span", { class: "padnav__title", text: pad.title || pad.ref }),
    ),
    // "locked" as a word on the meta line, not a glyph beside the title: a second
    // coloured dot next to the unread dot reads as a second unread state.
    el("span", {
      class: "padnav__meta",
      text: `${pad.ref} · ${shortRel(pad.last_ts)}${pad.protected ? " · locked" : ""}`,
    }),
  );
}

export function initSidebar() {
  const nav = document.getElementById("nav");
  const navBottom = document.getElementById("nav-bottom");
  const modeBox = document.getElementById("group-mode");
  const list = document.getElementById("padlist");
  const footer = document.getElementById("padlist-footer");

  let pads = [];
  let mode = localStorage.getItem(MODE_KEY) === "project" ? "project" : "recent";

  // ── section menu ───────────────────────────────────────────────────────────
  nav.items = [
    { label: "Overview", href: "#/" },
    { label: "Pads", href: "#/pads" },
    { label: "Projects", href: "#/projects" },
  ];
  navBottom.items = [{ label: "Settings", href: "#/settings" }];

  const mark = () => {
    const here = location.hash || "#/";
    nav.current = here;
    navBottom.current = here;
    for (const n of list.querySelectorAll("puredashboard-nav")) n.current = here;
  };

  // ── grouping switch ────────────────────────────────────────────────────────
  const seg = el("puredashboard-segmented", { size: "sm" });
  seg.setAttribute("block", ""); // attribute form: the component reflects it to layout
  seg.options = [{ value: "recent", label: "Recent" }, { value: "project", label: "Project" }];
  seg.value = mode;
  seg.addEventListener("change", (e) => {
    mode = e.target.value;
    localStorage.setItem(MODE_KEY, mode);
    renderList();
  });
  modeBox.replaceChildren(seg);

  // ── the list ───────────────────────────────────────────────────────────────
  function renderList() {
    const shown = pads.slice(0, SIDEBAR_LIMIT);
    const groups = mode === "project" ? groupByProject(shown) : groupByRecency(shown);

    list.replaceChildren(...groups.map((g) => {
      // The group heading is plain markup rather than a nav group: a nav group only
      // opens by default when it holds the current item, which would leave a fresh
      // sidebar collapsed and empty — the opposite of the point.
      const head = el("div", { class: "padgroup__head" },
        g.href
          ? el("a", { class: "padgroup__label", href: g.href, text: g.label })
          : el("span", { class: "padgroup__label", text: g.label }),
        el("span", { class: "padgroup__count", text: String(g.pads.length) }),
      );
      const groupNav = el("puredashboard-nav");
      groupNav.items = g.pads.map((pad) => ({
        label: padLabel(pad),
        href: `#/pads/${pad.ref}`,
        badge: String(pad.section_count),
      }));
      return el("div", { class: "padgroup" }, head, groupNav);
    }));

    if (!groups.length) {
      list.replaceChildren(el("p", { class: "padlist__empty", text: "No pads yet." }));
    }
    // The pads table is worth a link even when nothing is truncated — it is where
    // sorting, filtering and bulk actions live.
    footer.textContent = shown.length < pads.length
      ? `Showing ${shown.length} of ${pads.length} — see all →`
      : "Open pads table →";
    mark();
  }

  async function refresh() {
    try {
      const data = await api.pads();
      pads = data.pads || [];
    } catch {
      pads = [];
    }
    renderList();
  }

  // Live changes arrive one section at a time; coalesce so a burst of posts costs one
  // refetch rather than one per event.
  let pending = null;
  const refreshSoon = () => {
    clearTimeout(pending);
    pending = setTimeout(refresh, 250);
  };

  onPad(refreshSoon);
  wl.onChange(renderList);          // unread dots follow the watch list
  window.addEventListener("hashchange", mark);

  // Relative times go stale on a page nobody touches; a minute is close enough.
  setInterval(renderList, 60_000);

  refresh();
  return { refresh };
}
