// Overview — what is alive in the store right now.
//
// Everything here comes from pad METADATA, never from section bodies, so a
// password-protected pad appears in the activity feed at exactly the level
// `pad list` already publishes it.

import "/vendor/puredashboard/statistic.js";
import "/vendor/puredashboard/timeline.js";
import "/vendor/puredashboard/card.js";
import "/vendor/puredashboard/empty.js";

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import * as wl from "/lib/watchlist.js";
import { el, pageHead, skeleton, errorView, setChildren } from "/lib/ui.js";
import { relTime, absTime } from "/lib/fmt.js";

const RECENT = 15;

export default function mount(outlet) {
  outlet.replaceChildren(skeleton(5));
  let disposed = false;

  const render = async () => {
    let pads, projects, stuck = [];
    try {
      [{ pads }, { projects }] = await Promise.all([api.pads(), api.projects()]);
      // Best-effort: the stuck list is the most useful thing on this page, but it is
      // not worth the page failing over. It is a separate call because it walks every
      // pad's routing metadata, which the listing does not carry.
      ({ stuck } = await api.stuck().catch(() => ({ stuck: [] })));
    } catch (err) {
      if (!disposed) outlet.replaceChildren(errorView(err, render));
      return;
    }
    if (disposed) return;

    const dayAgo = Date.now() / 1000 - 86400;
    const activeToday = pads.filter((p) => p.last_ts >= dayAgo).length;
    const watching = pads.filter((p) => wl.isWatched(p.ref)).length;

    const stats = el("div", { class: "stat-row" },
      stat("Projects", projects.length),
      stat("Pads", pads.length),
      stat("Active today", activeToday),
      stat("Watching", watching),
    );

    const feed = el("puredashboard-timeline");
    feed.mode = "right";  // rail on the left, entries reading left-aligned
    feed.reverse = false; // the list is already newest-first
    feed.items = pads.slice(0, RECENT).map((p) => ({
      color: wl.isWatched(p.ref) ? "accent" : "info",
      label: relTime(p.last_ts),
      content: activityRow(p),
    }));

    const card = el("puredashboard-card", { title: "Recent activity" });
    card.append(pads.length ? feed : el("puredashboard-empty", { description: "No pads yet" }));

    setChildren(outlet,
      pageHead("Overview", "live view of the pad store"),
      stats,
      stuckBlock(stuck),
      card,
    );
  };

  render();

  // A change anywhere reshuffles the feed; metadata is cheap, so just refetch.
  const off = onPad(() => render());

  return () => { disposed = true; off(); };
}

// What stalled. This is the question a person opens the UI with, and it spans pads —
// answering it per-pad would mean opening every pad to find the one that is stuck,
// which is the work this page exists to save.
//
// It reports what has gone UNANSWERED, which is derivable, rather than who is currently
// listening, which is not: an append-only transcript cannot express presence, and an
// agent busy working looks identical to one that has died.
function stuckBlock(stuck) {
  if (!stuck?.length) return null;
  const card = el("puredashboard-card", {
    title: `Waiting on someone (${stuck.length})`,
  });
  const box = el("div", { class: "stuck" });
  for (const s of stuck.slice(0, 10)) {
    box.append(el("div", { class: "stuck__row" },
      el("a", { class: "stuck__what", href: `#/pads/${encodeURIComponent(s.ref)}`, text: s.what }),
      el("span", { class: "muted", text: `${s.from} → ${s.to}` }),
      el("span", { text: s.title || "" }),
      el("span", { class: "stuck__age", title: absTime(s.ts), text: relTime(s.ts) }),
    ));
  }
  if (stuck.length > 10) {
    box.append(el("p", { class: "muted", text: `…and ${stuck.length - 10} more` }));
  }
  card.append(box);
  return card;
}

function stat(title, value) {
  const s = el("puredashboard-statistic");
  s.title = title;
  s.value = value;
  return el("puredashboard-card", {}, s);
}

// activityRow is a DOM node rather than a string: the timeline accepts nodes, and
// building it this way keeps agent-written titles out of any HTML parser.
function activityRow(pad) {
  const row = el("div", {},
    el("a", { href: `#/pads/${pad.ref}`, class: "ref", text: pad.ref }),
    document.createTextNode(" "),
    el("span", { text: pad.title }),
  );
  const meta = el("div", { class: "muted", title: absTime(pad.last_ts) },
    `${pad.last_author} · section ${pad.section_count}${pad.protected ? " · protected" : ""}`,
  );
  row.append(meta);
  return row;
}
