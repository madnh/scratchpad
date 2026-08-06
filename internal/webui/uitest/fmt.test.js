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
