import { rowContractFailures } from "./row-contracts.mjs";

// A one-element tool group keeps the shared tool-row contracts on a phone: the
// Open control's 44px touch target overhangs its line yet stays reachable at
// both edges, the last rendered row's evidence band spans its row, and a
// suppressed member's empty wrapper adds no space below that band.
export default function assert(m) {
  const failures = rowContractFailures(m);
  if (Math.abs(m.groupBottom - m.bandBottom) > 0.5)
    failures.push(
      `the suppressed member's empty wrapper adds ${m.groupBottom - m.bandBottom}px below the evidence band`,
    );
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `the ${m.openHeight}px Open control is reachable at both edges, the evidence band spans its row and ends the group, and the empty suppressed wrapper adds no space`,
  };
}
