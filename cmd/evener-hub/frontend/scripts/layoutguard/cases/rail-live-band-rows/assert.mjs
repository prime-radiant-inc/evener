import sharedAssert from "./shared-assert.mjs";

export default function assert(m) {
  const base = sharedAssert(m);
  if (!base.pass) return base;
  const labelCenter = (m.whyLabel.top + m.whyLabel.bottom) / 2;
  const timeCenter = (m.whyTime.top + m.whyTime.bottom) / 2;
  const drift = Math.abs(timeCenter - labelCenter);
  if (!(drift <= 2)) {
    return {
      pass: false,
      reason: `desktop two-line timestamp center (${timeCenter.toFixed(1)}) drifted ${drift.toFixed(1)}px from title line center (${labelCenter.toFixed(1)})`,
    };
  }
  return {
    pass: true,
    reason: `${base.reason}; timestamp aligns with title row (${drift.toFixed(1)}px drift)`,
  };
}
