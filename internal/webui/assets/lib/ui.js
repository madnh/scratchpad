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

// svgIcon builds a lucide glyph from its path data, as NODES.
//
// Icons are built rather than parsed from markup for the same reason everything else
// here is: nothing in this UI reaches the DOM through innerHTML, so there is no
// markup-parsing path to keep an eye on at all. The CSP also forbids fetching an icon
// set, so the handful we use are inlined.
const SVG_NS = "http://www.w3.org/2000/svg";

export function svgIcon(shapes, { size = "1em", cls = "icon" } = {}) {
  const svg = document.createElementNS(SVG_NS, "svg");
  for (const [k, v] of Object.entries({
    viewBox: "0 0 24 24", width: size, height: size, fill: "none",
    stroke: "currentColor", "stroke-width": "2", "stroke-linecap": "round",
    "stroke-linejoin": "round", "aria-hidden": "true", class: cls,
  })) svg.setAttribute(k, v);
  for (const [tag, attrs] of shapes) {
    const node = document.createElementNS(SVG_NS, tag);
    for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
    svg.appendChild(node);
  }
  return svg;
}

// lucide `copy`
export const ICON_COPY_SHAPES = [
  ["rect", { width: "14", height: "14", x: "8", y: "8", rx: "2", ry: "2" }],
  ["path", { d: "M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" }],
];
// lucide `check`
const ICON_CHECK_SHAPES = [["path", { d: "M20 6 9 17l-5-5" }]];

// copyIconButton copies a value from beside the value itself. A labelled button in a
// row of actions says "Copy ref" and leaves you to work out WHICH ref; an icon sitting
// against the id copies the thing it is touching, and needs no label to say so.
export function copyIconButton(value, title = "Copy") {
  const btn = el("button", { type: "button", class: "inline-icon-btn", title, "aria-label": title },
    svgIcon(ICON_COPY_SHAPES));
  btn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(value);
      btn.replaceChildren(svgIcon(ICON_CHECK_SHAPES));
      btn.classList.add("inline-icon-btn--done");
      setTimeout(() => {
        btn.replaceChildren(svgIcon(ICON_COPY_SHAPES));
        btn.classList.remove("inline-icon-btn--done");
      }, 1200);
    } catch { /* clipboard blocked; the value is on screen anyway */ }
  });
  return btn;
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
