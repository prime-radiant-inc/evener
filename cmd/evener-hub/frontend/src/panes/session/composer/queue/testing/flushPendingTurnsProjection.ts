import { act } from "@testing-library/react";

import { settlePendingTurnsProjectionForTests } from "../pendingTurnsStore";

// The repeat loop every test wants: run rounds until one of them sees no work
// at all. The bound is a tripwire for a livelock - it throws rather than
// letting a test pass on a half-settled projection.
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
// them. A round also counts work registered while it runs, so work chained a
// few microtasks behind an operation that just finished - a receipt write once
// its RPC answers, a refresh once a commit notifies - keeps the loop going,
// and only a round that began and ended quiet ends it.
//
// It does not wait for anything that is not durable storage work: an RPC in
// flight (a test holding one open can still flush), a timer (the outbox's
// discovery interval), or a component's own state. pendingTurnsStore's "a
// flush cannot settle while ..." tests pin the property for submits and for
// storage writes begun anywhere, and Composer's pin it for both Stop routes.
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

// Gives a flush every chance to finish early: more macrotask hops than one of
// its rounds takes to return when it sees no work. So a flush still pending
// afterwards is waiting on work registered with the projection work tracker.
export async function outlastEmptyFlushRoundForTests(): Promise<void> {
  for (let hop = 0; hop < 5; hop += 1) {
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  }
}
