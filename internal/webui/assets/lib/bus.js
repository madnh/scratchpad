// bus.js — ONE EventSource for the whole app, fanned out to whoever is listening.
//
// The connection belongs to the app shell, not to a page: opening it per page would
// drop and re-establish the stream on every navigation, which is exactly when a
// change is most likely to be missed. Pages subscribe on mount and unsubscribe in
// their cleanup; the stream itself never restarts.
//
// EventSource reconnects by itself, so there is no retry logic here — only a status
// signal so the header can say whether updates are flowing.

const padListeners = new Set();
const statusListeners = new Set();

let source = null;
let status = "connecting"; // connecting | live | offline

function setStatus(next) {
  if (status === next) return;
  status = next;
  for (const fn of statusListeners) fn(status);
}

// connect opens the stream. Called once, from the shell.
export function connect() {
  if (source) return;
  source = new EventSource("/api/events");

  source.addEventListener("open", () => setStatus("live"));
  source.addEventListener("error", () => {
    // readyState CONNECTING means the browser is already retrying on its own.
    setStatus(source.readyState === EventSource.CLOSED ? "offline" : "connecting");
  });
  source.addEventListener("pad", (e) => {
    let ev;
    try { ev = JSON.parse(e.data); } catch { return; }
    for (const fn of padListeners) {
      try { fn(ev); } catch (err) { console.error("pad listener failed", err); }
    }
  });
}

// onPad subscribes to pad change/removal events; the returned function unsubscribes.
export function onPad(fn) {
  padListeners.add(fn);
  return () => padListeners.delete(fn);
}

// onStatus subscribes to connection-status changes and fires once with the current one.
export function onStatus(fn) {
  statusListeners.add(fn);
  fn(status);
  return () => statusListeners.delete(fn);
}

export function connectionStatus() {
  return status;
}
