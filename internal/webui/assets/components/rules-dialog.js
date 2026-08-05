// rules-dialog.js — the one place rules are shown and edited, at every level.
//
// A modal rather than a panel in the rail, deliberately. Rules are a short block of text
// a person opens, reads and closes; giving them a third rail tab would cost the
// transcript horizontal room permanently, and would drag the rail's own state — which tab
// is selected, where it is scrolled — into a thing that is looked at once a session.
//
// One component serves the pad view, the project page and Settings. They differ only in
// how many levels exist to show, which is data, not three different dialogs.

import { dialog } from "/vendor/puredashboard/dialog.js";
import { toast } from "/vendor/puredashboard/toast.js";
import "/vendor/puredashboard/md.js";

import { api } from "/lib/api.js";
import { el, setChildren, svgIcon } from "/lib/ui.js";
import { relTime } from "/lib/fmt.js";

// lucide `scroll-text`
const ICON_RULES_SHAPES = [
  ["path", { d: "M15 12h-5" }],
  ["path", { d: "M15 8h-5" }],
  ["path", { d: "M19 17V5a2 2 0 0 0-2-2H4" }],
  ["path", { d: "M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3" }],
];

// rulesIcon is exported so every place that marks "there are rules here" draws the SAME
// mark — a stand-in glyph somewhere else is how a UI ends up with two icons for one idea.
export function rulesIcon() { return svgIcon(ICON_RULES_SHAPES, { cls: "rules-icon" }); }

// What each level is called TO A PERSON. "store"/"project"/"pad" is the vocabulary of the
// file layout; what a reader needs to know is how far the rule reaches.
function levelTitle(level, scope) {
  if (level === "store") return "Everywhere in this store";
  if (level === "project") return `Everywhere in project “${scope.project}”`;
  return "This pad only";
}

// What ticking `replace` actually does, said in terms of the levels it switches off — not
// as "the levels above", which means nothing to someone looking at one box.
function replaceLabel(level, scope) {
  if (level === "pad") {
    return scope.project
      ? `Ignore the store rules and “${scope.project}”'s rules — this pad follows only what is written here`
      : "Ignore the store and project rules — this pad follows only what is written here";
  }
  return "Ignore the store rules — pads in this project follow only what is written here";
}

// showRules opens the dialog.
//
//   scope: { kind: "pad"|"project"|"store", ref?, project? }
//   rules: the payload from the API ({ layers, text, digest, history })
//   opts:  { onSection(n), onChange(rules), startEditing?: "store"|"project"|"pad" }
//
// onSection is how "open §43" gets back to the transcript: the dialog closes and the page
// scrolls, because the modal is for reading the rules, not for reading the pad.
//
// startEditing opens straight into the editor for one level. A caller that ALREADY shows
// the rules — Settings does — would otherwise make the person pass through a read-only
// copy of what they were looking at before reaching the thing they clicked for.
export function showRules(scope, rules, opts = {}) {
  let current = rules || {};
  // Whether this dialog exists to EDIT or to READ. Opened from an "Edit" button the job
  // is done once it is saved, so it closes; opened for reading, saving goes back to the
  // reading view the person was in. Cancel follows the same rule — leaving them in a view
  // they never asked for is the same wrong turn in the other direction.
  const editOnly = !!opts.startEditing;
  const body = el("div", { class: "rules" });
  // The action row lives in the dialog's FOOTER, not in the body. Only the body scrolls,
  // so a Save button placed inside it slides off the bottom the moment the editor is
  // taller than the viewport — which is exactly when a person is most likely to want it.
  const footer = el("div", { class: "rules__actions" });

  const d = dialog({
    title: dialogTitle(scope),
    className: "rules-dialog",
    content: (host) => host.append(body),
    footer: (host) => host.append(footer),
  });

  function render() {
    const layers = current.layers || [];
    const parts = [];
    for (const level of ["store", "project", "pad"]) {
      const layer = layers.find((l) => l.level === level);
      if (!layer && !canEdit(scope, level)) continue;
      parts.push(layerBlock(scope, level, layer, { onEdit: () => edit(level, layer) }));
    }
    if (current.history?.length) {
      parts.push(el("div", { class: "rules__history" },
        el("span", { text: "Earlier versions of this pad's rules: " }),
        ...current.history.flatMap((n, i) => [
          i ? el("span", { text: " · " }) : null,
          el("button", {
            type: "button", class: "rules__link", text: `§${n}`,
            onclick: () => { d.close(); opts.onSection?.(n); },
          }),
        ].filter(Boolean)),
      ));
    }
    // The digest is a machine detail, so it goes last and says what it is FOR. A person
    // never types it; an agent quotes it to prove it read this version. Which POST it has
    // to quote on is the deployment's `rules.reack`, so the sentence does not promise one:
    // saying "on its first post" was true of one setting and quietly wrong under the other,
    // which is worse than saying less.
    if (current.digest) {
      parts.push(el("p", { class: "rules__digest" },
        el("span", { text: "An agent posting here quotes the code " }),
        el("code", { text: current.digest }),
        el("span", { text: ", which is how it proves it read this version of the rules." }),
      ));
    }
    if (!layers.length) {
      parts.push(el("p", { class: "muted", text: "No rules yet. Writing some is how a long pad stays readable." }));
    }
    setChildren(body, parts);
    setChildren(footer); // reading needs no actions; the × closes the dialog
  }

  // edit swaps the whole dialog body for a textarea. It is a mode rather than a second
  // dialog so the person keeps seeing which level they are editing, and cancelling puts
  // them back exactly where they were.
  function edit(level, layer) {
    const area = el("textarea", {
      class: "rules__editor", rows: 15, spellcheck: false,
      value: layer?.text || "",
    });
    const replace = el("input", { type: "checkbox", checked: !!layer?.replace });
    const status = el("div", { class: "rules__status" });
    // Announcing the change into the pads it binds. ON by default, because that is what
    // editing a rule is FOR: a version nobody is told about binds only whoever happens to
    // post next, and the person editing has no other way to reach an agent mid-task. The
    // case for announcing nothing — a typo fix — is the rarer one, and it is one click away.
    //
    // The count arrives from the server a moment later; the box is usable before it does,
    // because waiting on a number to allow a click makes the dialog feel broken on a slow
    // store.
    const notify = el("input", { type: "checkbox", checked: true });
    const notifyCount = el("span", { class: "muted", text: "" });
    // The pad level has nothing to announce: its rules ARE a section, and every waiter on
    // that pad wakes for it whatever their selectors say.
    const notifiable = level !== "pad";
    if (notifiable) {
      api.rulesNotifyTargets(level === "project" ? scope.project : null)
        .then((t) => { notifyCount.textContent = " " + notifySummary(t); })
        // A count that cannot be fetched must not read as zero. The box still works; only
        // the preview is missing, and saying so is better than a number nobody can trust.
        .catch(() => { notifyCount.textContent = " (could not count the pads this would reach)"; });
    }
    // The version this editor opened on. Held HERE rather than read at save time, because
    // that is the whole question the server is being asked: is what I am about to replace
    // still the thing I was shown when I started typing. It moves only when the person
    // has been SHOWN what changed underneath them — see the conflict branch below.
    let openedAt = current.versions?.[level] || "none";
    // Empty until a conflict fills it; declared before Save so the handler can reach it.
    const conflict = el("div", { class: "rules__conflict" });
    const save = el("button", {
      type: "button", class: "ghost-btn rules__save", text: "Save",
      onclick: async () => {
        save.disabled = true;
        status.textContent = "saving…";
        try {
          current = await write(scope, level, area.value, replace.checked, openedAt,
            notifiable && notify.checked);
          const sent = current.notified;
          if (editOnly) {
            // The caller repaints from onChange, so the page behind already shows the
            // new text; a toast says it landed without making them dismiss anything.
            d.close();
            toast(`${levelTitle(level, scope)} — rules saved${notifiedSuffix(sent)}`, { type: "success" });
            return;
          }
          if (sent) {
            // Shown even on the happy path. The person asked for other people's pads to be
            // written into; how many, and how many were left out, is the result of what
            // they just did — not a detail to go looking for.
            toast(`Rules saved${notifiedSuffix(sent)}`, { type: sent.failed ? "warn" : "success" });
          }
          render();
        } catch (err) {
          save.disabled = false;
          if (err.code !== "rules_conflict") {
            status.textContent = err.message || "could not save";
            return;
          }
          // A conflict is the one failure where the typed text must NOT be thrown away:
          // somebody else wrote these rules while this editor was open, and what is in
          // the box is the only copy of the person's change. So the editor stays and
          // their version appears under it to merge from — re-rendering the dialog here
          // would lose exactly the work the check exists to protect.
          //
          // Once it has been SHOWN, the editor is no longer stale: the next Save quotes
          // the version now on screen. Leaving openedAt behind would make the conflict
          // permanent, which teaches people to route around the check rather than merge.
          try {
            current = await refresh(scope);
            opts.onChange?.(current);
            openedAt = current.versions?.[level] || "none";
          } catch {
            status.textContent = err.message || "could not save";
            return;
          }
          const theirs = (current.layers || []).find((l) => l.level === level);
          status.textContent =
            "Someone else changed these rules while this was open. Your text is untouched —" +
            " merge in what you want from theirs, then save again.";
          setChildren(conflict,
            el("p", { class: "rules__conflict-head", text: "Saved meanwhile, by someone else:" }),
            theirs ? rulesBody(theirs.text) : el("p", { class: "muted rules__text", text: "They emptied this level." }),
          );
        }
      },
    });
    setChildren(body,
      el("p", { class: "rules__editing" },
        el("strong", { text: `Editing: ${levelTitle(level, scope)}` }),
      ),
      area,
      el("p", { class: "rules__hint muted", text: "Markdown. A short list of habits works better than prose." }),
      // No level above the store to ignore, so the option would be a checkbox that
      // does nothing.
      level === "store" ? null : el("label", { class: "rules__replace" }, replace,
        el("span", { text: " " + replaceLabel(level, scope) })),
      level === "pad"
        ? el("p", { class: "rules__hint muted", text: "Saved as a new section by “scratchpad”. The previous version stays in the pad as history, and this does not take anyone's turn." })
        : null,
      // Last thing above the buttons: it is a decision about the save, taken once the text
      // is written, not a property of the rules themselves.
      // The label and its count are ONE grid cell, wrapped together: the layout puts the
      // control in a column of its own, so a third child would drop onto a second row
      // underneath the checkbox rather than sitting after the text.
      notifiable
        ? el("label", { class: "rules__notify" }, notify,
            el("span", {}, el("span", { text: "Tell the agents on the affected pads" }), notifyCount))
        : null,
      notifiable
        ? el("p", { class: "rules__hint muted", text: "Adds a short notice to each pad, which wakes anyone waiting there. Agents already have to re-read changed rules before their next post; this is what reaches the ones that are mid-task." })
        : null,
      status,
      conflict,
    );
    setChildren(footer,
      el("button", {
        type: "button", class: "ghost-btn", text: "Cancel",
        onclick: () => (editOnly ? d.close() : render()),
      }),
      save,
    );
    area.focus();
  }

  // write persists one level and returns the refreshed rule set, so the dialog always
  // renders what the server actually stored rather than what was typed.
  //
  // ifDigest is the version of THAT level the editor opened on. It is per-level rather
  // than the combined digest above, because a person editing this pad's rules has no way
  // to resolve — and no business being blocked by — someone else's change to the store's.
  async function write(sc, level, text, replace, ifDigest, notify) {
    let out;
    if (level === "store") out = await api.setStoreRules(text, replace, ifDigest, notify);
    else if (level === "project") out = await api.setProjectRules(sc.project, text, replace, ifDigest, notify);
    else out = await api.setPadRules(sc.ref, text, replace, ifDigest);
    opts.onChange?.(out);
    return out;
  }

  // refresh re-reads the whole rule set for this scope. Used after a conflict, where the
  // dialog must show what is actually stored now — the failed write's own reply cannot,
  // since the server refused before writing anything.
  async function refresh(sc) {
    if (sc.kind === "pad") return (await api.pad(sc.ref)).rules || {};
    if (sc.kind === "project") return api.projectRules(sc.project);
    return api.storeRules();
  }

  if (opts.startEditing && canEdit(scope, opts.startEditing)) {
    edit(opts.startEditing, (current.layers || []).find((l) => l.level === opts.startEditing));
  } else {
    render();
  }
  d.show();
  return d;
}

// canEdit decides whether a level is offered for editing in this scope. A pad dialog can
// edit all three; a project dialog has no pad; Settings has only the store.
function canEdit(scope, level) {
  if (level === "store") return true;
  if (level === "project") return scope.kind !== "store" && !!scope.project;
  return scope.kind === "pad" && !!scope.ref;
}

// notifySummary is the count beside the checkbox: how many pads, out of how many, and why
// the rest are not included.
//
// The denominator and the reasons are not decoration. "12 pads" invites two opposite wrong
// conclusions — that the store only has 12, or that 300 are about to be written into — and
// the reasons are what tell a person their protected pad was left out BEFORE they rely on
// having reached it.
function notifySummary(t) {
  const total = t.in_scope || 0;
  const n = (t.refs || []).length;
  if (!total) return "(no pads here yet)";
  // Listed in a fixed order — the store's own (SkipOrder), which the CLI prints in too.
  // Iterating the object would follow whatever the JSON happened to hold, so the same
  // store would describe itself differently in two places, and neither line would diff.
  const why = [
    ["quiet", `quiet for over ${t.active_days}d`],
    ["continued", "already continued"],
    ["full", "at their section limit"],
  ];
  const skipped = why
    .filter(([reason]) => (t.skipped || {})[reason] > 0)
    .map(([reason, label]) => `${t.skipped[reason]} ${label}`);
  const tail = skipped.length ? ` — skipping ${skipped.join(", ")}` : "";
  return `(${n} of ${total}${tail})`;
}

// notifiedSuffix is the same honesty after the fact. A failure never turns the save into an
// error — the rules are stored by then — so this is the only place it can be said.
function notifiedSuffix(sent) {
  if (!sent) return "";
  const n = (sent.notified || []).length;
  const failed = Object.keys(sent.failed || {}).length;
  const tail = failed ? `, ${failed} could not be reached` : "";
  return ` — ${n} pad${n === 1 ? "" : "s"} told${tail}`;
}

function dialogTitle(scope) {
  if (scope.kind === "pad") return `Rules · ${scope.ref}`;
  if (scope.kind === "project") return `Rules · project “${scope.project}”`;
  return "Rules · this store";
}

// layerBlock renders one level with its provenance. The levels are never flattened into
// one blob: what a person needs from this dialog is not only WHAT the rules say but where
// to go to change a particular line.
function layerBlock(scope, level, layer, { onEdit }) {
  const head = el("div", { class: "rules__head" },
    el("span", { class: "rules__level", text: levelTitle(level, scope) }),
  );
  if (layer?.author) {
    head.append(el("span", { class: "rules__by", text: `§${layer.section} · ${layer.author} · ${relTime(layer.ts)}` }));
  } else {
    head.append(el("span", { class: "rules__by", text: layer ? layer.source : sourceHint(scope, level) }));
  }
  if (layer?.superseded) {
    head.append(el("span", { class: "rules__flag", text: "not in force — this pad replaces it" }));
  } else if (layer?.replace) {
    head.append(el("span", { class: "rules__flag", text: "replaces the wider rules" }));
  }
  head.append(el("span", { class: "rules__spacer" }));
  if (canEdit(scope, level)) {
    head.append(el("button", { type: "button", class: "rules__link", text: "Edit", onclick: onEdit }));
  }

  const block = el("section", { class: "rules__layer" + (layer?.superseded ? " rules__layer--off" : "") }, head);
  block.append(layer ? rulesBody(layer.text) : el("p", { class: "muted rules__text", text: "None yet." }));
  return block;
}

// rulesBody renders the rules as the markdown they are written in — a bullet list reads
// as a list, not as a wall of hyphens. The renderer builds nodes and never touches
// innerHTML, so it is safe for text an agent wrote.
export function rulesBody(text) {
  const md = el("puredashboard-markdown", { class: "rules__text" });
  md.value = text || "";
  return md;
}

// sourceHint names the file a level WOULD be written to when it does not exist yet, so
// "Edit" never opens onto a mystery location.
function sourceHint(scope, level) {
  if (level === "store") return "_rules.md";
  if (level === "project") return `projects/${scope.project}/_rules.md`;
  return "not written yet";
}

// rulesChip is the entry point shown on a page. It says the WORD, not the digest: a
// person wants to know whether this place has rules, and the digest is a token for
// agents that means nothing at a glance.
//
// It wears `ghost-btn`, the same clothes as Copy ref beside it. A control that opens
// something has to look like the other controls that open something — with its own
// pill shape and colour it read as a tag, i.e. as a fact about the pad rather than a
// button. Only the icon marks it out.
export function rulesChip(scope, getRules, opts = {}) {
  const label = el("span");
  const chip = el("button", { type: "button", class: "ghost-btn rules-chip" }, rulesIcon(), label);
  const paint = () => {
    const r = getRules() || {};
    const has = !!r.digest;
    label.textContent = has ? "Rules" : "No rules";
    chip.title = has
      ? `${(r.layers || []).length} level(s) apply here — click to read or edit`
      : "No rules here yet — click to write some";
    chip.classList.toggle("rules-chip--empty", !has);
  };
  chip.addEventListener("click", () => showRules(scope, getRules(), opts));
  paint();
  chip.repaint = paint;
  return chip;
}
