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
      notificationsCard(),
      watchListCard(),
      diagnosticsCard(status),
    );
  };

  load();
  const off = wl.onChange(() => { if (!disposed) load(); });
  return () => { disposed = true; off(); };
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
