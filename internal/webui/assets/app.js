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

onPad((ev) => {
  if (ev.type === "removed") return;
  const watching = wl.isWatched(ev.ref);
  const scope = wl.notifyScope();
  const onPadPage = location.hash === `#/pads/${ev.ref}`;

  // Reading the pad right now is its own notification.
  if (onPadPage) return;
  if (scope === "off") return;
  if (scope === "watched" && !watching) return;

  const who = ev.last_author || "someone";
  toast(`${ev.ref}: ${who} posted section ${ev.section_count}`, { type: "info" });
  notify.notify(ev);
});

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
