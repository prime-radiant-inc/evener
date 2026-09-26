// The per-key half of listRevision's question. A store that reads one thing per
// key - a marketplace's catalog, an instance's models - has to ask it of each
// key separately: a reply is still this store's word on ITS key even though a
// newer reply for a different key has landed since.
//
// Two facts per key, and they are separate on purpose:
//   - its REVISION, which a retirement moves, so a reply issued before the
//     retirement lands nothing. A retirement is what invalidates the key: the
//     thing it described has changed, or the store has been told to forget it.
//   - whether a read for it is IN FLIGHT, and the promise of that read, so a
//     second caller waits for the first instead of sending its own. The
//     "loading" marker a view renders says nothing about when the answer comes.
//
// A retirement can land while a read for that key is on the wire, and a
// replacement read can be sent before the first one answers. So a registration
// is removed only by the read that made it - `settle` checks identity - and a
// waiter is handed whatever read is current for the key rather than the one it
// happened to find.

export interface KeyedRevision {
  /** The revision to issue a read for `key` under. */
  issue(key: string): number;
  /** Whether a reply that took `revision` for `key` is still current. */
  current(key: string, revision: number): boolean;
  /** Invalidates `key`: every reply for it still on the wire lands nothing. */
  retire(key: string): void;
  /** Invalidates every key that has a read in flight, and forgets those reads.
   * Returns the keys it retired, so a caller holding a cache entry per key -
   * one this map alone cannot see - can settle what it just fenced. */
  retireInFlight(): string[];
  /** Registers `read` as the read in flight for `key`. */
  begin(key: string, read: Promise<void>): void;
  /** The read in flight for `key`, if one is. */
  inFlight(key: string): Promise<void> | undefined;
  /**
   * Retires `read`'s registration if it is still the current one, and answers
   * what a waiter on `key` should await now: nothing when this read was the
   * current one, and the read that replaced it otherwise - that one's answer is
   * what the waiter actually wants, since this one has been fenced out.
   */
  settle(key: string, read: Promise<void>): Promise<void> | undefined;
}

export function createKeyedRevision(): KeyedRevision {
  const revisions = new Map<string, number>();
  const reads = new Map<string, Promise<void>>();

  function revisionOf(key: string): number {
    return revisions.get(key) ?? 0;
  }

  return {
    issue: revisionOf,
    current: (key, revision) => revisionOf(key) === revision,
    retire(key) {
      revisions.set(key, revisionOf(key) + 1);
    },
    retireInFlight() {
      const keys = [...reads.keys()];
      for (const key of keys) revisions.set(key, revisionOf(key) + 1);
      reads.clear();
      return keys;
    },
    begin(key, read) {
      reads.set(key, read);
    },
    inFlight: (key) => reads.get(key),
    settle(key, read) {
      const current = reads.get(key);
      if (current !== read) return current;
      reads.delete(key);
      return undefined;
    },
  };
}
