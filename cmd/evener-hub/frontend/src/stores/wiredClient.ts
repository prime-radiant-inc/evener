// The one client the thread store has wired its handlers onto, and the ready
// epoch that identifies the current connection generation. Everything that
// must not outlive a connection compares against these. This module never
// imports threads.ts.

import type { AppwireClientLike } from "@evener/appwire-client";

let wiredClient: AppwireClientLike | null = null;
let readyEpoch = 0;
const readyGenerationRetirementWaiters = new Map<number, Set<() => void>>();

export function getWiredClient(): AppwireClientLike | null {
  return wiredClient;
}

export function setWiredClient(client: AppwireClientLike | null): void {
  wiredClient = client;
}

export function getReadyEpoch(): number {
  return readyEpoch;
}

/** Retires the current ready generation's waiters, then starts the next one. */
export function advanceReadyEpoch(): number {
  const waiters = readyGenerationRetirementWaiters.get(readyEpoch);
  if (waiters) {
    readyGenerationRetirementWaiters.delete(readyEpoch);
    for (const waiter of waiters) waiter();
  }
  readyEpoch += 1;
  return readyEpoch;
}

export function watchReadyGenerationRetirement(epoch: number, waiter: () => void): () => void {
  let waiters = readyGenerationRetirementWaiters.get(epoch);
  if (!waiters) {
    waiters = new Set();
    readyGenerationRetirementWaiters.set(epoch, waiters);
  }
  waiters.add(waiter);
  return () => {
    waiters?.delete(waiter);
    if (waiters?.size === 0) readyGenerationRetirementWaiters.delete(epoch);
  };
}

export function resetWiredClientForTests(): void {
  wiredClient = null;
  readyEpoch = 0;
}
