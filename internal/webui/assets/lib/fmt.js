// fmt.js — small display helpers shared by the pages.

const RELATIVE = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

const UNITS = [
  ["year", 31536000],
  ["month", 2592000],
  ["week", 604800],
  ["day", 86400],
  ["hour", 3600],
  ["minute", 60],
];

// relTime renders a unix-second timestamp as "3 minutes ago". Anything under a minute
// reads as "just now": on a pad being written live, a ticking second count is noise.
export function relTime(unixSeconds) {
  if (!unixSeconds) return "—";
  const diff = Math.round(Date.now() / 1000) - unixSeconds;
  const abs = Math.abs(diff);
  if (abs < 60) return "just now";
  for (const [unit, secs] of UNITS) {
    if (abs >= secs) return RELATIVE.format(-Math.round(diff / secs), unit);
  }
  return "just now";
}

// shortRel is relTime compressed to a couple of characters ("15m", "4d", "3w"), for
// the sidebar — a 248px rail has no room for "15 minutes ago" beside a ref.
export function shortRel(unixSeconds) {
  if (!unixSeconds) return "—";
  const diff = Math.max(0, Math.round(Date.now() / 1000) - unixSeconds);
  if (diff < 60) return "now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  if (diff < 604800) return `${Math.floor(diff / 86400)}d`;
  if (diff < 2592000) return `${Math.floor(diff / 604800)}w`;
  if (diff < 31536000) return `${Math.floor(diff / 2592000)}mo`;
  return `${Math.floor(diff / 31536000)}y`;
}

// absTime is the exact local timestamp, used as the title of a relative one.
export function absTime(unixSeconds) {
  if (!unixSeconds) return "";
  return new Date(unixSeconds * 1000).toLocaleString();
}

// clockTime is the compact time-of-day shown on a section header.
export function clockTime(unixSeconds) {
  if (!unixSeconds) return "";
  return new Date(unixSeconds * 1000).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

// bytes renders a section's size so a long one is recognisable before it is opened.
export function bytes(n) {
  if (!n) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// ── untrusted display text ────────────────────────────────────────────────────
//
// Titles, author handles and section prose are written by AGENTS, so they are
// untrusted input. Going in through textContent stops them being markup, but not
// being a decoy: a string containing U+202E renders REVERSED, so a title can display
// as something other than what it contains, and zero-width characters can hide the
// difference between two entries entirely. Strip both before showing any of it.
//
// \t and \n survive the control-character class so a multiline excerpt keeps its
// shape; the single-line mode below collapses them along with the rest.
const UNSAFE_TEXT =
  /[\u0000-\u0008\u000B-\u001F\u007F-\u009F\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF]/g;

/**
 * safeText cleans agent-written text for display.
 * @param {string} raw
 * @param {{multiline?: boolean}} opts multiline keeps line breaks; the default
 *   collapses everything onto one line, for a title in a single-line row.
 */
export function safeText(raw, { multiline = false } = {}) {
  const cleaned = String(raw ?? "").replace(UNSAFE_TEXT, " ");
  if (!multiline) return cleaned.replace(/\s+/g, " ").trim();
  return cleaned
    .split("\n")
    .map((line) => line.replace(/[^\S\n]+/g, " ").trim())
    .join("\n")
    .trim();
}

/**
 * safeInline cleans agent text WITHOUT changing its length: every dangerous character
 * becomes exactly one space, and runs of whitespace are left alone.
 *
 * That is the whole difference from safeText, and it exists for one caller — a search
 * hit, which arrives with the match's position measured by the server. Collapsing
 * whitespace would move every character after the first double space, and the highlight
 * would then be painted over the wrong word. Length-preserving is the contract; a change
 * here that drops or merges characters breaks the offsets silently.
 * @param {string} raw
 */
export function safeInline(raw) {
  return String(raw ?? "").replace(UNSAFE_TEXT, " ");
}

/**
 * cutChars shortens a string to `max` CHARACTERS — code points, not UTF-16 units, so
 * an emoji or a non-BMP character is never split in half and left as a lone surrogate.
 */
export function cutChars(s, max) {
  const chars = Array.from(String(s ?? ""));
  if (chars.length <= max) return String(s ?? "");
  return `${chars.slice(0, max - 1).join("").trimEnd()}…`;
}

// ── agent identity ────────────────────────────────────────────────────────────
//
// Authors are agent handles, not people's names, so initials-of-a-full-name rules
// produce nothing useful: "backend" is a single word and a lone "B" tells a reader
// apart from "b5i2cj" only by luck. ROLES gives the handles a person actually uses
// the abbreviation they would write themselves; anything unrecognised falls back to
// a readable slice of the handle.
//
// Keys are matched against the normalised handle and against each of its words, so
// "backend", "Backend-2", "acme_backend" and "backend agent" all land on BE.
//
// Null-prototype: handles are written by agents, i.e. untrusted, and a plain object
// literal would resolve an author named "constructor" through Object.prototype and
// hand back a FUNCTION where a two-letter string was expected.
//
// EVERY VALUE HERE IS 2–3 CHARACTERS, and nothing enforces it. An avatar is a fixed disc;
// a four-character value spills out of it. The test suite checks that the FALLBACK paths
// stay within the limit — those take arbitrary input — but it cannot check this table,
// because the table is module-private and exporting it to let a test read it would widen
// the production surface so a test can exist. Deliberately not done: the first consumer
// who imports it turns an internal lookup into a contract we have to keep, and the defect
// it would catch is one slightly wide disc, visible by looking at it.
//
// So a new entry is checked by REVIEW. That is the whole enforcement; there is no other.
const ROLES = Object.assign(Object.create(null), {
  backend: "BE", back: "BE", server: "BE", api: "API", srv: "BE",
  frontend: "FE", front: "FE", client: "FE", web: "FE", webapp: "FE", ui: "UI",
  fullstack: "FS", full: "FS",
  mobile: "MOB", android: "AND", ios: "iOS", flutter: "FLT",
  devops: "DO", ops: "OPS", sre: "SRE", infra: "INF", infrastructure: "INF",
  platform: "PLT", deploy: "DEP", release: "REL", build: "BLD", ci: "CI", cd: "CD",
  qa: "QA", test: "QA", tester: "QA", testing: "QA", audit: "AUD", auditor: "AUD",
  review: "RV", reviewer: "RV",
  pm: "PM", "project manager": "PM", "product manager": "PM", manager: "PM",
  po: "PO", "product owner": "PO", ba: "BA", "business analyst": "BA",
  lead: "LD", architect: "ARC", arch: "ARC",
  design: "UX", designer: "UX", ux: "UX",
  data: "DAT", db: "DB", database: "DB", etl: "ETL",
  ml: "ML", ai: "AI", llm: "LLM", "data scientist": "DS",
  security: "SEC", sec: "SEC", infosec: "SEC",
  docs: "DOC", doc: "DOC", writer: "DOC",
  support: "SUP", mcp: "MCP", cli: "CLI", sdk: "SDK", bot: "BOT", agent: "AG",
});

// words splits a handle the way handles are actually written: spaces, dashes,
// underscores, dots, slashes and camelCase humps are all word boundaries.
function words(name) {
  return String(name ?? "")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);
}

// agentInitials maps an author handle onto the 2–3 characters shown in its avatar.
export function agentInitials(name) {
  const parts = words(name);
  if (!parts.length) return "?";

  const whole = parts.join(" ");
  if (ROLES[whole]) return ROLES[whole];
  // Two-word roles ("project manager") before single words, so the pair wins over
  // its own first half ("project").
  for (let i = 0; i < parts.length - 1; i++) {
    const pair = `${parts[i]} ${parts[i + 1]}`;
    if (ROLES[pair]) return ROLES[pair];
  }
  for (const p of parts) {
    if (ROLES[p]) return ROLES[p];
  }

  // Unrecognised: a short leading word is already an abbreviation ("skb", "erp"),
  // so keep it whole; otherwise take one letter per word, or two from a lone word.
  const first = parts[0];
  if (first.length <= 3) return first.toUpperCase();
  if (parts.length > 1) return (first[0] + parts[1][0]).toUpperCase();
  return first.slice(0, 2).toUpperCase();
}

// agentColorIndex hashes a handle onto one of the avatar hues. Same handle → same
// colour in every pad, and it hashes the FULL handle, not the initials, so two
// different agents that abbreviate alike still look different.
export function agentColorIndex(name, palette = 6) {
  const s = String(name ?? "");
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = (h + ((h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))) >>> 0;
  }
  return h % palette;
}
