import { act } from "@testing-library/react";

import { settlePendingTurnsProjectionForTests } from "../pendingTurnsStore";

// The repeat loop every test wants: run rounds until one of them finds
// nothing outstanding. The bound is a tripwire for a livelock - it throws
// rather than letting a test pass on a half-settled projection.
//
// The act comes from @testing-library/react, not react: only RTL's wrapper
// marks the environment as act-capable for the wrapped scope, and callers of
// this flush run outside RTL's own scopes. Bare React act here logged "The
// current testing environment is not configured to support act(...)" once per
// round.
//
// What it can settle is exactly what registers with trackProjectionWork, so a
// round that found nothing is proof that nothing is left only while every
// durable path registers before it can be observed. Every path in
// pendingTurnsStore does: the last one that did not was
// submitWithPendingTracking, which registered nothing until its own work had
// already finished, and a flush starting in that window declared the
// projection settled with a send still in flight (kata 3p22).
//
// That is a claim about pendingTurnsStore, not about every caller. The round
// below snapshots synchronously, in the same tick as the call, so a caller
// that starts durable work which only registers a microtask later is
// invisible to it - but only if that same caller flushes without yielding
// first. Composer's queueRecoveryPersistence chains that way and every one of
// its call sites yields, which is why it was measured unreachable rather than
// fixed: zero occurrences in 1422 test executions under load (kata 5meh,
// closed as an audit).
//
// So the hazard is the pattern, not that instance. A new path that registers
// late reopens it as a load-sensitive false green rather than a failure.
// pendingTurnsStore's "a flush cannot settle while a submit is still in
// flight" pins the property for the paths there.
export async function flushPendingTurnsProjectionForTests(): Promise<void> {
  for (let round = 0; round < 10; round += 1) {
    let awaited = 0;
    await act(async () => {
      awaited = await settlePendingTurnsProjectionForTests();
    });
    if (awaited === 0) return;
  }
  throw new Error("pending-turns projection never settled");
}
