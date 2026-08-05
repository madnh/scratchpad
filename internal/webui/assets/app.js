// app.js — boots the app shell.
//
// The shell (header, sidebar, live stream) is created ONCE here; the router only ever
// swaps #view. That is what keeps the SSE connection, the notification permission and
// the sidebar state alive across navigation.

import { Router } from "/vendor/puredashboard/router.js";
import { toast } from "/vendor/puredashboard/toast.js";
import "/vendor/puredashboard/layout.js";
import "/vendor/puredashboard/skeleton.js";

import { api } from "/lib/api.js";
import { connect, onPad, onStatus } from "/lib/bus.js";
import { initSidebar } from "/lib/sidebar.js";
import * as notify from "/lib/notify.js";
import * as wl from "/lib/watchlist.js";

const conn = document.getElementById("conn");
const themeBtn = document.getElementById("theme-btn");
const notifyBtn = document.getElementById("notify-btn");

// ── Theme ────────────────────────────────────────────────────────────────────
// Three states, not two: "auto" follows the OS, and is the default.
const THEME_KEY = "scratchpad.theme";
function applyTheme(mode) {
  if (mode === "auto") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", mode);
  // Words, not glyphs: the moon/sun symbols are missing from enough system fonts to
  // render as tofu, and a theme switch nobody can read is worse than a wide button.
  themeBtn.textContent = mode === "auto" ? "Auto" : mode === "light" ? "Light" : "Dark";
  themeBtn.title = `Theme: ${mode} (click to change)`;
}
applyTheme(localStorage.getItem(THEME_KEY) || "auto");
themeBtn.addEventListener("click", () => {
  const order = ["auto", "light", "dark"];
  const next = order[(order.indexOf(localStorage.getItem(THEME_KEY) || "auto") + 1) % order.length];
  localStorage.setItem(THEME_KEY, next);
  applyTheme(next);
});

// ── Connection indicator ─────────────────────────────────────────────────────
onStatus((status) => {
  conn.dataset.status = status;
  conn.querySelector(".conn__text").textContent =
    status === "live" ? "live" : status === "offline" ? "disconnected" : "connecting…";
});

// ── Notification permission ──────────────────────────────────────────────────
function syncNotifyButton() {
  const state = notify.permission();
  notifyBtn.hidden = state !== "default";
}
syncNotifyButton();
notifyBtn.addEventListener("click", async () => {
  await notify.request();
  syncNotifyButton();
});

// ── Brand ────────────────────────────────────────────────────────────────────
api.status()
  .then((s) => {
    document.getElementById("brand-name").textContent = s.display_name || "Scratchpad";
    document.title = `${s.display_name || "Scratchpad"} — pads`;
    // The running binary's version in the footer: the first thing worth knowing when
    // reporting something from this UI.
    if (s.version) document.getElementById("foot-version").textContent = s.version;
  })
  .catch(() => { /* the pages surface their own errors */ });

// ── Live events ──────────────────────────────────────────────────────────────
// The shell handles the ANNOUNCEMENT side (toast + OS notification + sidebar counts).
// Pages subscribe separately for their own in-place updates.
connect();
initSidebar();

// The filter is the person's `wake_for`: scope decides which pads may speak, and this
// decides what is worth interrupting them about. "overdue" never matches an event —
// being overdue is the absence of one — so it is handled by the sweep below and every
// arriving section is silent while it is selected.
//
// A protected pad publishes nothing beyond the listing level, so it carries no kind and
// no task number: the task filters cannot match it, and it stays quiet rather than
// leaking that a task moved. That is the event stream's boundary, not a special case
// here.
function passesFilter(ev) {
  switch (wl.notifyFilter()) {
    case "tasks": return ev.last_kind === "task";
    case "task": return ev.last_kind === "task" && ev.last_task === wl.notifyTask();
    case "overdue": return false;
    default: return true;
  }
}

function announces(ref) {
  // Reading the pad right now is its own notification.
  if (location.hash === `#/pads/${ref}`) return false;
  const scope = wl.notifyScope();
  if (scope === "off") return false;
  if (scope === "watched" && !wl.isWatched(ref)) return false;
  return true;
}

onPad((ev) => {
  if (ev.type === "removed") return;
  if (!announces(ev.ref) || !passesFilter(ev)) return;

  const who = ev.last_author || "someone";
  const what = ev.last_kind === "task" && ev.last_task
    ? `${who} moved T${ev.last_task}${ev.last_status ? ` → ${ev.last_status}` : ""}`
    : `${who} posted section ${ev.section_count}`;
  toast(`${ev.ref}: ${what}`, { type: "info" });
  notify.notify(ev);
});

// ── Overdue sweep ────────────────────────────────────────────────────────────
// "Only when something is overdue" is the one filter that cannot ride on pad events:
// what makes an assignment overdue is that NOTHING arrived, so gating the event stream
// would produce a setting that never fires. It polls /api/stuck instead — the same
// derivation the overview shows — and announces an assignment the first time it
// crosses the threshold.
//
// The first sweep only records: whatever was already stuck when this tab opened is a
// backlog, not news, and announcing it would train the person to dismiss the one
// notification that means something.
const STUCK_POLL_MS = 60_000;
let stuckTimer = null;
let stuckSeen = null;

const stuckKey = (s) => `${s.ref}${s.what}${s.to}${s.section}`;

async function sweepStuck() {
  let stuck;
  try {
    ({ stuck = [] } = await api.stuck());
  } catch {
    return; // offline or asleep: the next tick retries, and nothing is marked as seen
  }
  const keys = new Set(stuck.map(stuckKey));
  if (stuckSeen === null) {
    stuckSeen = keys;
    return;
  }
  for (const s of stuck) {
    if (stuckSeen.has(stuckKey(s)) || !announces(s.ref)) continue;
    // Longer than the default four seconds, because this is announced exactly ONCE:
    // a post that slips by is repeated by the next post, and an assignment nobody
    // answers has nothing to repeat it. Still self-clearing, so a morning's worth of
    // them cannot bury the page the way sticky toasts would.
    toast(`${s.ref}: ${s.what} unanswered by ${s.to}`, { type: "warn", duration: 20000 });
    notify.notifyStuck(s);
  }
  stuckSeen = keys;
}

// Started and stopped by the preference itself, so a person who is not using the
// filter pays for no polling at all. onChange fires on every watch-list write too,
// hence the comparison: this must be idempotent, not a timer reset per keystroke.
function syncStuckSweep() {
  const wanted = wl.notifyFilter() === "overdue" && wl.notifyScope() !== "off";
  if (wanted === (stuckTimer !== null)) return;
  if (wanted) {
    stuckSeen = null;
    sweepStuck();
    stuckTimer = setInterval(sweepStuck, STUCK_POLL_MS);
  } else {
    clearInterval(stuckTimer);
    stuckTimer = null;
    stuckSeen = null;
  }
}
syncStuckSweep();
wl.onChange(syncStuckSweep);

// ── Router ───────────────────────────────────────────────────────────────────
// Hash mode: the UI is served from a binary at "/", so there is no server-side
// rewrite to configure, and the router reacts to hashchange instead of hijacking
// clicks — ⌘-click, open-in-new-tab and copy-link keep working on every link.
const router = new Router({
  outlet: "#view",
  appName: "Scratchpad",
  mode: "hash",
  routes: {
    "/": { title: "Overview", load: () => import("/pages/overview.js") },
    "/pads": { title: "Pads", load: () => import("/pages/pads.js") },
    // The page narrows this to the query itself once it mounts — the router's title hook
    // is handed the path PARAMS only, and the question here lives in the query string.
    // Router order makes that safe: it writes the title before mounting, so the page's
    // own write is the one that stands.
    "/search": { title: "Search", load: () => import("/pages/search.js") },
    "/pads/:ref": { title: (p) => p.ref, load: () => import("/pages/pad.js") },
    "/projects": { title: "Projects", load: () => import("/pages/projects.js") },
    "/projects/:name": { title: (p) => `Project ${p.name}`, load: () => import("/pages/project.js") },
    "/settings": { title: "Settings", load: () => import("/pages/settings.js") },
    "*": { title: "Not found", load: () => import("/pages/404.js") },
  },
  onError: (err) => {
    console.error(err);
    toast(`Could not open that page: ${err.message}`, { type: "error" });
  },
});

router.start();
