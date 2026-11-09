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
// Tracking costs two counter updates and one `finally` reaction per storage
// transaction. Nothing outside a test reads the count or waits on it, so
// production behavior is unchanged apart from each storage call settling a
// few microtasks later. The package tracker owns only track/whenIdle/clear;
// the stall tripwire and macrotask hop are this host's test policy
// (flushPendingTurnsProjection.ts, #1891).
const projectionWorkTracker = createMutationProjectionWorkTracker();

export function trackProjectionWork<T>(work: Promise<T>): Promise<T> {
  return projectionWorkTracker.track(work);
}

// Waits for the tracked work once. The flush helper owns the tripwire and the
// repeated hop; this is only the package's wait.
export function whenProjectionWorkIdleForTests(): Promise<number> {
  return projectionWorkTracker.whenIdle();
}

// Cumulative registrations, so the flush can see work that started and
// finished entirely inside one macrotask hop.
export function projectionWorkRegistrationsForTests(): number {
  return projectionWorkTracker.registrations();
}

// Forgets whatever is still in flight: a test resetting its stores already
// knows that work's result no longer matters.
export function clearProjectionWorkForTests(): void {
  projectionWorkTracker.clear();
}
