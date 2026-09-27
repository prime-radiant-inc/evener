import { createMutationProjectionWorkTracker } from "@evener/appwire-client/state/mutation";

// The durable work the pending-turns projection depends on, counted while it
// runs. Every transaction the mutation storage runs registers here when it
// starts (mutationOutboxIndexedDB.ts), and the projection's own reads and
// submissions register from pendingTurnsStore, so no caller has to remember to.
// Its wall time scales with machine load - a mount-to-activation latency of
// 124-1246ms was measured for the Composer's own path (kata 3c7t) - which
// leaves a test with nothing to await but the work itself: polling its side
// effects against a fixed window is a race, not an assertion.
//
// Production only records: tracking costs a counter increment and one
// `finally` reaction per operation, and nothing outside a test ever waits on
// the count, so it changes no production behavior. The stall tripwire and the
// macrotask yield the tracker settles through are the package's; this binds
// them to the browser's own timers and a MessageChannel hop.
const projectionWorkTracker = createMutationProjectionWorkTracker({
  setTimeout: (callback, milliseconds) => setTimeout(callback, milliseconds),
  clearTimeout: (timerId) => clearTimeout(timerId),
  yieldMacrotask: () =>
    new Promise<void>((resolve) => {
      const hop = new MessageChannel();
      hop.port1.onmessage = () => {
        hop.port1.close();
        resolve();
      };
      hop.port2.postMessage(undefined);
    }),
});

export function trackProjectionWork<T>(work: Promise<T>): Promise<T> {
  return projectionWorkTracker.track(work);
}

// Waits until no tracked work is outstanding and reports how much the round
// saw. Callers repeat until it reports zero, flushing React in between: the
// components start this work from effects, so only a flush can reveal whether
// anything is left.
export function settleProjectionWorkForTests(): Promise<number> {
  return projectionWorkTracker.settle();
}

// Forgets whatever is still in flight: a test resetting its stores already
// knows that work's result no longer matters.
export function clearProjectionWorkForTests(): void {
  projectionWorkTracker.clear();
}
