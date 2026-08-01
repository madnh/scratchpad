// Not found — a hash route that matches nothing.

import "/vendor/puredashboard/result.js";

import { el } from "/lib/ui.js";

export default function mount(outlet) {
  const res = el("puredashboard-result", {
    status: "404",
    title: "No such page",
    subtitle: "That address does not match anything in this UI.",
  });
  res.append(el("a", { href: "#/" }, el("button", { type: "button", text: "Go to Overview" })));
  outlet.replaceChildren(res);
}
