import { MutationOutboxIndexedDB } from "../mutationOutboxIndexedDB";

export interface PumpIndexedDBOptions {
  // Real `listTargetRefs()` reads to spend before giving up. The count is a
  // tripwire that keeps a genuine hang loud, never the condition a test awaits.
  turns: number;
  // Checked before each read; the pump stops as soon as it holds. A caller that
  // only wants the reads to run passes an always-false predicate.
  until: () => boolean | Promise<boolean>;
  // Runs before each probe read: the hook for a caller whose pending work
  // advances only when something else - a fake clock tick - interleaves with
  // the storage round trip.
  onTurn?: (turn: number) => void | Promise<void>;
}

// pumpIndexedDBUntil spends real IndexedDB round trips so pending storage and
// the continuations parked on it can finish. fake-indexeddb delivers each
// event on a macrotask, so one `listTargetRefs()` read per turn is what lets a
// chain of queued work converge without a wall-clock wait.
//
// The probe database is arbitrary: the read only has to be one real round
// trip, so it uses the outbox adapter these tests already import.
//
// Returns whether `until` held after the last read. A caller that must fail
// loudly checks the result; a caller whose own assertion is the oracle
// ignores it.
export async function pumpIndexedDBUntil({ turns, until, onTurn }: PumpIndexedDBOptions): Promise<boolean> {
  const probe = new MutationOutboxIndexedDB();
  try {
    for (let turn = 0; turn < turns; turn += 1) {
      if (await until()) return true;
      await onTurn?.(turn);
      await probe.listTargetRefs();
    }
    return await until();
  } finally {
    probe.close();
  }
}
