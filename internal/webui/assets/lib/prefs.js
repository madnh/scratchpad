// prefs.js — how this reader wants the UI to behave.
//
// These describe a PERSON, not a pad and not the deployment: which end of a transcript
// they read from, and whether the toolbar should follow them down a long pad. So they
// live in localStorage rather than in the URL (a link should open the same pad for
// whoever you send it to) and rather than on the server (the operator does not choose
// how each reader reads).
//
// Every getter tolerates storage being unavailable — private windows and locked-down
// browsers throw on access — by falling back to the default. The UI then still works,
// it just forgets the choice when the tab closes.

const KEYS = {
  order: "scratchpad.ui.order",
  stickyBar: "scratchpad.ui.stickyBar",
  outline: "scratchpad.ui.outline",
};

const DEFAULTS = {
  // Opening a pad to see what just happened is the common visit.
  order: "newest",
  // A pad runs to hundreds of sections; controls that scroll away are controls you
  // have to scroll back for.
  stickyBar: true,
  // The rail is where you find out how long the conversation is and where you are in
  // it — worth its width by default. A narrow window hides it regardless; that is a
  // fact about the viewport and is deliberately NOT written back here, or one resize
  // would erase the choice.
  outline: true,
};

function read(key) {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function write(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch { /* the session still works, it just will not be remembered */ }
}

// Changes have to reach a pad page that is already open — Settings is a different
// route, so nothing re-renders on its own.
const listeners = new Set();

function announce(name, value) {
  for (const fn of [...listeners]) {
    try { fn(name, value); } catch { /* one bad listener must not stop the rest */ }
  }
}

/** onChange registers a listener; returns the function that removes it. */
export function onChange(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** order is "newest" (default) or "oldest" — which end of the pad to read from. */
export function order() {
  return read(KEYS.order) === "oldest" ? "oldest" : DEFAULTS.order;
}

export function setOrder(value) {
  const v = value === "oldest" ? "oldest" : "newest";
  write(KEYS.order, v);
  announce("order", v);
}

/** stickyBar keeps the pad toolbar pinned to the top while scrolling. */
export function stickyBar() {
  const raw = read(KEYS.stickyBar);
  return raw === null ? DEFAULTS.stickyBar : raw !== "off";
}

export function setStickyBar(on) {
  write(KEYS.stickyBar, on ? "on" : "off");
  announce("stickyBar", !!on);
}

/** outline shows the pad's section index beside the transcript. */
export function outline() {
  const raw = read(KEYS.outline);
  return raw === null ? DEFAULTS.outline : raw !== "off";
}

export function setOutline(on) {
  write(KEYS.outline, on ? "on" : "off");
  announce("outline", !!on);
}
