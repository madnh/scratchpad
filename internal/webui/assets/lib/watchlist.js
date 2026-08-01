// watchlist.js — which pads this person is watching, and how much of each they have
// already seen.
//
// This lives in localStorage, NOT in the Scratchpad dir: it is one person's attention
// on one machine, not deployment configuration. A second browser, or a colleague on
// the same store, keeps their own.

const WATCH_KEY = "scratchpad.watch";
const SEEN_KEY = "scratchpad.seen";
const SCOPE_KEY = "scratchpad.notifyScope"; // "watched" | "all" | "off"

const listeners = new Set();

function read(key, fallback) {
  try {
    const raw = localStorage.getItem(key);
    return raw ? JSON.parse(raw) : fallback;
  } catch {
    return fallback;
  }
}

function write(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* private mode */ }
  for (const fn of listeners) fn();
}

export function watched() {
  return new Set(read(WATCH_KEY, []));
}

export function isWatched(ref) {
  return watched().has(ref);
}

export function setWatched(ref, on) {
  const set = watched();
  if (on) set.add(ref); else set.delete(ref);
  write(WATCH_KEY, [...set]);
}

// seenCount is how many sections of a pad this person has already looked at — the
// basis for the "unread" dot, and for deciding whether a change is worth announcing.
export function seenCount(ref) {
  return read(SEEN_KEY, {})[ref] || 0;
}

export function markSeen(ref, count) {
  const all = read(SEEN_KEY, {});
  if (all[ref] === count) return;
  all[ref] = count;
  write(SEEN_KEY, all);
}

export function notifyScope() {
  return read(SCOPE_KEY, "watched");
}

export function setNotifyScope(scope) {
  write(SCOPE_KEY, scope);
}

// onChange fires whenever the watch list or seen counters change, so the sidebar and
// any open page stay in step.
export function onChange(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
