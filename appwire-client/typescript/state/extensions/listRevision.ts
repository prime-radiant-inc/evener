// The "latest request wins" fence the extensions stores put on a list every
// response replaces whole (a hub's marketplaces, its installed plugins, its
// global launch layer).
//
// The owner is the latest request ISSUED and still live, not the latest
// response applied. A
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
   * one that may publish. */
  next(): number;
  /** Publishes this request's answer if it is the live one, and otherwise
   * holds it as this revision's candidate: should the requests that
   * superseded it all retract, the newest held answer becomes the live one and
   * is published then. `write` is called at most once. */
  publish(revision: number, write: () => void): void;
  /** Gives `revision` back when its request published nothing at all - a
   * rejection the store records nowhere. Ownership returns to the request
   * before it: if that one's answer has already landed it is published now,
   * and if it is still on the wire it will publish when it lands. Without this
   * a failed write keeps the loading flag a read raised, with nothing left to
   * lower it. A no-op for a revision something newer has already superseded. */
  retract(revision: number): void;
  /** Fences every response still on the wire and every answer held: none of
   * them publishes. */
  fence(): void;
}

export function createListRevision(): ListRevision {
  let issued = 0;
  // Answers that landed while superseded, by the revision that asked. Only a
  // retraction can make one of them live again, and a live answer clears the
  // older ones: nothing before the newest published answer can ever apply.
  const held = new Map<number, () => void>();

  function publishLive(revision: number, write: () => void): void {
    held.clear();
    write();
    void revision;
  }

  return {
    next() {
      issued += 1;
      return issued;
    },
    publish(revision, write) {
      if (revision === issued) publishLive(revision, write);
      else if (revision < issued) held.set(revision, write);
    },
    retract(revision) {
      if (revision !== issued) return;
      issued -= 1;
      const landed = held.get(issued);
      if (!landed) return;
      held.delete(issued);
      publishLive(issued, landed);
    },
    fence() {
      issued += 1;
      held.clear();
    },
  };
}
