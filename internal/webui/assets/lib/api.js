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

  // Content search — the one read that selects by what was WRITTEN rather than by
  // position. The three scopes a person asks for are not three calls: no scope is the
  // whole store, `project` narrows to one project, `ref` to one pad.
  //
  // A protected pad answers only when named by `ref`, and only if this session has
  // already unlocked it — the server takes the password from the session, so it never
  // travels in a URL that ends up in history or in a link someone copies out.
  //
  // `before`/`after` are unix SECONDS. The CLI's "30d" spelling stays at the terminal:
  // a browser knows the clock and can subtract, and a wire format that parses prose is
  // one more place for the two surfaces to disagree about what "a month ago" means.
  search: (query, {
    project, ref, exclude, author, kind, before, after,
    oldest, regexp, word, matchCase, limit,
  } = {}) => {
    const q = new URLSearchParams();
    q.set("q", query);
    for (const [k, v] of Object.entries({ project, ref, author, kind })) {
      if (v) q.set(k, v);
    }
    for (const [k, v] of Object.entries({ before, after, limit })) {
      if (v != null && v !== "") q.set(k, String(v));
    }
    // Flags travel only when ON. Sending `word=false` would work, but a URL a person
    // copies out of the address bar is better read when it lists what was asked for
    // rather than everything that was not.
    for (const [k, v] of Object.entries({ oldest, regexp, word, case: matchCase })) {
      if (v) q.set(k, "true");
    }
    for (const r of exclude || []) q.append("exclude", r);
    return request(`/api/search?${q.toString()}`);
  },

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
  //
  // Every write carries ifDigest: the version of that LEVEL the dialog was showing. This
  // UI is exempt from the policy over who may write rules — it is the surface the policy
  // points at — but not from the version check. A tab left open while an agent posted new
  // rules would otherwise save over a version nobody here ever saw.
  // The deployment's own settings. The digest that comes back with a read must be quoted
  // on the write — the same compare-and-set the rules use, and for the same reason: two
  // tabs of this page lose an edit exactly the way two agents do.
  config: () => request("/api/config"),
  setConfig: (config, ifDigest) =>
    request("/api/config", {
      method: "PUT",
      body: JSON.stringify({ config, if_digest: ifDigest || "" }),
    }),

  storeRules: () => request("/api/rules"),
  setStoreRules: (text, replace, ifDigest, notify) =>
    request("/api/rules", {
      method: "PUT",
      body: JSON.stringify({ text, replace: !!replace, if_digest: ifDigest || "", notify: !!notify }),
    }),

  projectRules: (name) => request(`/api/projects/${projectSegment(name)}/rules`),
  setProjectRules: (name, text, replace, ifDigest, notify) =>
    request(`/api/projects/${projectSegment(name)}/rules`, {
      method: "PUT",
      body: JSON.stringify({ text, replace: !!replace, if_digest: ifDigest || "", notify: !!notify }),
    }),

  // How many pads an announcement at this level would reach. Read before anyone commits
  // to it: a fan-out whose size only shows up afterwards is one people leave switched off.
  rulesNotifyTargets: (name) =>
    request(name ? `/api/projects/${projectSegment(name)}/rules/notify-targets` : "/api/rules/notify-targets"),

  // No GET counterpart: a pad's rules ride along with the pad view, because the header
  // has to know whether there ARE rules before the person opens them.
  setPadRules: (ref, text, replace, ifDigest) =>
    request(`/api/pads/${encodeURIComponent(ref)}/rules`, {
      method: "PUT",
      body: JSON.stringify({ text, replace: !!replace, if_digest: ifDigest || "" }),
    }),
};
