// Settings — notification behaviour, appearance, the watch list, and what this UI is
// actually looking at.
//
// The diagnostics block is deliberate: "why am I not getting updates?" should be
// answerable here rather than by reading the server's log.

import "/vendor/puredashboard/card.js";
import "/vendor/puredashboard/segmented.js";
import "/vendor/puredashboard/descriptions.js";
import "/vendor/puredashboard/alert.js";
import "/vendor/puredashboard/empty.js";
import { toast } from "/vendor/puredashboard/toast.js";

import { showRules, rulesBody } from "/components/rules-dialog.js";
import { api } from "/lib/api.js";
import { connectionStatus } from "/lib/bus.js";
import * as notify from "/lib/notify.js";
import * as wl from "/lib/watchlist.js";
import * as prefs from "/lib/prefs.js";
import { el, pageHead, skeleton, errorView, setChildren } from "/lib/ui.js";

export default function mount(outlet) {
  outlet.replaceChildren(skeleton(5));
  let disposed = false;

  const load = async () => {
    let status;
    try {
      status = await api.status();
    } catch (err) {
      if (!disposed) outlet.replaceChildren(errorView(err, load));
      return;
    }
    if (disposed) return;
    outlet.replaceChildren(
      pageHead("Settings"),
      readingCard(),
      notificationsCard(),
      deploymentCard(),
      storeRulesCard(),
      watchListCard(),
      diagnosticsCard(status),
    );
  };

  load();
  const off = wl.onChange(() => { if (!disposed) load(); });
  return () => { disposed = true; off(); };
}

// readingCard holds the choices about how a pad READS. They are per person and stay in
// this browser — the pad itself has no opinion about which end you start from, and a
// link you send someone must open the same way for them as it does for you.
function readingCard() {
  const card = el("puredashboard-card", { title: "Reading" });

  const order = el("puredashboard-segmented");
  order.options = [
    { value: "newest", label: "Newest first" },
    { value: "oldest", label: "Oldest first" },
  ];
  order.value = prefs.order();
  order.addEventListener("change", (e) => {
    prefs.setOrder(e.target.value);
    toast(e.target.value === "newest"
      ? "Pads open at the newest section"
      : "Pads read from the first section", { type: "info" });
  });

  const sticky = el("puredashboard-segmented");
  sticky.options = [
    { value: "on", label: "Follow the page" },
    { value: "off", label: "Stay at the top" },
  ];
  sticky.value = prefs.stickyBar() ? "on" : "off";
  sticky.addEventListener("change", (e) => {
    prefs.setStickyBar(e.target.value === "on");
    toast(e.target.value === "on"
      ? "The pad toolbar follows you down the page"
      : "The pad toolbar stays at the top of the page", { type: "info" });
  });

  const outline = el("puredashboard-segmented");
  outline.options = [
    { value: "on", label: "Show the outline" },
    { value: "off", label: "Transcript only" },
  ];
  outline.value = prefs.outline() ? "on" : "off";
  outline.addEventListener("change", (e) => {
    prefs.setOutline(e.target.value === "on");
    toast(e.target.value === "on"
      ? "Pads show their section outline"
      : "Pads open without the outline", { type: "info" });
  });

  card.append(
    el("p", { class: "muted", text: "Which end of a pad to start reading from:" }),
    order,
    el("p", {
      class: "muted",
      text: "A pad's own page has the same switch, next to “Expand all” — changing it either " +
        "way changes it everywhere.",
    }),
    el("p", { class: "muted", text: "The toolbar that filters, jumps and reorders:" }),
    sticky,
    el("p", {
      class: "muted",
      text: "Following the page keeps those controls reachable in a pad hundreds of sections " +
        "long, and carries the pad's name and actions once its header has scrolled away.",
    }),
    el("p", { class: "muted", text: "The index of sections beside a pad:" }),
    outline,
    el("p", {
      class: "muted",
      text: "The outline lists every section of the pad, not just the ones loaded, and marks " +
        "where you are. A narrow window hides it whatever this says — the pad's toolbar has " +
        "an “Outline” button to bring it back.",
    }),
  );
  return card;
}

function notificationsCard() {
  const card = el("puredashboard-card", { title: "Notifications" });
  const state = notify.permission();

  if (state === "unsupported") {
    const a = el("puredashboard-alert");
    a.type = "warning";
    a.message = "This browser does not support notifications. In-app toasts still work.";
    card.append(a);
  } else if (state === "denied") {
    const a = el("puredashboard-alert");
    a.type = "warning";
    a.title = "Notifications are blocked";
    a.message = "Re-allow them for this site in the browser's site settings; the page cannot ask again once denied.";
    card.append(a);
  } else if (state === "default") {
    card.append(el("button", {
      type: "button", text: "Enable browser notifications",
      onclick: async () => {
        const result = await notify.request();
        toast(result === "granted" ? "Notifications enabled" : `Notifications ${result}`,
          { type: result === "granted" ? "success" : "warn" });
      },
    }));
  } else {
    const a = el("puredashboard-alert");
    a.type = "success";
    a.message = "Notifications are enabled.";
    card.append(a);
  }

  const scope = el("puredashboard-segmented");
  scope.options = [
    { value: "watched", label: "Watched pads" },
    { value: "all", label: "All pads" },
    { value: "off", label: "Off" },
  ];
  scope.value = wl.notifyScope();
  scope.addEventListener("change", (e) => {
    wl.setNotifyScope(e.target.value);
    toast(`Notifying for: ${e.target.value}`, { type: "info" });
  });

  // The same choice an agent makes with `wake_for`, in the same words. With five agents
  // in a pad most of what arrives belongs to two of them, and a person watching that pad
  // has exactly the agents' problem — so it gets the agents' answer rather than a second
  // model invented for the browser.
  const filter = el("puredashboard-segmented");
  filter.options = [
    { value: "any", label: "Everything" },
    { value: "tasks", label: "Tasks only" },
    { value: "task", label: "One task" },
    { value: "overdue", label: "Only when overdue" },
  ];
  filter.value = wl.notifyFilter();

  // The number belongs to "One task" and appears with it: a field that does nothing
  // where it stands is a question about the setting, not an answer.
  const taskField = el("label", { class: "field-row" },
    el("span", { class: "muted", text: "Task number" }),
    el("input", {
      type: "number", min: "1", step: "1", value: String(wl.notifyTask() || 1),
      // `change`, not `input`: writing the preference re-renders this page, so
      // announcing every keystroke would take the field's focus away mid-number.
      onchange: (e) => {
        const n = Number(e.target.value);
        wl.setNotifyTask(n);
        if (n > 0) toast(`Notifying about T${n}`, { type: "info" });
      },
    }),
  );
  taskField.hidden = filter.value !== "task";

  filter.addEventListener("change", (e) => {
    wl.setNotifyFilter(e.target.value);
    if (e.target.value === "task" && !wl.notifyTask()) wl.setNotifyTask(1);
  });

  card.append(
    el("p", { class: "muted", text: "Which pads may announce anything:" }),
    scope,
    el("p", { class: "muted", text: "And what is worth interrupting you about:" }),
    filter,
    taskField,
    el("p", {
      class: "muted",
      text: filter.value === "task"
        // Said plainly rather than left to be discovered: T3 in one pad is not T3 in
        // another, so the number only pins down one task once the scope does.
        ? "Task numbers belong to a pad, so this matches T" + (wl.notifyTask() || 1) +
          " in every pad the scope above allows — narrow it to watched pads to follow one."
        : filter.value === "overdue"
          ? "Checked once a minute against what has gone unanswered across the store, " +
            "and announced when an assignment first crosses the line — the browser's " +
            "version of pad wait --unacked."
          : "Mirrors the agents' --wake-for: reading is never filtered, only being interrupted is.",
    }),
    // Stated up front rather than discovered: nothing here can outlive the tab.
    el("p", {
      class: "muted",
      text: "Notifications arrive while a tab of this UI is open — a background tab is fine, " +
        "a closed browser is not. Keep a pinned tab if you want to be told while working elsewhere.",
    }),
  );
  return card;
}

function watchListCard() {
  const card = el("puredashboard-card", { title: "Watched pads" });
  const refs = [...wl.watched()];
  if (!refs.length) {
    card.append(el("puredashboard-empty", {
      compact: true,
      description: "Not watching any pad. Turn on Watch from a pad's page or the pads table.",
    }));
    return card;
  }
  const list = el("div");
  for (const ref of refs) {
    list.append(el("div", { class: "pad__meta" },
      el("a", { href: `#/pads/${ref}`, class: "ref", text: ref }),
      el("button", {
        type: "button", class: "ghost-btn", text: "Stop watching",
        onclick: () => { wl.setWatched(ref, false); toast(`Stopped watching ${ref}`, { type: "info" }); },
      }),
    ));
  }
  card.append(list);
  return card;
}

// deploymentCard edits the marker file — the settings that belong to the STORE rather
// than to this browser. Everything above it is a preference that lives in localStorage
// and follows the person; everything in here follows the deployment, and every agent
// working against it feels the change.
//
// The two halves are separated on purpose. What is editable takes effect on every process
// using this store within a moment, because they all watch the file. What is not either
// names something a running process has already bound (a port, a socket) or decides who
// may rewrite the rules — a browser session must not be the thing that grants that.
function deploymentCard() {
  const card = el("puredashboard-card", { title: "Deployment settings" });
  const body = el("div");
  let digest = "";
  // What a save will send, refreshed by paint(). The Save button is built ONCE and
  // appended as a direct child of the card, because that is what the card projects into
  // its header — nested inside `body` it is not projected, and lands at the top of the
  // form instead, above the fields it applies to.
  let fields = null;

  // An empty field means "use the built-in default", exactly as a missing line in the
  // marker does — so the default shows as a placeholder rather than being filled in.
  // Filling it in would turn a default into an explicit setting on the first save, and
  // this deployment would stop following the built-in one if it ever changed.
  const numField = (label, value, def, hint) => {
    const input = el("input", {
      type: "number", min: "0", step: "1",
      value: value ? String(value) : "",
      placeholder: String(def),
    });
    const row = el("label", { class: "setting-row" },
      el("span", { class: "muted", text: label }),
      input,
    );
    return { input, node: hint ? el("div", {}, row, el("p", { class: "muted", text: hint })) : row };
  };

  const textField = (label, value, def, hint) => {
    const input = el("input", { type: "text", value: value || "", placeholder: def });
    const row = el("label", { class: "setting-row" },
      el("span", { class: "muted", text: label }),
      input,
    );
    return { input, node: hint ? el("div", {}, row, el("p", { class: "muted", text: hint })) : row };
  };

  // Reads a number field back. A blank is 0 — "the default". Anything that is not a
  // whole number ≥ 0 is refused by name instead of being coerced: silently saving 0 for
  // what somebody typed would set the limit to the default and call it success.
  const readNum = (field, label) => {
    const v = field.input.value.trim();
    if (v === "") return 0;
    const n = Number(v);
    if (!Number.isInteger(n) || n < 0) throw new Error(`${label} must be a whole number, or empty for the default`);
    return n;
  };

  const save = el("button", {
    type: "button", text: "Save settings", disabled: true,
    "data-card-extra": "", // the card projects this into its header
    onclick: async () => {
      if (!fields) return;
      const f = fields;
      let payload;
      try {
        payload = {
          display_name: f.name.input.value.trim(),
          default_project: f.project.input.value.trim(),
          limits: {
            max_title_kb: readNum(f.titleKB, "Title, KB"),
            max_content_kb: readNum(f.contentKB, "Content per section, KB"),
            max_sections_per_pad: readNum(f.sections, "Sections per pad"),
            max_pads_per_project: readNum(f.padsPer, "Pads per project"),
          },
          wait: {
            default_s: readNum(f.waitDefault, "Default wait, seconds"),
            max_s: readNum(f.waitMax, "Longest wait, seconds"),
          },
        };
      } catch (err) {
        toast(err.message, { type: "warn" });
        return;
      }
      save.disabled = true;
      try {
        const next = await api.setConfig(payload, digest);
        paint(next);
        toast("Settings saved — every process using this store picks them up within a moment",
          { type: "success" });
      } catch (err) {
        save.disabled = false;
        if (err.code === "config_stale") {
          // Somebody else saved in between. Re-read rather than overwrite them, and
          // say so — the same remedy the rules dialog offers on its own conflict.
          toast("Someone else changed these settings; reloading the current values", { type: "warn" });
          api.config().then(paint).catch(() => {});
          return;
        }
        toast(err.message, { type: "error" });
      }
    },
  });
  card.append(save, body);

  const paint = (data) => {
    digest = data.digest;
    const c = data.config, d = data.defaults;

    const name = textField("Deployment name", c.display_name, d.display_name,
      "Shown in this UI's header and by scratchpad doctor.");
    const project = textField("Default project", c.default_project, d.default_project,
      "Used when a command or an MCP tool call omits the project.");

    const titleKB = numField("Title, KB", c.limits.max_title_kb, d.limits.max_title_kb);
    const contentKB = numField("Content per section, KB", c.limits.max_content_kb, d.limits.max_content_kb);
    const sections = numField("Sections per pad", c.limits.max_sections_per_pad, d.limits.max_sections_per_pad);
    const padsPer = numField("Pads per project", c.limits.max_pads_per_project, d.limits.max_pads_per_project);

    const waitDefault = numField("Default wait, seconds", c.wait.default_s, d.wait.default_s);
    const waitMax = numField("Longest wait, seconds", c.wait.max_s, d.wait.max_s);

    fields = { name, project, titleKB, contentKB, sections, padsPer, waitDefault, waitMax };
    save.disabled = false;

    setChildren(body,
      name.node,
      project.node,
      el("p", { class: "muted", text: "How much one section may carry, and how much a pad or a project may hold:" }),
      titleKB.node,
      contentKB.node,
      sections.node,
      padsPer.node,
      el("p", {
        class: "muted",
        text: "A pad that has reached its section limit refuses the next post — raise this rather " +
          "than losing the transcript. Leave a field empty to follow the built-in default shown in it.",
      }),
      el("p", { class: "muted", text: "How long an agent's pad_wait may block before it must return:" }),
      waitDefault.node,
      waitMax.node,
      el("p", {
        class: "muted",
        text: "The cap exists because an MCP call has to answer inside the host's own timeout. " +
          "The CLI's pad wait is not bounded by it.",
      }),
      coldBlock(data.cold),
    );
  };

  api.config()
    .then(paint)
    .catch((err) => setChildren(body,
      el("p", { class: "muted", text: `Could not read the deployment settings: ${err.message}` })));

  return card;
}

// coldBlock shows the settings this page will not write, and says why in one line each.
// Hiding them would leave "where do I change the port?" unanswerable from the UI; showing
// them without the reason would read as a bug.
function coldBlock(cold) {
  const d = el("puredashboard-descriptions");
  d.columns = 1;
  d.items = [
    { label: "Instance", value: cold.instance },
    { label: "Store", value: cold.projects_dir },
    { label: "Socket", value: cold.socket_path },
    { label: "UI port", value: String(cold.ui_port) },
    { label: "MCP TCP port", value: String(cold.tcp_port) },
    {
      label: "Who may write the rules",
      value: `store: ${cold.rules.store}, project: ${cold.rules.project}, pad: ${cold.rules.pad}`,
    },
  ];
  return el("div", {},
    el("p", { class: "muted", text: "Fixed while this process runs:" }),
    d,
    el("p", {
      class: "muted",
      text: "Ports and the socket name are already bound, so changing them here could not be " +
        "honoured. The rules policy is left out for a different reason: it decides whether an " +
        `agent may rewrite the operator's instructions, and a browser session is not how that ` +
        "is granted. Edit " + cold.marker_file + " and restart.",
    }),
  );
}

// storeRulesCard is where the store-wide rules live, because they are a property of this
// deployment rather than of any one pad — the same reason the store path and the watcher
// mode are on this page.
//
// It shows the digest and opens the shared dialog rather than rendering the text inline:
// one place edits rules, at every level, so the pad view and this page cannot grow two
// different ideas of what editing them does.
function storeRulesCard() {
  const card = el("puredashboard-card", { title: "Rules for every pad in this store" });
  const box = el("div", { class: "rules rules--inline" }, el("p", { class: "muted", text: "loading…" }));
  let rules = null;

  // Edit opens the EDITOR, not a read-only copy of what is already on screen. The card
  // shows the rules in place, so a dialog that then made you press "Edit" a second time
  // would be a step that shows you nothing you were not already looking at.
  const open = el("button", {
    type: "button", class: "ghost-btn", text: "Edit", disabled: true,
    "data-card-extra": "", // the card projects this into its header
    onclick: () => showRules({ kind: "store" }, rules, {
      startEditing: "store",
      onChange: (next) => { rules = next; paint(); },
    }),
  });

  // The rules themselves, not a summary of them: this card is short, and a person who
  // came here to check what the store asks of its agents should not have to open a
  // dialog to find out. The dialog is for CHANGING them.
  const paint = () => {
    const layer = (rules?.layers || []).find((l) => l.level === "store");
    setChildren(box, layer
      ? rulesBody(layer.text)
      : el("p", { class: "muted", text: "None yet — agents joining a pad here are told nothing about how this store works." }));
    open.disabled = false;
  };
  api.storeRules()
    .then((r) => { rules = r; paint(); })
    .catch(() => setChildren(box, el("p", { class: "muted", text: "Could not read the rules." })));

  card.append(open, box);
  return card;
}

function diagnosticsCard(status) {
  const card = el("puredashboard-card", { title: "This instance" });

  const d = el("puredashboard-descriptions");
  d.columns = 1;
  d.items = [
    { label: "Deployment", value: status.display_name },
    { label: "Store", value: status.projects_dir },
    { label: "Version", value: status.version },
    { label: "Surface", value: status.read_only ? "read-only for the conversation (posting is an agent surface); rules are editable" : "read-write" },
    { label: "Live stream", value: connectionStatus() },
    {
      label: "Change detection",
      value: status.watcher === "push"
        ? "kernel filesystem events (instant)"
        : "periodic rescan — filesystem notification is unavailable here, so changes appear within 30s",
    },
    {
      label: "Settings reload",
      value: status.config_watcher === "push"
        ? "kernel filesystem events (instant)"
        : "periodic rescan — a saved setting still applies, within 30s",
    },
  ];
  card.append(d);

  if (status.watcher !== "push") {
    const a = el("puredashboard-alert");
    a.type = "warning";
    a.title = "Running on the rescan fallback";
    a.message = "Updates still arrive, just later. This usually means the store is on a filesystem " +
      "that does not support change notification (some network or container filesystems).";
    card.append(a);
  }
  return card;
}
