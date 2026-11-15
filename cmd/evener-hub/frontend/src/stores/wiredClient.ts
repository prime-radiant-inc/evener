// The one client the thread store has wired its handlers onto, and the ready
// epoch that identifies the current connection generation. Everything that
// must not outlive a connection compares against these. This module never
// imports threads.ts.

import type { AppwireClientLike } from "@evener/appwire-client";
import { KeyedWaiters } from "./keyedWaiters";

let wiredClient: AppwireClientLike | null = null;
let readyEpoch = 0;
const readyGenerationRetirementWaiters = new KeyedWaiters<number, () => void>();

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
  const waiters = readyGenerationRetirementWaiters.take(readyEpoch);
  if (waiters) {
    for (const waiter of waiters) waiter();
  }
  readyEpoch += 1;
  return readyEpoch;
}

export function watchReadyGenerationRetirement(epoch: number, waiter: () => void): () => void {
  readyGenerationRetirementWaiters.add(epoch, waiter);
  return () => readyGenerationRetirementWaiters.remove(epoch, waiter);
}

export function resetWiredClientForTests(): void {
  wiredClient = null;
  readyEpoch = 0;
}
