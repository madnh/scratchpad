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

// authorColor maps an author name onto one of the timeline's dot colours, stably, so
// the same agent keeps the same colour for the whole conversation.
const DOT_COLOURS = ["accent", "success", "warning", "error", "info"];
export function authorColor(author, authors) {
  const idx = authors.indexOf(author);
  return DOT_COLOURS[(idx < 0 ? 0 : idx) % DOT_COLOURS.length];
}
