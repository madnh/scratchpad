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

// notify announces a pad change. The body is TURN-AWARE: in a turn-taking protocol
// "who moved, and who is it on now" is the whole point, so that is what the
// notification says rather than a bare "something changed".
export function notify(ev) {
  if (!supported() || Notification.permission !== "granted") return;

  const title = ev.last_title ? `${ev.ref} · ${ev.last_title}` : ev.ref;
  const who = ev.last_author || "someone";
  const body = `${who} posted section ${ev.section_count} — now waiting on anyone but ${who}`;

  try {
    // tag collapses repeats: a pad that gets three replies while the tab is hidden
    // leaves one current notification, not a stack of stale ones.
    const n = new Notification(title, { body, tag: `scratchpad:${ev.ref}`, renotify: true });
    n.onclick = () => {
      window.focus();
      location.hash = `#/pads/${ev.ref}`;
      n.close();
    };
  } catch { /* the browser may refuse in some contexts; the in-app toast still fires */ }
}
