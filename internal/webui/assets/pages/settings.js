// Settings — notification behaviour, appearance, the watch list, and what this UI is
// actually looking at.
//
// The diagnostics block is deliberate: "why am I not getting updates?" should be
// answerable here rather than by reading the server's log.

import "/vendor/puredashboard/card.js";
import "/vendor/puredashboard/segmented.js";
import "/vendor/puredashboard/descriptions.js";
import "/vendor/puredashboard/alert.js";
import "/vendor/puredashboard/empty.js";
import { toast } from "/vendor/puredashboard/toast.js";

import { api } from "/lib/api.js";
import { connectionStatus } from "/lib/bus.js";
import * as notify from "/lib/notify.js";
import * as wl from "/lib/watchlist.js";
import * as prefs from "/lib/prefs.js";
import { el, pageHead, skeleton, errorView } from "/lib/ui.js";

export default function mount(outlet) {
  outlet.replaceChildren(skeleton(5));
  let disposed = false;

  const load = async () => {
    let status;
    try {
      status = await api.status();
    } catch (err) {
      if (!disposed) outlet.replaceChildren(errorView(err, load));
      return;
    }
    if (disposed) return;
    outlet.replaceChildren(
      pageHead("Settings"),
      readingCard(),
      notificationsCard(),
      watchListCard(),
      diagnosticsCard(status),
    );
  };

  load();
  const off = wl.onChange(() => { if (!disposed) load(); });
  return () => { disposed = true; off(); };
}

// readingCard holds the choices about how a pad READS. They are per person and stay in
// this browser — the pad itself has no opinion about which end you start from, and a
// link you send someone must open the same way for them as it does for you.
function readingCard() {
  const card = el("puredashboard-card", { title: "Reading" });

  const order = el("puredashboard-segmented");
  order.options = [
    { value: "newest", label: "Newest first" },
    { value: "oldest", label: "Oldest first" },
  ];
  order.value = prefs.order();
  order.addEventListener("change", (e) => {
    prefs.setOrder(e.target.value);
    toast(e.target.value === "newest"
      ? "Pads open at the newest section"
      : "Pads read from the first section", { type: "info" });
  });

  const sticky = el("puredashboard-segmented");
  sticky.options = [
    { value: "on", label: "Follow the page" },
    { value: "off", label: "Stay at the top" },
  ];
  sticky.value = prefs.stickyBar() ? "on" : "off";
  sticky.addEventListener("change", (e) => {
    prefs.setStickyBar(e.target.value === "on");
    toast(e.target.value === "on"
      ? "The pad toolbar follows you down the page"
      : "The pad toolbar stays at the top of the page", { type: "info" });
  });

  const outline = el("puredashboard-segmented");
  outline.options = [
    { value: "on", label: "Show the outline" },
    { value: "off", label: "Transcript only" },
  ];
  outline.value = prefs.outline() ? "on" : "off";
  outline.addEventListener("change", (e) => {
    prefs.setOutline(e.target.value === "on");
    toast(e.target.value === "on"
      ? "Pads show their section outline"
      : "Pads open without the outline", { type: "info" });
  });

  card.append(
    el("p", { class: "muted", text: "Which end of a pad to start reading from:" }),
    order,
    el("p", {
      class: "muted",
      text: "A pad's own page has the same switch, next to “Expand all” — changing it either " +
        "way changes it everywhere.",
    }),
    el("p", { class: "muted", text: "The toolbar that filters, jumps and reorders:" }),
    sticky,
    el("p", {
      class: "muted",
      text: "Following the page keeps those controls reachable in a pad hundreds of sections " +
        "long, and carries the pad's name and actions once its header has scrolled away.",
    }),
    el("p", { class: "muted", text: "The index of sections beside a pad:" }),
    outline,
    el("p", {
      class: "muted",
      text: "The outline lists every section of the pad, not just the ones loaded, and marks " +
        "where you are. A narrow window hides it whatever this says — the pad's toolbar has " +
        "an “Outline” button to bring it back.",
    }),
  );
  return card;
}

function notificationsCard() {
  const card = el("puredashboard-card", { title: "Notifications" });
  const state = notify.permission();

  if (state === "unsupported") {
    const a = el("puredashboard-alert");
    a.type = "warning";
    a.message = "This browser does not support notifications. In-app toasts still work.";
    card.append(a);
  } else if (state === "denied") {
    const a = el("puredashboard-alert");
    a.type = "warning";
    a.title = "Notifications are blocked";
    a.message = "Re-allow them for this site in the browser's site settings; the page cannot ask again once denied.";
    card.append(a);
  } else if (state === "default") {
    card.append(el("button", {
      type: "button", text: "Enable browser notifications",
      onclick: async () => {
        const result = await notify.request();
        toast(result === "granted" ? "Notifications enabled" : `Notifications ${result}`,
          { type: result === "granted" ? "success" : "warn" });
      },
    }));
  } else {
    const a = el("puredashboard-alert");
    a.type = "success";
    a.message = "Notifications are enabled.";
    card.append(a);
  }

  const scope = el("puredashboard-segmented");
  scope.options = [
    { value: "watched", label: "Watched pads" },
    { value: "all", label: "All pads" },
    { value: "off", label: "Off" },
  ];
  scope.value = wl.notifyScope();
  scope.addEventListener("change", (e) => {
    wl.setNotifyScope(e.target.value);
    toast(`Notifying for: ${e.target.value}`, { type: "info" });
  });

  card.append(
    el("p", { class: "muted", text: "Which changes are announced:" }),
    scope,
    // Stated up front rather than discovered: nothing here can outlive the tab.
    el("p", {
      class: "muted",
      text: "Notifications arrive while a tab of this UI is open — a background tab is fine, " +
        "a closed browser is not. Keep a pinned tab if you want to be told while working elsewhere.",
    }),
  );
  return card;
}

function watchListCard() {
  const card = el("puredashboard-card", { title: "Watched pads" });
  const refs = [...wl.watched()];
  if (!refs.length) {
    card.append(el("puredashboard-empty", {
      compact: true,
      description: "Not watching any pad. Turn on Watch from a pad's page or the pads table.",
    }));
    return card;
  }
  const list = el("div");
  for (const ref of refs) {
    list.append(el("div", { class: "pad__meta" },
      el("a", { href: `#/pads/${ref}`, class: "ref", text: ref }),
      el("button", {
        type: "button", class: "ghost-btn", text: "Stop watching",
        onclick: () => { wl.setWatched(ref, false); toast(`Stopped watching ${ref}`, { type: "info" }); },
      }),
    ));
  }
  card.append(list);
  return card;
}

function diagnosticsCard(status) {
  const card = el("puredashboard-card", { title: "This instance" });

  const d = el("puredashboard-descriptions");
  d.columns = 1;
  d.items = [
    { label: "Deployment", value: status.display_name },
    { label: "Store", value: status.projects_dir },
    { label: "Version", value: status.version },
    { label: "Surface", value: status.read_only ? "read-only (posting is an agent surface)" : "read-write" },
    { label: "Live stream", value: connectionStatus() },
    {
      label: "Change detection",
      value: status.watcher === "push"
        ? "kernel filesystem events (instant)"
        : "periodic rescan — filesystem notification is unavailable here, so changes appear within 30s",
    },
  ];
  card.append(d);

  if (status.watcher !== "push") {
    const a = el("puredashboard-alert");
    a.type = "warning";
    a.title = "Running on the rescan fallback";
    a.message = "Updates still arrive, just later. This usually means the store is on a filesystem " +
      "that does not support change notification (some network or container filesystems).";
    card.append(a);
  }
  return card;
}
