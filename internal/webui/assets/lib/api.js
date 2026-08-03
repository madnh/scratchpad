// api.js — the JSON surface of the UI server.
//
// Every response error carries the SAME stable code vocabulary the CLI and the MCP
// tools use (pad_not_found, unauthorized, invalid_ref, …), so callers branch on
// `err.code` and never on prose.

export class ApiError extends Error {
  constructor(code, message, status) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

async function request(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    ...opts,
    headers: { ...(opts.body ? { "Content-Type": "application/json" } : {}), ...(opts.headers || {}) },
  });
  const text = await res.text();
  let body = null;
  if (text) {
    try { body = JSON.parse(text); } catch { /* non-JSON error page */ }
  }
  if (!res.ok) {
    const code = body?.code || String(res.status);
    const msg = body?.error || res.statusText || "request failed";
    throw new ApiError(code, msg, res.status);
  }
  return body;
}

// projectSegment refuses a project name that is not one before it becomes a URL path
// segment. `encodeURIComponent` leaves "." and ".." intact, and a path containing them is
// normalised by the browser AND by Go's router before any handler sees it — so
// `/api/projects/../rules` silently becomes `/api/rules` and would edit the STORE's rules
// while the caller believed it was editing a project's. The server's own name rule
// (a-z0-9) can never catch that, because the request never reaches it.
function projectSegment(name) {
  if (!/^[a-z0-9]{1,64}$/.test(name)) {
    throw new ApiError("invalid_project_name", `${name} is not a project name (a-z0-9 only)`, 0);
  }
  return encodeURIComponent(name);
}

export const api = {
  status: () => request("/api/status"),
  projects: () => request("/api/projects"),

  pads: (project) => request("/api/pads" + (project ? `?project=${encodeURIComponent(project)}` : "")),

  // Compact pad view: header, turn state, and the full table of contents WITHOUT any
  // section bodies — cheap even on a pad with hundreds of sections.
  pad: (ref) => request(`/api/pads/${encodeURIComponent(ref)}`),

  // One page of section bodies. No argument = the newest page; `before` walks
  // backwards through the history.
  sections: (ref, { before, limit, section } = {}) => {
    const q = new URLSearchParams();
    if (before != null) q.set("before", before);
    if (limit != null) q.set("limit", limit);
    if (section != null) q.set("section", section);
    const qs = q.toString();
    return request(`/api/pads/${encodeURIComponent(ref)}/sections${qs ? "?" + qs : ""}`);
  },

  // The opening excerpt of ONE section, for the outline's hover popup. Deliberately
  // not part of the TOC: it is wanted for the handful of entries a person points at,
  // not for every entry of every pad they open.
  sectionPreview: (ref, n, opts = {}) =>
    request(`/api/pads/${encodeURIComponent(ref)}/sections/${encodeURIComponent(n)}/preview`, opts),

  // The folded task board. It is its own endpoint rather than a field on the pad view
  // because the board sits behind a tab: a person reading the transcript should not pay
  // for a panel they have not opened. (Participants take the opposite route — they ride
  // along with the pad, because that strip is never hidden.)
  tasks: (ref, { task } = {}) => {
    const q = task ? `?task=${encodeURIComponent(task)}` : "";
    return request(`/api/pads/${encodeURIComponent(ref)}/tasks${q}`);
  },

  // Assignments that have gone unanswered, across every pad. This is the question a
  // person opens the UI with — "did anything stall?" — and it spans pads, so answering
  // it per-pad would mean opening every pad to find the one that is stuck.
  stuck: (olderThanS) =>
    request("/api/stuck" + (olderThanS != null ? `?older_than_s=${olderThanS}` : "")),

  unlock: (ref, password) =>
    request(`/api/pads/${encodeURIComponent(ref)}/unlock`, {
      method: "POST",
      body: JSON.stringify({ password }),
    }),

  // One pad at a time. Bulk cleanup by age stays in the CLI (`pad purge`), where the
  // victim list is printed and confirmed before anything is removed.
  deletePad: (ref) => request(`/api/pads/${encodeURIComponent(ref)}`, { method: "DELETE" }),

  // Rules — the one part of a pad this UI may write. The store and project levels are
  // files; a pad's own rules are appended as a section authored by "scratchpad", the
  // identity that means a PERSON changed this. Messages and tasks stay agent-only,
  // which is what "read-only" was ever protecting.
  storeRules: () => request("/api/rules"),
  setStoreRules: (text, replace) =>
    request("/api/rules", { method: "PUT", body: JSON.stringify({ text, replace: !!replace }) }),

  projectRules: (name) => request(`/api/projects/${projectSegment(name)}/rules`),
  setProjectRules: (name, text, replace) =>
    request(`/api/projects/${projectSegment(name)}/rules`, {
      method: "PUT",
      body: JSON.stringify({ text, replace: !!replace }),
    }),

  // No GET counterpart: a pad's rules ride along with the pad view, because the header
  // has to know whether there ARE rules before the person opens them.
  setPadRules: (ref, text, replace) =>
    request(`/api/pads/${encodeURIComponent(ref)}/rules`, {
      method: "PUT",
      body: JSON.stringify({ text, replace: !!replace }),
    }),
};
