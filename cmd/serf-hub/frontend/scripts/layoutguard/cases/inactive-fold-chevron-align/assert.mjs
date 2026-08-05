// Primary assertion: the fold's chevron gutter starts at exactly the parent
// session label's x - Rail.module.css's .inactiveFold padding exists solely to
// make this hold; delete it (or drop the dotted-parent :has(.signal) override)
// and the fold's chevron falls back to one raw nesting level, left of the
// label. 1px tolerance, not 0, to stay clear of sub-pixel rounding noise.
export default function assert(measurement) {
  for (const kind of ["dotted", "quiet"]) {
    const { label, chevron } = measurement[kind];
    const drift = chevron - label;
    if (Math.abs(drift) > 1) {
      return {
        pass: false,
        reason: `${kind} parent: fold chevron gutter sits ${drift.toFixed(1)}px from the parent label's x (chevron.left=${chevron.toFixed(1)}, label.left=${label.toFixed(1)}) - Rail.module.css's .inactiveFold alignment is off`,
      };
    }
  }
  return {
    pass: true,
    reason: `fold chevron aligns to the parent label for a dotted parent (${(measurement.dotted.chevron - measurement.dotted.label).toFixed(1)}px drift) and a quiet one (${(measurement.quiet.chevron - measurement.quiet.label).toFixed(1)}px drift)`,
  };
}
