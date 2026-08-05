// Search — finding a pad by WHAT WAS SAID in it, across the store or inside one project.
//
// Every other view here selects by position: a pad, a section number, a task, a kind.
// This one reads the prose, which makes it the page a person arrives at with a word and
// no idea where it lives.
//
// Three things shape it.
//
// The QUESTION LIVES IN THE URL. A search is a hash away from being a link, and the
// thing worth sending a colleague is the question, not a screenshot of the answer. So
// submitting navigates rather than fetching in place: back works, reload works, and the
// address bar always describes what is on screen.
//
// It does NOT search as you type. There is no index — the store reads the pads it looks
// at — so ten keystrokes would be ten walks over every pad. Enter, or the button.
//
// WHAT WAS NOT SEARCHED IS PART OF THE ANSWER. A protected pad is skipped, and a result
// of nothing means "not here" only if you also know where it did not look. That count
// belongs on the summary line itself, not below it: a person who reads one line must not
// read the wrong answer.

import "/vendor/puredashboard/input.js";
import "/vendor/puredashboard/select.js";
import "/vendor/puredashboard/checkbox.js";
import "/vendor/puredashboard/tag.js";
import "/vendor/puredashboard/result.js";

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import { el, pageHead, skeleton, errorView, setChildren, link } from "/lib/ui.js";
import { relTime, absTime, cutChars, safeText, safeInline } from "/lib/fmt.js";

// How many hits one search returns. The server caps it harder (500); this is the number
// a person actually reads before narrowing the question instead of scrolling.
const LIMIT = 100;

// The time presets. A search for a decision is usually "recently" or "ever", and a date
// picker asks for a precision nobody has when they are looking for a word.
const WINDOWS = [
  { value: "", label: "Any time" },
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
];

const KINDS = [
  { value: "", label: "All kinds" },
  { value: "message", label: "Messages" },
  { value: "task", label: "Task events" },
  { value: "rules", label: "Rules" },
  { value: "notice", label: "Notices" },
];

// readQuery turns the URL into the search that is on screen. It is the ONE reader of the
// query string: the form is filled from what this returns, and so is the request, so the
// two can never describe different searches.
function readQuery(q = {}) {
  return {
    query: q.q || "",
    project: q.project || "",
    author: q.author || "",
    kind: q.kind || "",
    days: q.days || "",
    oldest: q.oldest === "1",
    regexp: q.regexp === "1",
    word: q.word === "1",
    matchCase: q.case === "1",
  };
}

// writeQuery is readQuery's inverse, and only records what was ASKED for — an empty
// field leaves no trace in the URL, so a copied link reads as the question it was.
function writeQuery(s) {
  const p = new URLSearchParams();
  if (s.query) p.set("q", s.query);
  for (const k of ["project", "author", "kind", "days"]) {
    if (s[k]) p.set(k, s[k]);
  }
  for (const [k, on] of [["oldest", s.oldest], ["regexp", s.regexp], ["word", s.word], ["case", s.matchCase]]) {
    if (on) p.set(k, "1");
  }
  return p.toString();
}

export default function mount(outlet, ctx) {
  let disposed = false;
  const state = readQuery(ctx?.query);

  // ── the form ───────────────────────────────────────────────────────────────
  // Built once and never rebuilt: it holds what the person typed, and a page that
  // reconstructs its own controls loses the caret in the middle of a correction.

  const input = el("puredashboard-input", {
    type: "search", placeholder: "Find a word or phrase in pad content", value: state.query,
  });
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") submit(); });

  const projectSel = el("puredashboard-select", { placeholder: "All projects" });
  projectSel.value = state.project;

  const kindSel = el("puredashboard-select", { placeholder: "All kinds" });
  kindSel.options = KINDS.filter((k) => k.value).map((k) => ({ value: k.value, label: k.label }));
  kindSel.value = state.kind;

  const whenSel = el("puredashboard-select", { placeholder: "Any time" });
  whenSel.options = WINDOWS.filter((w) => w.value).map((w) => ({ value: w.value, label: w.label }));
  whenSel.value = state.days;

  const authorInput = el("puredashboard-input", { placeholder: "Any agent", value: state.author });
  authorInput.addEventListener("keydown", (e) => { if (e.key === "Enter") submit(); });

  const flag = (name, label, title) => {
    const box = el("puredashboard-checkbox", { title });
    box.label = label;
    box.checked = state[name];
    return box;
  };
  // "Oldest first" is not a sort preference dressed up as one — it answers a DIFFERENT
  // question. The default surfaces what is being said about a word now; this surfaces
  // where it was settled, which is almost always the first time anyone wrote it down.
  const oldestBox = flag("oldest", "Oldest first", "Where a word was first written — usually where it was decided");
  const wordBox = flag("word", "Whole word", "Match whole words only");
  const caseBox = flag("matchCase", "Match case", "Match capitalisation exactly");
  const reBox = flag("regexp", "Regex", "Read the pattern as a regular expression");

  // No class: the theme's default button is the page's primary action, exactly as the
  // pad page's Unlock is. A second style here would make the two look like two ranks.
  const goBtn = el("button", { type: "button", text: "Search", onclick: () => submit() });

  const form = el("form", { class: "search__form", onsubmit: (e) => { e.preventDefault(); submit(); } },
    el("div", { class: "search__row" }, input, goBtn),
    el("div", { class: "search__filters" },
      labelled("Project", projectSel),
      labelled("Agent", authorInput),
      labelled("Kind", kindSel),
      labelled("When", whenSel),
      el("div", { class: "search__flags" }, oldestBox, wordBox, caseBox, reBox),
    ),
  );

  // An ordinary wrapping <label>: it names the control and clicking the caption focuses
  // it, both from the platform.
  //
  // This was a <div> plus a hand-set aria-label for a while, because the vendored
  // components mirror a wrapping <label> down to their inner control and used to mint
  // its id from a per-file counter — the first select and the first input both claimed
  // `#pd-label-1`, and this row's Agent field announced itself as "Project". Fixed
  // upstream in puredashboard (labelIdFor, one counter), vendored here, so the
  // workaround is gone rather than left in place to rot.
  function labelled(text, control) {
    return el("label", { class: "search__field" }, el("span", { class: "search__label", text }), control);
  }

  // The project list is a convenience, so a failure to fetch it must not take the page
  // down with it — the field falls back to whatever the URL already said.
  api.projects()
    .then(({ projects = [] }) => {
      if (disposed) return;
      projectSel.options = projects.map((p) => ({ value: p.name, label: `${p.name} (${p.pad_count})` }));
      projectSel.value = state.project;
    })
    .catch(() => {});

  // submit writes the question into the URL. The router then re-enters this page with
  // it, which is what makes the address bar and the screen the same thing.
  function submit() {
    const next = {
      query: (input.value || "").trim(),
      project: projectSel.value || "",
      author: (authorInput.value || "").trim(),
      kind: kindSel.value || "",
      days: whenSel.value || "",
      oldest: !!oldestBox.checked,
      word: !!wordBox.checked,
      matchCase: !!caseBox.checked,
      regexp: !!reBox.checked,
    };
    const qs = writeQuery(next);
    const target = `#/search${qs ? "?" + qs : ""}`;
    if (location.hash === target) run(next);   // same question asked again: just re-run
    else location.hash = target;
  }

  const results = el("div", { class: "search__results" });

  // ── results go stale, and say so rather than moving ────────────────────────
  //
  // A result list is an answer about a moment. Repainting it when a pad changes would
  // reorder rows under the cursor of whoever is reading them — the default order is by
  // pad activity, so a single new section can move a whole group to the top.
  //
  // So the live stream only ever sets a flag here. The person decides when to ask again,
  // and until they do, what is on screen keeps matching what they were told about it.
  let staleNote = null;
  let ranAt = null;   // the search the notice would re-run

  function markStale(ref) {
    if (!ranAt || staleNote) return;
    staleNote = el("p", { class: "search__stale" },
      el("span", { text: `${ref} changed since this search ran · ` }),
      el("button", { type: "button", class: "ghost-btn", text: "Search again", onclick: () => run(ranAt) }),
    );
    results.prepend(staleNote);
  }

  // Scope the notice the way the search was scoped: a pad in another project has no
  // bearing on a result list that never looked outside this one.
  const offPad = onPad((ev) => {
    if (ev.type === "removed" || !ranAt) return;
    if (ranAt.project && ev.project !== ranAt.project) return;
    markStale(ev.ref);
  });

  outlet.replaceChildren(
    pageHead("Search", "the one read that selects by what was written"),
    form,
    results,
  );

  // The tab says WHICH search it is holding. The router cannot do this one — its title
  // hook is handed the path params, and the question here is in the query string — so
  // the page writes it, after the router already wrote the generic one.
  document.title = state.query ? `Search: ${state.query} — Scratchpad` : "Search — Scratchpad";

  // ── running one search ─────────────────────────────────────────────────────

  async function run(s) {
    if (!s.query) {
      setChildren(results, el("p", { class: "muted", text: "Type a word or phrase, then press Enter." }));
      input.focus?.();
      return;
    }
    setChildren(results, skeleton(4));
    staleNote = null;      // this run answers whatever the notice was about
    ranAt = s;
    goBtn.disabled = true;
    try {
      const res = await api.search(s.query, {
        project: s.project, author: s.author, kind: s.kind,
        after: s.days ? Math.floor(Date.now() / 1000) - Number(s.days) * 86400 : undefined,
        oldest: s.oldest, word: s.word, matchCase: s.matchCase, regexp: s.regexp,
        limit: LIMIT,
      });
      if (disposed) return;
      renderResults(s, res);
    } catch (err) {
      if (disposed) return;
      // A bad regex is the person's typo, not a broken page: it belongs beside the field
      // they typed it into, with what they already have still on screen.
      if (err.code === "invalid_input") {
        setChildren(results, el("puredashboard-result", {
          status: "warning", title: "That search could not be run", subtitle: err.message,
        }));
        return;
      }
      setChildren(results, errorView(err, () => run(s)));
    } finally {
      goBtn.disabled = false;
    }
  }

  function renderResults(s, res) {
    const hits = res.hits || [];
    const skipped = res.skipped || [];

    const parts = [summary(s, res)];
    for (const w of res.warnings || []) {
      parts.push(el("p", { class: "search__warn", text: `warning: ${w}` }));
    }
    if (skipped.length) {
      parts.push(el("p", { class: "search__skipped" },
        el("span", { text: "Not searched (protected): " }),
        ...skipped.flatMap((ref, i) => [
          i ? el("span", { text: ", " }) : null,
          link(`#/pads/${encodeURIComponent(ref)}`, ref, "ref"),
        ].filter(Boolean)),
        el("span", { text: " — open a pad and unlock it to search inside." }),
      ));
    }
    if (!hits.length) {
      parts.push(el("puredashboard-result", {
        status: "info",
        title: "No matches",
        subtitle: skipped.length
          ? "Nothing in the pads that were read. The protected pads above were not among them."
          : "Nothing in any pad that was read. Try a shorter word, or turn off “Whole word”.",
      }));
      setChildren(results, ...parts);
      return;
    }

    // Oldest-first deliberately drops the grouping: "which of these came first" is a
    // question about absolute time, and answering it inside per-pad groups would answer
    // a different one. The store already ordered the hits; this only mirrors the shape.
    parts.push(s.oldest ? flatList(s, hits) : groupedList(s, hits));
    setChildren(results, ...parts);
  }

  function summary(s, res) {
    const n = (res.hits || []).length;
    const skipped = (res.skipped || []).length;
    // Everything that decides how to read the number is ON this line. Pushing the
    // skipped count to the paragraph below turns "0 matches" into a claim the search
    // never made.
    let text = `${n} match${n === 1 ? "" : "es"} in ${res.scanned} pad${res.scanned === 1 ? "" : "s"} searched`;
    if (skipped) text += ` · ${skipped} pad${skipped === 1 ? "" : "s"} NOT searched (protected)`;
    if (res.truncated) text += ` · stopped at ${LIMIT}, there may be more`;
    return el("p", { class: "search__summary", text });
  }

  function groupedList(s, hits) {
    const box = el("div", { class: "hits" });
    let current = "";
    let group = null;
    for (const h of hits) {
      if (h.ref !== current) {
        current = h.ref;
        group = el("div", { class: "hits__group" },
          el("div", { class: "hits__pad" },
            link(`#/pads/${encodeURIComponent(h.ref)}`, h.ref, "ref"),
            el("span", { class: "muted", title: absTime(h.ts), text: relTime(h.ts) }),
          ),
        );
        box.append(group);
      }
      group.append(hitRow(s, h));
    }
    return box;
  }

  function flatList(s, hits) {
    const box = el("div", { class: "hits hits--flat" });
    for (const h of hits) box.append(hitRow(s, h, { withRef: true }));
    return box;
  }

  // One hit is a LINK, not a row with a click handler: ⌘-click, middle-click and
  // copy-link all have to keep working, and the section it points at is a real address.
  function hitRow(s, h, { withRef = false } = {}) {
    const href = `#/pads/${encodeURIComponent(h.ref)}?section=${h.section}&q=${encodeURIComponent(s.query)}`;
    const where = h.in_title ? "title" : `L${h.line}`;
    return el("a", { class: "hit", href, title: h.title },
      el("span", { class: "hit__meta" },
        withRef ? el("span", { class: "ref", text: h.ref }) : null,
        el("span", { class: "hit__sec", text: `§${h.section}` }),
        el("span", { class: "hit__where", text: where }),
        el("span", { class: "hit__author", text: h.author }),
        h.kind ? el("puredashboard-tag", { size: "sm", color: "neutral", text: h.kind }) : null,
        el("span", { class: "hit__time muted", title: absTime(h.ts), text: relTime(h.ts) }),
      ),
      // The section's own title, above the line — it is the most deliberate statement of
      // what the section is about, and on a body hit it is the context the line lacks.
      h.in_title ? null : el("span", { class: "hit__title", text: cutChars(safeText(h.title || ""), 120) }),
      marked(h.text, h.match_start, h.match_end),
    );
  }

  // marked paints the match the SERVER found. The offsets are computed by the same
  // matcher that decided this line was a hit, so highlighting never disagrees with
  // finding — re-running the pattern here would, because "whole word" is spelled with
  // Unicode classes RE2 means and JavaScript's \b does not.
  //
  // Text goes in as text nodes throughout: every line here was written by an agent.
  function marked(text, start, end) {
    const box = el("span", { class: "hit__text" });
    // safeInline, not safeText: this one preserves length, and the offsets below were
    // measured by the server against the string as it was written.
    const r = [...safeInline(text)];
    if (!(Number.isInteger(start) && start >= 0 && Number.isInteger(end) && end > start && end <= r.length)) {
      box.textContent = r.join("");
      return box;
    }
    box.append(
      document.createTextNode(r.slice(0, start).join("")),
      el("mark", { text: r.slice(start, end).join("") }),
      document.createTextNode(r.slice(end).join("")),
    );
    return box;
  }

  run(state);

  return () => { disposed = true; offPad(); };
}
