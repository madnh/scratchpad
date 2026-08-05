// One project — the same pad table as #/pads, scoped to a project, plus the rules that
// apply to every pad in it.
//
// It shares renderPadTable rather than duplicating it, so the two views cannot drift
// apart in columns, live behaviour or bulk actions.

import { renderPadTable } from "/pages/pads.js";
import { rulesChip } from "/components/rules-dialog.js";
import { api } from "/lib/api.js";
import { el } from "/lib/ui.js";

export default function mount(outlet, ctx) {
  const project = ctx.params.name;

  // The chip is mounted with the heading and filled in when the rules arrive: a page
  // that pops a control in after a round-trip moves under the reader's cursor, and this
  // one is a fetch away from being empty anyway.
  let rules = null;
  const chip = rulesChip({ kind: "project", project }, () => rules, {
    onChange: (next) => { rules = next; chip.repaint(); },
  });
  api.projectRules(project).then((r) => { rules = r; chip.repaint(); }).catch(() => {});

  // No subtitle: the default one counts the pads, which is a fact about this page. The
  // word "project" was neither — the heading IS the project's name, and the sidebar
  // already says where you are.
  // Searching THIS project is a link, not a control: the search page owns the question,
  // and arriving there with the project already filled in is the whole difference
  // between "search" and "search here". A real anchor, so it opens in a tab like any
  // other link on this page.
  const searchLink = el("a", {
    class: "ghost-btn", text: "Search this project",
    href: `#/search?project=${encodeURIComponent(project)}`,
  });

  return renderPadTable(outlet, {
    project,
    heading: project,
    actions: [searchLink, chip],
  });
}
