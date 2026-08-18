// Primary assertion: the tab-overflow popover must stay clear of the
// middle "Start an agent" panel. dockview's own shiftAbsoluteElementIntoView
// only keeps the popover inside the WHOLE workspace (never the one dock
// group it opened from - see harness.html's own comment), so an
// unconstrained popover width (a long tab title with no max-width/ellipsis)
// can still shift far enough left to cover the neighbour. dockview-theme.css's
// kata-491q rule (max-width + ellipsis on the tab's own content) is what
// keeps the popover narrow enough that this shift never needs to cross the
// boundary between panels.
export default function assert(measurement) {
  const { overlapX, overlapY } = measurement;
  if (overlapX > 1 && overlapY > 1) {
    // >1px, not >0, to stay clear of sub-pixel layout rounding noise.
    return {
      pass: false,
      reason: `tab-overflow popover overlaps the middle panel by ${overlapX.toFixed(1)}x${overlapY.toFixed(1)}px (dropdown=${JSON.stringify(measurement.dropdown)}, mainPanel.right=${measurement.mainPanel.right.toFixed(1)}) - dockview-theme.css is missing (or has lost) its kata-491q max-width/ellipsis rule on .dv-tabs-overflow-container`,
    };
  }
  return {
    pass: true,
    reason: `popover clears the middle panel by ${(-overlapX).toFixed(1)}px horizontally`,
  };
}
