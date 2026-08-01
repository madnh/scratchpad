// One project — the same pad table as #/pads, scoped to a project.
//
// It shares renderPadTable rather than duplicating it, so the two views cannot drift
// apart in columns, live behaviour or bulk actions.

import { renderPadTable } from "/pages/pads.js";

export default function mount(outlet, ctx) {
  const project = ctx.params.name;
  return renderPadTable(outlet, {
    project,
    heading: project,
    subtitle: "project",
  });
}
