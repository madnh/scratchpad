// Projects — the pad namespaces, ordered by what is alive.

import "/vendor/puredashboard/card.js";
import "/vendor/puredashboard/empty.js";
import "/vendor/puredashboard/statistic.js";

import { api } from "/lib/api.js";
import { onPad } from "/lib/bus.js";
import { el, pageHead, skeleton, errorView } from "/lib/ui.js";
import { relTime, absTime } from "/lib/fmt.js";

export default function mount(outlet) {
  outlet.replaceChildren(skeleton(4));
  let disposed = false;

  const load = async () => {
    let projects;
    try {
      ({ projects } = await api.projects());
    } catch (err) {
      if (!disposed) outlet.replaceChildren(errorView(err, load));
      return;
    }
    if (disposed) return;

    projects.sort((a, b) => (b.last_ts || 0) - (a.last_ts || 0));

    const grid = el("div", { class: "stat-row" },
      projects.map((p) => {
        const stat = el("puredashboard-statistic");
        stat.title = p.name;
        stat.value = p.pad_count;
        stat.suffix = p.pad_count === 1 ? " pad" : " pads";

        const card = el("puredashboard-card", {},
          el("a", { href: `#/projects/${encodeURIComponent(p.name)}` }, stat),
          el("div", { class: "muted", title: absTime(p.last_ts), text: `last activity ${relTime(p.last_ts)}` }),
        );
        return card;
      }),
    );

    outlet.replaceChildren(
      pageHead("Projects", `${projects.length} project${projects.length === 1 ? "" : "s"}`),
      projects.length ? grid : el("puredashboard-empty", { description: "No projects yet" }),
    );
  };

  load();
  const off = onPad(() => load());
  return () => { disposed = true; off(); };
}
