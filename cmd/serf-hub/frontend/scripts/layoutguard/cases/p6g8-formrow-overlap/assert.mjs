// Primary assertion: the working-directory trigger must stay inside its own
// flex container (.cfgDir). This is the direct, width-independent invariant
// that formrow.module.css's `.root { min-width: 0 }` exists to guarantee -
// remove it and the trigger's nowrap content forces the box wider than its
// container allows to shrink to.
export default function assert(measurement) {
  const overflow = measurement.triggerOverflowsCfgDir;
  if (overflow > 1) {
    // >1px, not >0, to stay clear of sub-pixel layout rounding noise.
    return {
      pass: false,
      reason: `working-directory trigger overflows its .cfgDir container by ${overflow.toFixed(1)}px (trigger.right=${measurement.trigger.right.toFixed(1)}, cfgDir.right=${measurement.cfgDir.right.toFixed(1)}) - formrow.module.css's .root is missing min-width:0`,
    };
  }
  return {
    pass: true,
    reason: `trigger fits inside .cfgDir with ${(-overflow).toFixed(1)}px to spare`,
  };
}
