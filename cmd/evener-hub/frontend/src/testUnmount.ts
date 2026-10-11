// Testing Library's cleanup() unmounts each tree inside act() and stops at the
// first whose unmount throws (an effect cleanup that throws), before it removes
// that tree's container or unmounts the trees after it. React DOM's
// root.unmount() does nothing for a root it has already unmounted, so running
// cleanup() again gets past the tree that threw. Each error is reported.
//
// No test renders anywhere near this many trees, so a cleanup() that still
// throws after this many runs is not getting past the tree that threw. It
// stops there and says so, rather than running forever.
export const unmountAttemptsBeforeGivingUp = 20;

export function unmountEveryTree(cleanup: () => void, report: (error: unknown) => void): void {
  for (let attempt = 0; attempt < unmountAttemptsBeforeGivingUp; attempt += 1) {
    try {
      cleanup();
      return;
    } catch (error) {
      report(error);
    }
  }
  report(
    new Error(
      `Testing Library's cleanup() threw on ${unmountAttemptsBeforeGivingUp} runs in a row, so it is not getting past a tree whose unmount throws. That relies on React DOM's root.unmount() doing nothing for a root it has already unmounted; update unmountEveryTree in testUnmount.ts.`,
    ),
  );
}
