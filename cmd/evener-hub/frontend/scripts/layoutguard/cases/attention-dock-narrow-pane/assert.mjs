// The dormant dock is the attention control's other placement, and the pane it
// sits in can be far narrower than the viewport: a two-column cascade beside
// the rail leaves ~300px on a laptop. The dock must keep the control inside the
// pane it belongs to, so a long next-session title truncates inside its own
// ceiling instead of growing the dock out past the scaffold's clipped edge.
//
// The label check keeps the fixture honest: if the title stops overflowing its
// slot, this case would pass without exercising anything.
const TOLERANCE = 1;

export default function assert(measurement) {
  const { pane, dock, next, label } = measurement;
  const failures = [];

  const spill = Math.max(pane.left - Math.min(dock.left, next.left), dock.right - pane.right, next.right - pane.right);
  if (spill > TOLERANCE) {
    failures.push(
      `the dock sits ${spill.toFixed(1)}px outside the ${pane.width.toFixed(0)}px pane (dock ${dock.left.toFixed(1)}..${dock.right.toFixed(1)}, Next ${next.left.toFixed(1)}..${next.right.toFixed(1)}, pane ${pane.left.toFixed(1)}..${pane.right.toFixed(1)}) - session.module.css's .attentionDock needs both inline anchors so the control's ceiling is the pane, not the viewport`,
    );
  }
  if (label.scrollWidth <= label.clientWidth + TOLERANCE) {
    failures.push(
      `the fixture's title fits its ${label.clientWidth}px slot, so this case proves nothing about a long target title`,
    );
  }

  return failures.length === 0
    ? {
        pass: true,
        reason: `the control stays inside the ${pane.width.toFixed(0)}px pane (Next ${next.width.toFixed(1)}px wide) and the title truncates in its ${label.clientWidth}px slot (${label.scrollWidth}px of text)`,
      }
    : { pass: false, reason: failures.join("; ") };
}
