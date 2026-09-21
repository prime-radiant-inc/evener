// The fence every extensions store puts on a list that each response replaces
// whole (a hub's marketplaces, its installed plugins, its global launch
// layer). It is written out as a state machine and implemented from the table,
// because a rule about one ordering is not a rule about all of them.
//
// ONE INVARIANT
//   The owner is the highest revision that is still live. Only the owner's
//   answer may be published. When the owner stops being live, ownership passes
//   to the highest lower revision that is still live, and if that revision's
//   answer has already arrived it is published then.
//
// A revision is LIVE from the moment it is issued until it publishes, is
// retracted, or is fenced. "Held" is a live revision whose answer has already
// arrived while something newer owned the list.
//
// STATES, EVENTS AND TRANSITIONS  (r is the revision the row is about)
//
//   state      | issue          | answer arrives        | reject            | fence
//   -----------|----------------|-----------------------|-------------------|--------
//   (none)     | -> live        | -                     | -                 | -
//   live,      | (a higher      | r is the owner:       | -> retracted;     | -> fenced
//   no answer  |  revision      |   publish, and every  | ownership passes  |  (its answer,
//              |  becomes the   |   revision <= r dies  | down; the new     |  if it ever
//              |  owner)        | r is not the owner:   | owner's held      |  arrives,
//              |                |   -> held             | answer publishes  |  publishes
//              |                |                       |                   |  nothing)
//   held       | (unchanged)    | (cannot happen: one   | -> retracted      | -> fenced
//              |                |  answer per request)  |                   |
//   published  | (unchanged)    | -                     | -                 | -
//   retracted  | (unchanged)    | dropped (defensive:   | -                 | -
//              |                |  a request that        |                   |
//              |                |  retracted has already |                   |
//              |                |  failed, so no caller  |                   |
//              |                |  reaches this)         |                   |
//   fenced     | (unchanged)    | dropped               | -                 | -
//
// Published, retracted and fenced are terminal: a revision in any of them is
// not live, and nothing it does afterwards reaches the store.
//
// WHY EACH ROW IS THERE
//   - "publish, and every revision <= r dies": nothing older than the newest
//     published answer can ever apply, so a held answer below it is dead.
//   - "r is not the owner: held", rather than dropped: the requests that
//     outran it may all fail, and then its answer is the newest anybody has
//     and the last one there will be.
//   - "reject -> ownership passes down": a request that publishes nothing must
//     not keep what it fenced. A failed write that held the loading flag a
//     read raised would otherwise leave it up with nothing left to lower it.
//   - fence is what a reset, a dispose and a REPLACED connection use: a
//     different hub's answers describe a machine the store no longer speaks
//     to, and a store that has forgotten what it read wants none of it either.
//
// The hub answers one connection's requests in the order they were sent, and a
// dropped connection rejects everything it had in flight, so the owner's answer
// is also the newest list. The revision is the fence should that ever stop
// holding.

export interface ListRevision {
  /** Issues a revision: live from here, and the owner until something newer is
   * issued. */
  next(): number;
  /** This request's answer. Published if the revision owns the list, held if
   * the revision is live but outrun, dropped if it is no longer live. `write`
   * runs at most once. */
  publish(revision: number, write: () => void): void;
  /** This request published nothing at all - a rejection the store records
   * nowhere - so the revision stops being live and ownership passes down. */
  retract(revision: number): void;
  /** Every live revision stops being live: nothing on the wire, and nothing
   * held, publishes. */
  fence(): void;
  /** Whether any revision is currently live - on the wire, or answered but
   * held behind a newer owner. A read and a write both issue through next(),
   * so this is true for either: the seam a store's own "does it want this
   * list" question (storeLifecycle.ts's wantsList) reads a mutation's intent
   * off, the same way it already reads a read's own loading flag. */
  hasLive(): boolean;
}

export function createListRevision(): ListRevision {
  let issued = 0;
  // The live revisions, each with the answer it is holding or null while its
  // request is still on the wire. A Map keeps insertion order, and revisions
  // are only ever added in ascending order, so the last key is the owner.
  const live = new Map<number, (() => void) | null>();

  /** The highest live revision, or null when nothing is live. */
  function owner(): number | null {
    let highest: number | null = null;
    for (const revision of live.keys()) highest = revision;
    return highest;
  }

  /** Publishes the owner's answer and retires every revision at or below it. */
  function publishOwner(revision: number, write: () => void): void {
    for (const candidate of [...live.keys()]) {
      if (candidate <= revision) live.delete(candidate);
    }
    write();
  }

  return {
    next() {
      issued += 1;
      live.set(issued, null);
      return issued;
    },
    publish(revision, write) {
      if (!live.has(revision)) return;
      if (revision === owner()) publishOwner(revision, write);
      else live.set(revision, write);
    },
    retract(revision) {
      if (!live.delete(revision)) return;
      const next = owner();
      if (next === null) return;
      const held = live.get(next);
      if (held) publishOwner(next, held);
    },
    fence() {
      live.clear();
    },
    hasLive: () => live.size > 0,
  };
}

/**
 * Runs a READ under a fresh revision. Either outcome is list state - the rows,
 * or the error a view renders instead of them - so both go through the fence
 * and the read never throws: a read that was superseded publishes neither.
 */
export async function readRevisioned<T>(
  revisions: ListRevision,
  request: () => Promise<T>,
  handlers: { onAnswer(answer: T): () => void; onFailure(error: unknown): () => void },
): Promise<void> {
  const revision = revisions.next();
  try {
    const answer = await request();
    revisions.publish(revision, handlers.onAnswer(answer));
  } catch (error) {
    revisions.publish(revision, handlers.onFailure(error));
  }
}

/**
 * Runs a WRITE under a fresh revision. Its answer is list state and goes
 * through the fence; its failure is the caller's to surface, so the rejection
 * propagates and the revision is given back - a request that published nothing
 * must not keep what it fenced. `onAnswer` returns null to say the same thing
 * about an answer it will not publish, for a store with a fence of its own
 * beside this one. `onFailure` may return a state writer for an authoritative
 * failure payload; it uses this request's revision before the original error
 * is rethrown.
 */
export async function writeRevisioned<T>(
  revisions: ListRevision,
  request: () => Promise<T>,
  onAnswer: (answer: T) => (() => void) | null,
  onFailure?: (error: unknown) => (() => void) | null,
): Promise<void> {
  const revision = revisions.next();
  let answer: T;
  try {
    answer = await request();
  } catch (error) {
    const write = onFailure?.(error) ?? null;
    if (write) revisions.publish(revision, write);
    else revisions.retract(revision);
    throw error;
  }
  const write = onAnswer(answer);
  if (write) revisions.publish(revision, write);
  else revisions.retract(revision);
}
