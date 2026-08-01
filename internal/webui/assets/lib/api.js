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

  unlock: (ref, password) =>
    request(`/api/pads/${encodeURIComponent(ref)}/unlock`, {
      method: "POST",
      body: JSON.stringify({ password }),
    }),

  // One pad at a time. Bulk cleanup by age stays in the CLI (`pad purge`), where the
  // victim list is printed and confirmed before anything is removed.
  deletePad: (ref) => request(`/api/pads/${encodeURIComponent(ref)}`, { method: "DELETE" }),
};
