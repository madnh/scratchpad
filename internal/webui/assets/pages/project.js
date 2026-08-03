// One project — the same pad table as #/pads, scoped to a project, plus the rules that
// apply to every pad in it.
//
// It shares renderPadTable rather than duplicating it, so the two views cannot drift
// apart in columns, live behaviour or bulk actions.

import { renderPadTable } from "/pages/pads.js";
import { rulesChip } from "/components/rules-dialog.js";
import { api } from "/lib/api.js";

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
  return renderPadTable(outlet, {
    project,
    heading: project,
    actions: [chip],
  });
}
