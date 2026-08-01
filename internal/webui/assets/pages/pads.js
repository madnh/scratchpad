// Pads — the sortable, filterable table of every pad (optionally one project's).
//
// Live updates land IN PLACE: a change repaints the affected row instead of
// re-fetching and re-sorting the whole table under the person's cursor.

import "/vendor/puredashboard/table.js";
import { toast } from "/vendor/puredashboard/toast.js";
import { confirm } from "/vendor/puredashboard/dialog.js";

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import * as wl from "/lib/watchlist.js";
import { el, pageHead, skeleton, errorView } from "/lib/ui.js";
import { relTime, absTime } from "/lib/fmt.js";

export default function mount(outlet, ctx) {
  return renderPadTable(outlet, { project: ctx?.params?.name || ctx?.query?.project || "" });
}

// renderPadTable is shared with the per-project page, which is the same table with a
// project fixed and a different heading.
export function renderPadTable(outlet, { project = "", heading, subtitle } = {}) {
  outlet.replaceChildren(skeleton(6));
  let disposed = false;
  let rows = [];

  const table = el("puredashboard-table");
  table.filterable = true;
  table.pageSize = 25;
  table.rowKey = (r) => r.ref;
  table.getHref = (r) => `#/pads/${r.ref}`;
  // Deletion is ONE pad at a time, deliberately: no row selection, no bulk action.
  // Wiping a batch of transcripts is irreversible and belongs to `pad purge`, where a
  // person types the age threshold and reads the list before confirming.
  table.actions = [{ name: "watch", label: "Watch" }, { name: "delete", label: "Delete", danger: true }];
  table.columns = [
    { key: "ref", label: "Ref", sortable: true, render: (r) => el("span", { class: "ref", text: r.ref }) },
    { key: "project", label: "Project", sortable: true },
    { key: "title", label: "Title", sortable: true },
    { key: "section_count", label: "Sections", sortable: true, align: "right" },
    { key: "last_author", label: "Last turn", sortable: true },
    {
      key: "last_ts", label: "Activity", sortable: true,
      render: (r) => el("span", { class: "nowrap", title: absTime(r.last_ts), text: relTime(r.last_ts) }),
    },
    { key: "flags", label: "", render: (r) => flags(r) },
  ];

  table.addEventListener("rowaction", async (e) => {
    const { name, row } = e.detail;
    if (name === "watch") {
      const on = !wl.isWatched(row.ref);
      wl.setWatched(row.ref, on);
      toast(on ? `Watching ${row.ref}` : `Stopped watching ${row.ref}`, { type: "info" });
      repaint();
      return;
    }
    if (name === "delete") await removePad(row);
  });

  async function removePad(pad) {
    if (!(await confirm(`Delete ${pad.ref}? This cannot be undone.`))) return;
    try {
      await api.deletePad(pad.ref);
      wl.setWatched(pad.ref, false);
      toast(`Deleted ${pad.ref}`, { type: "success" });
    } catch (err) {
      toast(`Could not delete ${pad.ref}: ${err.message}`, { type: "error" });
    }
    load();
  }

  function repaint() {
    table.rows = rows.map((r) => ({ ...r }));
  }

  async function load() {
    try {
      const data = await api.pads(project);
      if (disposed) return;
      rows = data.pads || [];
      outlet.replaceChildren(
        pageHead(heading || "Pads", subtitle ?? `${rows.length} pad${rows.length === 1 ? "" : "s"}`),
        table,
      );
      repaint();
      for (const w of data.warnings || []) toast(w, { type: "warn", duration: 8000 });
    } catch (err) {
      if (!disposed) outlet.replaceChildren(errorView(err, load));
    }
  }

  load();

  const off = onPad((ev) => {
    if (project && ev.project !== project) return;
    if (ev.type === "removed") {
      rows = rows.filter((r) => r.ref !== ev.ref);
      repaint();
      return;
    }
    const idx = rows.findIndex((r) => r.ref === ev.ref);
    const next = {
      ref: ev.ref, project: ev.project, title: ev.title,
      section_count: ev.section_count, last_author: ev.last_author,
      last_ts: ev.last_ts, protected: ev.protected,
    };
    if (idx < 0) rows.unshift(next); else rows[idx] = { ...rows[idx], ...next };
    repaint();
  });

  const offWatch = wl.onChange(repaint);

  return () => { disposed = true; off(); offWatch(); };
}

// flags renders the at-a-glance markers: protected, watched, and unread.
function flags(pad) {
  const box = el("span", { class: "flags" });
  if (pad.protected) box.append(el("puredashboard-tag", { color: "warning", size: "sm", text: "locked" }));
  if (wl.isWatched(pad.ref)) {
    const unread = pad.section_count > wl.seenCount(pad.ref);
    box.append(el("puredashboard-tag", {
      color: unread ? "accent" : "neutral", size: "sm",
      text: unread ? "new" : "watching",
    }));
  }
  return box;
}
