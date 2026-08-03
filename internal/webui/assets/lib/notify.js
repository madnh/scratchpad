// notify.js — browser notifications for pad activity.
//
// The Notification API needs a secure context, and http://127.0.0.1 qualifies: the
// spec treats loopback as a potentially-trustworthy origin, so this works over plain
// HTTP with no certificate anywhere.
//
// The honest limit, stated in Settings rather than discovered: notifications only fire
// while a tab is open (backgrounded is fine, closed is not). Nothing here can survive
// the browser being shut down — that would need a push service, which a local,
// single-user tool has no business talking to.

export function supported() {
  return typeof Notification !== "undefined";
}

export function permission() {
  return supported() ? Notification.permission : "unsupported";
}

// request must be called from a user gesture — browsers reject a permission prompt
// that no one asked for.
export async function request() {
  if (!supported()) return "unsupported";
  if (Notification.permission !== "default") return Notification.permission;
  try { return await Notification.requestPermission(); } catch { return Notification.permission; }
}

// eventBody says what actually happened, in the vocabulary of the stream it happened
// in. A MESSAGE is turn-taking, so "who moved, and who is it on now" is the whole
// point. A TASK event takes no turn — saying "now waiting on anyone but ios" about a
// status report would be wrong — so it reports the move instead.
function eventBody(ev) {
  const who = ev.last_author || "someone";
  if (ev.last_kind === "task" && ev.last_task) {
    const move = ev.last_status ? ` → ${ev.last_status}` : "";
    return `${who} moved T${ev.last_task}${move} · §${ev.section_count}`;
  }
  return `${who} posted section ${ev.section_count} — now waiting on anyone but ${who}`;
}

// notify announces a pad change.
export function notify(ev) {
  if (!supported() || Notification.permission !== "granted") return;
  const title = ev.last_title ? `${ev.ref} · ${ev.last_title}` : ev.ref;
  // tag collapses repeats: a pad that gets three replies while the tab is hidden
  // leaves one current notification, not a stack of stale ones.
  show(title, eventBody(ev), `scratchpad:${ev.ref}`, ev.ref);
}

// notifyStuck announces an assignment that has gone unanswered past the threshold.
//
// It is a separate entry point because it is a separate KIND of news: every other
// notification here reports something that happened, and this one reports something
// that did not. Its tag is per assignment, so a second overdue task does not silently
// replace the first the way two posts to one pad do.
export function notifyStuck(s) {
  if (!supported() || Notification.permission !== "granted") return;
  const title = `${s.ref} · ${s.what} unanswered`;
  const body = s.title
    ? `${s.from} → ${s.to}: ${s.title}`
    : `${s.from} addressed ${s.to}, who has not answered`;
  show(title, body, `scratchpad:stuck:${s.ref}:${s.what}:${s.to}`, s.ref);
}

function show(title, body, tag, ref) {
  try {
    const n = new Notification(title, { body, tag, renotify: true });
    n.onclick = () => {
      window.focus();
      location.hash = `#/pads/${ref}`;
      n.close();
    };
  } catch { /* the browser may refuse in some contexts; the in-app toast still fires */ }
}
