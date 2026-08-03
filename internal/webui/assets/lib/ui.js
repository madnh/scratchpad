// ui.js — DOM helpers shared by the pages.
//
// Text always goes in through textContent, never innerHTML: pad titles and author
// names are written by agents, i.e. untrusted input.

import { agentColorIndex, agentInitials } from "/lib/fmt.js";

export function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null) continue;
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k === "dataset") Object.assign(node.dataset, v);
    else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
    else if (k in node) node[k] = v;
    else node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c != null) node.append(c);
  }
  return node;
}

// setChildren replaces a node's children, DROPPING the absent ones.
//
// It exists because `replaceChildren` does not: passing it a null — which is what a
// conditional block like "the stuck list, when there is one" naturally returns —
// appends the literal string "null" to the page.
export function setChildren(node, ...children) {
  node.replaceChildren(...children.flat().filter((c) => c != null));
}

// pageHead builds the title row every page starts with.
export function pageHead(title, subtitle, ...actions) {
  return el("div", { class: "page__head" },
    el("h1", { class: "page__title", text: title }),
    subtitle ? el("span", { class: "page__sub", text: subtitle }) : null,
    el("div", { class: "page__spacer" }),
    ...actions,
  );
}

// skeleton is the placeholder a page shows while its first fetch is in flight — the
// app shell has already painted, so this is the only part that can be pending.
export function skeleton(lines = 4) {
  return el("puredashboard-skeleton", { lines });
}

// errorView renders a failed fetch as a result page rather than a silent blank.
export function errorView(err, retry) {
  const res = el("puredashboard-result", {
    status: err?.status === 404 ? "404" : "error",
    title: err?.code === "pad_not_found" ? "No such pad" : "Something went wrong",
    subtitle: err?.message || String(err),
  });
  if (retry) {
    res.append(el("button", { type: "button", text: "Try again", onclick: retry }));
  }
  return res;
}

// tag builds a small labelled chip.
export function tag(text, color = "default") {
  return el("puredashboard-tag", { color, text });
}

// agentChips renders a pad's roster — everyone who has posted, in the order they first
// appeared. Each agent carries the colour its transcript avatar has, so the same handle
// is recognisable in the pads table, in a pad's header and beside its sections.
//
// `avatar` picks the marker. The pad's own page uses the transcript's lettered disc, so
// the roster and the messages below it show the same faces. A table row uses the plain
// dot instead: a column of discs reads as a column ABOUT avatars, and twenty of them
// down a page of rows is louder than the data they sit next to.
//
// `max` bounds long rosters into a "+N"; the full list stays in the title either way.
export function agentChips(list, { max = Infinity, avatar = false } = {}) {
  const all = Array.isArray(list) ? list : [];
  const box = el("span", { class: "agents", title: all.join(", ") });
  for (const name of all.slice(0, max)) {
    const mark = avatar
      ? el("span", { class: "agents__avatar", text: agentInitials(name), "aria-hidden": "true" })
      : el("span", { class: "agents__dot" });
    mark.style.setProperty("--avatar-bg", `var(--avatar-c${agentColorIndex(name)})`);
    box.append(el("span", { class: "agents__one" }, mark, el("span", { text: name })));
  }
  if (all.length > max) {
    box.append(el("span", { class: "agents__more", text: `+${all.length - max}` }));
  }
  return box;
}

// link builds a real anchor — hash routing means every navigation target is a normal
// link, so middle-click and ⌘-click behave.
export function link(href, text, cls) {
  return el("a", { href, text, class: cls });
}

// copyButton copies a value and confirms it in place.
export function copyButton(value, label = "Copy ref") {
  return el("button", {
    type: "button", class: "ghost-btn", text: label,
    onclick: async (e) => {
      try {
        await navigator.clipboard.writeText(value);
        const btn = e.currentTarget;
        btn.textContent = "Copied";
        setTimeout(() => { btn.textContent = label; }, 1200);
      } catch { /* clipboard blocked; the value is on screen anyway */ }
    },
  });
}
