export default function assert(m) {
  const inside = (inner, outer, tolerance = 1) =>
    inner.left >= outer.left - tolerance &&
    inner.right <= outer.right + tolerance &&
    inner.top >= outer.top - tolerance &&
    inner.bottom <= outer.bottom + tolerance;

  const failures = [];
  const vp = m.viewport;

  // The mobile panel is fixed to the viewport; a panel escaping it is the
  // whole-body layout failure, not a pane-level one.
  if (m.panel.left < -1 || m.panel.right > vp.w + 1 || m.panel.top < -1 || m.panel.bottom > vp.h + 1) {
    failures.push(`panel escapes the viewport: ${JSON.stringify(m.panel)} in ${vp.w}x${vp.h}`);
  }

  // The footer's actions stay reachable: a picker whose confirm and cancel
  // sit below the fold cannot commit or escape.
  for (const [label, rect] of [
    ["cancel", m.cancel],
    ["use this folder", m.useFolder],
  ]) {
    if (rect.bottom > vp.h + 1 || rect.top < -1 || rect.height <= 0) {
      failures.push(`footer ${label} is not reachable in the viewport: ${JSON.stringify(rect)}`);
    }
  }

  // The browse pane keeps its primary controls in its box: the aside's
  // basis is bounded to a 40% share of the row, so the browse pane keeps
  // the majority however long the recents list is, and the path input
  // and the New folder affordance stay inside its box.
  if (!inside(m.pathInput, m.browse)) {
    failures.push(`path input escapes the browse pane: input ${JSON.stringify(m.pathInput)} vs browse ${JSON.stringify(m.browse)}`);
  }
  if (!inside(m.newFolder, m.browse)) {
    failures.push(`New folder button escapes the browse pane: ${JSON.stringify(m.newFolder)} vs browse ${JSON.stringify(m.browse)}`);
  }

  // The invariant the fix's comment states: the browse pane keeps the
  // majority of the row (a partial regression that still crushes the pane
  // toward a minority share must fail too, not only the total collapse).
  if (m.browse.clientHeight <= m.recents.clientHeight) {
    failures.push(
      `browse pane is not the row's majority: browse ${m.browse.clientHeight}px vs recents ${m.recents.clientHeight}px`,
    );
  }

  // Both columns scroll inside the bounded body instead of stretching it:
  // the row itself stays within the panel and neither pane's content can
  // make the whole body scroll (the footer must never move off-screen).
  if (m.body.scrollHeight > m.body.clientHeight + 1) {
    failures.push(`body scrolls: ${m.body.scrollHeight}px content in ${m.body.clientHeight}px body`);
  }
  if (m.recents.overflowY !== "auto" || m.browse.overflowY !== "auto") {
    failures.push(`pane overflow contract broken: recents ${m.recents.overflowY}, browse ${m.browse.overflowY}`);
  }
  if (m.recents.bottom > m.panel.bottom + 1 || m.browse.bottom > m.panel.bottom + 1) {
    failures.push(`a pane escapes the panel: recents ${JSON.stringify(m.recents)} browse ${JSON.stringify(m.browse)} panel ${JSON.stringify(m.panel)}`);
  }

  return failures.length === 0
    ? {
        pass: true,
        reason: `phone picker keeps browse ${Math.round(m.browse.clientHeight)}px and recents ${Math.round(m.recents.clientHeight)}px inside a ${Math.round(m.body.clientHeight)}px body, footer in viewport`,
      }
    : { pass: false, reason: failures.join("; ") };
}
