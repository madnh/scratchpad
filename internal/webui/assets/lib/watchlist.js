// watchlist.js — which pads this person is watching, and how much of each they have
// already seen.
//
// This lives in localStorage, NOT in the Scratchpad dir: it is one person's attention
// on one machine, not deployment configuration. A second browser, or a colleague on
// the same store, keeps their own.

const WATCH_KEY = "scratchpad.watch";
const SEEN_KEY = "scratchpad.seen";
const SCOPE_KEY = "scratchpad.notifyScope"; // "watched" | "all" | "off"
const FILTER_KEY = "scratchpad.notifyFilter"; // "any" | "tasks" | "task" | "overdue"
const TASK_KEY = "scratchpad.notifyTask"; // the task number "task" watches

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

// The notification filter answers the question `wake_for` answers for an agent — what
// should INTERRUPT you — and deliberately uses the same vocabulary, because it is the
// same problem: with five agents in a pad, most of what arrives belongs to two of them.
// Scope says which pads may speak; the filter says what is worth being told about.
//
//   any      every section          (the default, and what this UI has always done)
//   tasks    task events only       — `wake_for: tasks`
//   task     one task's events      — `wake_for: task:<n>`
//   overdue  only when something has gone unanswered — `--unacked`
export function notifyFilter() {
  const v = read(FILTER_KEY, "any");
  return ["any", "tasks", "task", "overdue"].includes(v) ? v : "any";
}

export function setNotifyFilter(filter) {
  write(FILTER_KEY, filter);
}

// The task the "task" filter watches. Task numbers are per pad, so this is only
// unambiguous when the scope narrows to pads you watch — which Settings says plainly
// rather than pretending the number is global.
export function notifyTask() {
  const n = Number(read(TASK_KEY, 0));
  return Number.isInteger(n) && n > 0 ? n : 0;
}

export function setNotifyTask(n) {
  write(TASK_KEY, Number.isInteger(n) && n > 0 ? n : 0);
}

// onChange fires whenever the watch list or seen counters change, so the sidebar and
// any open page stay in step.
export function onChange(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
