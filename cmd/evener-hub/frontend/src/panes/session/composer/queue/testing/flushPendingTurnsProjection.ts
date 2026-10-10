import { act } from "@testing-library/react";

import { settleProjectionWorkForTests } from "../../../../../stores/projectionWork";

// The repeat loop every test wants: run rounds until one of them sees no work
// at all. Each round settles the tracked work inside act. When the flush is the
// outermost act, each round's exit is where React runs the effects that work's
// updates scheduled; an effect can start new work, which the next round
// settles, so the bound counts those effect cycles. Call the flush after the
// outer act, not inside it: React holds a nested flush's effects until the
// outer act exits, so work they start registers after the flush returned
// (#2599), and testFlushScopeHygiene.test.ts fails a direct nested call. The
// bound is a tripwire for a livelock: it throws rather than letting a test pass
// on a half-settled projection.
//
// The act comes from @testing-library/react, not react: only RTL's wrapper
// marks the environment as act-capable for the wrapped scope, and callers of
// this flush run outside RTL's own scopes. Bare React act here made
// react-dom log "The current testing environment is not configured to
// support act(...)" on every render while the flag was clear.
//
// What it settles is what registers with the projection work tracker
// (stores/projectionWork.ts), and every durable path registers at its source:
// each transaction the mutation storage runs registers when it starts, and
// pendingTurnsStore's reads and submissions register from the call that starts
// them. One round waits out a whole chain of that work, including a step that
// starts only once the step before it settled - the dispatcher's next read, a
// receipt write once its RPC answers, a refresh once a commit notifies - and
// only a round that began and ended quiet ends the loop.
//
// The storage registers no RPC, so a test holding one open can still flush,
// except where pendingTurnsStore tracks a whole operation that contains one (a
// Retry's reconciliation read). Nor does the flush wait for a timer (the
// outbox's discovery interval) or a component's own state. pendingTurnsStore's "a flush
// cannot settle while ..." tests pin the property for submits and for storage
// writes begun anywhere, and Composer's pin it for both Stop routes.
export async function flushPendingTurnsProjectionForTests(): Promise<void> {
  for (let round = 0; round < 10; round += 1) {
    let awaited = 0;
    await act(async () => {
      awaited = await settleProjectionWorkForTests();
    });
    if (awaited === 0) return;
  }
  throw new Error("pending-turns projection never settled");
}

// Gives a flush every chance to finish early: more macrotask hops than one of
// its rounds takes to return when it sees no work. So a flush still pending
// afterwards is waiting on work registered with the projection work tracker.
async function outlastEmptyFlushRoundForTests(): Promise<void> {
  for (let hop = 0; hop < 5; hop += 1) {
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  }
}

// Starts a flush and outlasts an empty round of it, for a test that holds
// durable work open to check that the flush waits for it: `isDone()` still
// false on return means the flush is waiting on that work, and `done` settles
// once the test releases it.
export async function startFlushPastEmptyRoundForTests(): Promise<{ done: Promise<void>; isDone: () => boolean }> {
  let finished = false;
  const done = flushPendingTurnsProjectionForTests().then(() => {
    finished = true;
  });
  await outlastEmptyFlushRoundForTests();
  return { done, isDone: () => finished };
}
