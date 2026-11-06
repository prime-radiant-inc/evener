import { MutationOutboxIndexedDB } from "../mutationOutboxIndexedDB";

export interface PumpIndexedDBOptions {
  // The pump's storage factory. Omit it to use the ambient factory the caller's
  // `fake-indexeddb/auto` import installed; pass the test's own factory when its
  // storage lives in a private database.
  factory?: IDBFactory;
  // Real `listTargetRefs()` reads to spend before giving up. The count is a
  // tripwire that keeps a genuine hang loud, never the condition a test awaits.
  turns?: number;
  // Checked before each read. The pump stops as soon as this holds, so a caller
  // that only needs the reads to run omits it and spends every turn.
  until?: () => boolean | Promise<boolean>;
  // Runs before each probe read: the hook for a caller whose pending work
  // advances only when something else - a fake clock tick, an act() render
  // commit - interleaves with the storage round trip.
  onTurn?: (turn: number) => void | Promise<void>;
}

// pumpIndexedDBUntil spends real IndexedDB round trips so pending storage and
// the continuations parked on it can finish. fake-indexeddb delivers each
// event on a macrotask, so one `listTargetRefs()` read per turn is what lets a
// chain of queued work converge without a wall-clock wait.
//
// Returns whether `until` held (true when there was no `until` to hold). A
// caller that must fail loudly checks the result; a caller pumping a fixed
// number of turns ignores it.
export async function pumpIndexedDBUntil({
  factory,
  turns = 30,
  until,
  onTurn,
}: PumpIndexedDBOptions = {}): Promise<boolean> {
  const probe = new MutationOutboxIndexedDB(factory ? { indexedDB: factory } : {});
  try {
    for (let turn = 0; turn < turns; turn += 1) {
      if (until && (await until())) return true;
      await onTurn?.(turn);
      await probe.listTargetRefs();
    }
    return until ? await until() : true;
  } finally {
    probe.close();
  }
}
