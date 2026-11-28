import { rowContractFailures } from "../toolcard-corner-clip-phone/row-contracts.mjs";

// A folded run cancels Disclosure's body gap so its first row sits on the summary.
export default function assert(m) {
  const failures = rowContractFailures(m);
  if (Math.abs(m.firstRowGap) > 0.5)
    failures.push(
      `the open run's first row starts ${m.firstRowGap}px below the summary - expected Disclosure's --space-1 top gap to be cancelled`,
    );
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `the ${m.openHeight}px Open control is reachable at both edges, the evidence band spans its row, and the first row sits on the summary`,
  };
}
