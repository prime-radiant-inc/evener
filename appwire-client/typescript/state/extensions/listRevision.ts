// The "latest request wins" fence the extensions stores put on a list every
// response replaces whole (a hub's marketplaces, its installed plugins, its
// global launch layer).
//
// The owner is the latest request ISSUED, not the latest response applied. A
// request that has been superseded writes nothing when it lands, even if
// nothing newer has answered yet: its list is older than the one already on
// the way, and the loading flag and the error belong to it just as much, so a
// superseded answer clearing the flag a pending request raised - or posting
// its own error over a load still running - is the same mistake as rolling the
// list back. A failed list is an answer like any other and takes the same
// fence.
//
// The hub answers one connection's requests in the order they were sent, and a
// dropped connection rejects everything it had in flight, so the latest
// request's answer is also the newest list. The revision is the fence should
// that ever stop holding.

export interface ListRevision {
  /** The revision for a request about to be sent, and from here on the only
   * one that may commit. */
  next(): number;
  /** Whether the response that took `revision` is still the store's newest
   * word on the list. */
  commit(revision: number): boolean;
  /** Fences every response still on the wire: none of them commits. */
  fence(): void;
}

export function createListRevision(): ListRevision {
  let issued = 0;
  return {
    next() {
      issued += 1;
      return issued;
    },
    commit(revision) {
      return revision === issued;
    },
    fence() {
      issued += 1;
    },
  };
}
