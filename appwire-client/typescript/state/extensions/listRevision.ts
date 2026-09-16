// The "newest response wins" fence the extensions stores put on a list every
// response replaces whole (a hub's marketplaces, its installed plugins). The
// hub answers one connection's requests in the order they were sent, and a
// dropped connection rejects everything it had in flight, so a later response
// is always the newer list. The revision each request takes as it starts is
// the fence should that ever stop holding: a response writes its list only if
// no later revision has committed since. A failed list counts as a commit -
// its error is list state too, so a success that started earlier must not
// clear it.

export interface ListRevision {
  /** The revision for a request about to be sent. */
  next(): number;
  /** Whether the response that took `revision` is still the store's newest
   * word on the list, and records it as such when it is. */
  commit(revision: number): boolean;
  /** Fences every response still on the wire: none of them commits. */
  fence(): void;
}

export function createListRevision(): ListRevision {
  let issued = 0;
  let applied = 0;
  return {
    next() {
      issued += 1;
      return issued;
    },
    commit(revision) {
      if (revision < applied) return false;
      applied = revision;
      return true;
    },
    fence() {
      issued += 1;
      applied = issued;
    },
  };
}
