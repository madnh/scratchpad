// Tests for the display helpers the whole UI shares.
//
// These run on `node --test`, which ships with Node: no package.json to install, no
// node_modules, no browser. That is the whole reason this layer exists — fmt.js imports
// nothing and touches no DOM, so the most valuable thing to test here is also the
// cheapest, and it can sit inside `make check` without making the gate flaky.
//
// They live OUTSIDE assets/ because `//go:embed all:assets` would compile anything under
// there into the binary and serve it.
//
// What is asserted is the CONTRACT the source documents, not the output it happens to
// produce. Anything locale- or timezone-dependent (relTime's wording, absTime's format)
// is checked for shape only: pinning `Intl` output would make this suite fail on a
// machine with different defaults, which teaches people to ignore it.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  relTime, shortRel, absTime, clockTime, bytes,
  safeText, safeInline, cutChars, agentInitials, agentColorIndex,
} from "../assets/lib/fmt.js";

const now = () => Math.round(Date.now() / 1000);

// ── untrusted display text ─────────────────────────────────────────────────────
//
// Agents write titles and handles, so these are the functions standing between an
// agent-written string and what a person believes they are looking at.

test("safeText strips the characters that let a string lie about itself", () => {
  // U+202E renders everything after it right-to-left, so a title can DISPLAY as
  // something other than what it contains.
  assert.equal(safeText("report‮gnp.exe"), "report gnp.exe");
  // Zero-width characters make two different entries look identical.
  assert.equal(safeText("ad​min"), "ad min");
  assert.equal(safeText("a\u0000b"), "a b");
  assert.equal(safeText("a﻿b"), "a b");
});

test("safeText collapses to one line by default and keeps lines when asked", () => {
  assert.equal(safeText("  a \t b \n c  "), "a b c");
  assert.equal(safeText("a\n\nb", { multiline: true }), "a\n\nb");
  assert.equal(safeText("a  \t  b\nc", { multiline: true }), "a b\nc");
});

test("safeText survives nothing at all", () => {
  assert.equal(safeText(null), "");
  assert.equal(safeText(undefined), "");
  assert.equal(safeText(""), "");
});

// safeInline's whole reason to exist is that a search hit arrives with the match's
// offset measured by the SERVER. Drop or merge one character and the highlight lands on
// the wrong word — silently, because nothing throws and the text still reads fine.
test("safeInline preserves length exactly — the search highlight depends on it", () => {
  for (const raw of [
    "plain text",
    "bidi ‮ here",
    "zero​width‌joined",
    "controlchars",
    "double  spaces   kept",
    "⁦isolate⁩",
  ]) {
    assert.equal(safeInline(raw).length, raw.length, `length changed for ${JSON.stringify(raw)}`);
  }
});

test("safeInline neutralises the same characters safeText does", () => {
  assert.equal(safeInline("a‮b"), "a b");
  assert.ok(!/[​‮]/.test(safeInline("x​y‮z")));
});

// EVERY character the source says it strips, not the two or three a test author happened
// to think of. Sampling is what lets the character class shrink unnoticed: drop the
// isolate range from it and the handful of sampled characters still pass, while U+2066
// goes back to reordering whatever follows it.
//
// The ranges are copied from the comment above UNSAFE_TEXT in fmt.js. If that class is
// deliberately narrowed, this list is the place the decision has to be repeated — which is
// the point: narrowing it should cost a conversation, not go through silently.
const UNSAFE_RANGES = [
  [0x0000, 0x0008], [0x000b, 0x001f], [0x007f, 0x009f],
  [0x200b, 0x200f], [0x202a, 0x202e], [0x2060, 0x2064],
  [0x2066, 0x2069], [0xfeff, 0xfeff],
];

function unsafeChars() {
  const out = [];
  for (const [from, to] of UNSAFE_RANGES) {
    for (let cp = from; cp <= to; cp++) out.push(String.fromCodePoint(cp));
  }
  return out;
}

test("safeText and safeInline neutralise every character in the documented ranges", () => {
  for (const ch of unsafeChars()) {
    const hex = ch.codePointAt(0).toString(16).padStart(4, "0");
    assert.ok(!safeText(`a${ch}b`).includes(ch), `safeText let U+${hex} through`);
    assert.ok(!safeInline(`a${ch}b`).includes(ch), `safeInline let U+${hex} through`);
    assert.equal(safeInline(`a${ch}b`).length, 3, `safeInline changed length at U+${hex}`);
  }
});

// The other half of that contract: \t and \n are deliberately OUTSIDE the class, "so a
// multiline excerpt keeps its shape". Widening the class to swallow them would be just as
// silent a change in the other direction.
test("safeText keeps the line breaks a multiline excerpt is made of", () => {
  assert.equal(safeText("first\nsecond", { multiline: true }), "first\nsecond");
  assert.ok(safeInline("first\nsecond").includes("\n"));
});

// ── cutting text without breaking it ───────────────────────────────────────────

test("cutChars never leaves half of a character behind", () => {
  // BOTH parities of cut, and that is not padding. A UTF-16-unit slice of "👍👍👍👍" at 3
  // takes 2 units — which lands exactly on a pair boundary and comes out clean, so a
  // broken implementation passes. Only an ODD number of units splits one. Checking just
  // the even case is a test that reports the bug as absent; found by breaking cutChars
  // on purpose and watching this one stay green.
  for (const [text, max] of [["👍👍👍👍", 3], ["👍👍👍👍👍", 4], ["👍👍👍👍👍👍", 5]]) {
    const cut = cutChars(text, max);
    assert.ok(cut.length < text.length, `expected ${JSON.stringify(text)} at ${max} to actually cut`);
    // A lone surrogate is the failure this function exists to prevent: it renders as a
    // replacement glyph and breaks copy-paste.
    assert.ok(!/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/.test(cut), `unpaired high surrogate in ${JSON.stringify(cut)}`);
    assert.ok(!/(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/.test(cut), `unpaired low surrogate in ${JSON.stringify(cut)}`);
    assert.ok(cut.endsWith("…"));
  }
});

test("cutChars counts code points, not UTF-16 units", () => {
  // Four astral characters are 8 units long; a length-based cut would trim this and it
  // must not, because there are only 4 CHARACTERS.
  assert.equal(cutChars("👍👍👍👍", 4), "👍👍👍👍");
});

// The assertion the function's NAME makes and nothing was checking: "shortens a string to
// `max` CHARACTERS". The ellipsis counts, which is the whole reason the slice is `max - 1`
// — and changing it to `max` left all twenty tests green while cutChars("abcdefghij", 5)
// handed back six characters.
//
// Found by choosing the probe from the contract rather than from the tests. A mutation
// picked by reading the suite can only confirm the suite runs; it cannot find an assertion
// nobody wrote.
test("cutChars result is never longer than max — the ellipsis counts", () => {
  for (const [text, max] of [
    ["abcdefghij", 5],
    ["abcdefghij", 2],
    ["a".repeat(200), 48],
    ["👍👍👍👍👍👍", 3],
    ["mixed 👍 text that runs on", 10],
  ]) {
    // Code points, because that is the unit the contract is written in.
    const got = Array.from(cutChars(text, max)).length;
    assert.ok(got <= max, `cutChars(${JSON.stringify(text)}, ${max}) returned ${got} characters`);
  }
});

test("cutChars leaves anything short enough alone", () => {
  assert.equal(cutChars("short", 10), "short");
  assert.equal(cutChars("", 5), "");
  assert.equal(cutChars(null, 5), "");
});

// ── agent identity ─────────────────────────────────────────────────────────────

test("agentInitials gives known roles the abbreviation a person would write", () => {
  assert.equal(agentInitials("backend"), "BE");
  assert.equal(agentInitials("ios"), "iOS");
  assert.equal(agentInitials("qa"), "QA");
});

test("agentInitials finds the role however the handle is decorated", () => {
  assert.equal(agentInitials("Backend-2"), "BE");
  assert.equal(agentInitials("acme_backend"), "BE");
  assert.equal(agentInitials("backend agent"), "BE");
  assert.equal(agentInitials("backendAgent"), "BE", "camelCase humps are word boundaries");
});

test("agentInitials prefers a two-word role over its own first half", () => {
  assert.equal(agentInitials("acme product owner"), "PO");
  assert.equal(agentInitials("project manager"), "PM");
});

// The ROLES table is a null-prototype object because handles are agent-written. Through
// a plain object literal, an author called "constructor" resolves through
// Object.prototype and hands back a FUNCTION where two characters were expected.
test("agentInitials cannot be tricked into returning something off Object.prototype", () => {
  for (const handle of ["constructor", "toString", "hasOwnProperty", "__proto__", "valueOf"]) {
    const out = agentInitials(handle);
    assert.equal(typeof out, "string", `${handle} did not produce a string`);
    assert.ok(out.length > 0 && out.length <= 3, `${handle} produced ${JSON.stringify(out)}`);
  }
});

test("agentInitials falls back readably for handles it does not know", () => {
  assert.equal(agentInitials("skb"), "SKB", "a short leading word is already an abbreviation");
  assert.equal(agentInitials("alpha beta"), "AB");
  assert.equal(agentInitials("zephyr"), "ZE");
  assert.equal(agentInitials(""), "?");
  assert.equal(agentInitials(null), "?");
  assert.equal(agentInitials("!!!"), "?", "nothing word-like left after splitting");
});

// "maps an author handle onto the 2–3 characters shown in its avatar" — an avatar is a
// fixed disc, so a longer string spills out of it. The handles that reach the fallback are
// the arbitrary ones (anything not in the role table), which is precisely where a shape
// nobody sampled can turn up.
//
// LIMIT, stated because it is the interesting half: this cannot check the role table
// itself. ROLES is module-private, so a new entry with a four-character value would sail
// past every assertion here. Enforcing that from outside would mean exporting the table
// only for the test, and a test that changes the module's surface to make itself possible
// is a trade I am not taking on my own.
test("agentInitials stays inside the avatar for handles it has never seen", () => {
  const handles = [
    "zephyr", "alpha beta", "a b c d e", "skb", "x", "xy", "xyz", "wxyz",
    "aVeryLongCamelCaseHandleIndeed", "dash-separated-handle", "dot.separated.handle",
    "under_scored_handle", "handle/with/slashes", "  spaced  out  ", "123456",
    "ÅÄÖ", "handle9000", "a".repeat(64), "!!!", "constructor", "__proto__",
  ];
  for (const h of handles) {
    const out = agentInitials(h);
    assert.equal(typeof out, "string", `${JSON.stringify(h)} did not produce a string`);
    assert.ok(out.length >= 1 && out.length <= 3,
      `${JSON.stringify(h)} produced ${JSON.stringify(out)} (${out.length} chars)`);
  }
});

test("agentColorIndex is stable and inside the palette", () => {
  for (const name of ["backend", "ios", "a very long agent handle", "", "👍"]) {
    const a = agentColorIndex(name);
    assert.equal(a, agentColorIndex(name), `${name} was not stable`);
    assert.ok(Number.isInteger(a) && a >= 0 && a < 6, `${name} produced ${a}`);
  }
  assert.ok(agentColorIndex("x", 3) < 3, "palette size is honoured");
});

test("agentColorIndex hashes the whole handle, not a prefix of it", () => {
  // The point of hashing the full handle is that two agents whose initials collide still
  // look different — so this has to fail if the function ever starts reading only the
  // first characters.
  //
  // NOT by asserting one pair differs: six buckets means any two handles collide about
  // one time in six, and "backend"/"backend2" happen to be such a pair. A test that only
  // passes because two particular strings got lucky is the guard-whose-premise-was-never-
  // checked failure. Asserting a FAMILY sharing a prefix is not all one colour is
  // deterministic, and it is the property that actually matters.
  const samePrefix = ["backend", "backend2", "backendX", "back-office", "backup", "backlog"];
  const colours = new Set(samePrefix.map((n) => agentColorIndex(n)));
  assert.ok(colours.size > 1, `a prefix-only hash would put all of these on one colour: ${[...colours]}`);
});

// ── sizes and times ────────────────────────────────────────────────────────────

test("bytes switches units where a reader expects", () => {
  assert.equal(bytes(0), "0 B");
  assert.equal(bytes(512), "512 B");
  assert.equal(bytes(1023), "1023 B");
  assert.equal(bytes(1024), "1.0 KB");
  assert.equal(bytes(1024 * 1024), "1.0 MB");
  assert.equal(bytes(1024 * 1024 * 3), "3.0 MB");
});

test("shortRel compresses an age to the couple of characters a 248px rail has room for", () => {
  const t = now();
  assert.equal(shortRel(t), "now");
  assert.equal(shortRel(t - 59), "now");
  assert.equal(shortRel(t - 60), "1m");
  assert.equal(shortRel(t - 3600), "1h");
  assert.equal(shortRel(t - 86400), "1d");
  assert.equal(shortRel(t - 604800), "1w");
  assert.equal(shortRel(t - 2592000), "1mo");
  assert.equal(shortRel(t - 31536000), "1y");
  assert.equal(shortRel(0), "—");
});

test("shortRel does not render a future timestamp as a negative age", () => {
  assert.equal(shortRel(now() + 3600), "now");
});

// relTime's words come from Intl, so they differ by locale. Asserting them would pin the
// suite to whatever this machine happens to be set to; the contract worth holding is the
// behaviour around the edges.
test("relTime reads as 'just now' under a minute and says something after it", () => {
  const t = now();
  assert.equal(relTime(t), "just now");
  assert.equal(relTime(t - 59), "just now");
  assert.ok(relTime(t - 3600).length > 0);
  assert.equal(relTime(0), "—");
  assert.equal(relTime(undefined), "—");
});

test("absTime and clockTime are empty for no timestamp and non-empty otherwise", () => {
  assert.equal(absTime(0), "");
  assert.equal(clockTime(0), "");
  assert.ok(absTime(now()).length > 0);
  assert.ok(clockTime(now()).length > 0);
});
