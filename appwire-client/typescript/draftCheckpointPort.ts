// The checkpointed draft editor's port and repository, generic over each
// store's own checkpoint shape: transcriptDisplayStore.ts and
// keybindingsStore.ts each keep their own draftCheckpoint decoder (and its
// own invalid-draft message), but everything downstream of "decode this
// value or throw" was identical between the two, byte for byte. This module
// is that shared downstream half.

/** The shape a checkpointed draft editor's storage port has, over any
 * checkpoint type - the same shape testing/draftStorage.ts's in-memory test
 * double implements. */
export interface DraftPort<Checkpoint> {
  createId(): string;
  load(): unknown;
  save(checkpoint: Checkpoint): void;
  removeIf(checkpoint: Checkpoint): void;
}

/** What the port held is not a checkpoint this build can read. Distinct from
 * a port that could not be reached: the record is the problem, so the section
 * can still load the hub and can still throw the record away. */
export class UnreadableDraftError extends Error {}

/** Removes whatever a draft port holds, readable or not, WITHOUT a store: the
 * raw value goes straight back to the port, which matches its own bytes, so a
 * record no build can decode is still the record removed. A host whose store is
 * gone (no connection, so no client to build one from) needs this to clear an
 * unreadable record; a host with a live store uses discardDraft, which also
 * publishes the state. One implementation either way. */
export function discardStoredDraft<Checkpoint>(storage: DraftPort<Checkpoint>): void {
  const value = storage.load();
  if (value !== null && value !== undefined) storage.removeIf(value as Checkpoint);
}

export interface DraftRepository<Checkpoint> {
  createId(): string;
  load(): Checkpoint | null;
  save(checkpoint: Checkpoint): void;
  removeIf(checkpoint: Checkpoint): void;
  discardUnreadable(): void;
}

/** The draft port with every checkpoint normalized through `decode` in BOTH
 * directions, so a port that compares serialized bytes sees one key order on
 * both sides. load() is the trust boundary: a malformed stored draft
 * surfaces as a storage failure, never as state.
 *
 * `decode` only keeps the fields its store knows, so a record another build
 * wrote with extra fields decodes to a normalized checkpoint that is not
 * what the stored bytes actually hold - a byte-aware port's own compare
 * (matching what it read against what it is asked to remove) would then
 * refuse to remove a record it just handed back. rawFrom keeps load()'s raw
 * value beside the checkpoint built from it, so removeIf can still hand the
 * port back exactly what it read: the only thing a byte-aware port can name a
 * record by. A checkpoint removeIf is given that load() never produced (a
 * fresh save's own checkpoint, or one rebuilt from published state) has no
 * raw value to recover and falls back to `decode`'s normalized one, as
 * before - such a checkpoint carries no unknown fields to begin with.
 *
 * lastUnreadable keeps the raw value load() most recently classified
 * unreadable, named by WHEN it was classified, not by discardUnreadable's own
 * call: another store or a newer app version can replace the record between
 * the two, and a fresh storage.load() at discard time would then name (and
 * remove) whatever is there NOW - never the record the user was actually
 * shown. */
export function createDraftRepository<Checkpoint>(
  storage: DraftPort<Checkpoint>,
  decode: (value: unknown) => Checkpoint,
): DraftRepository<Checkpoint> {
  const rawFrom = new WeakMap<object, unknown>();
  let lastUnreadable: unknown;
  let hasLastUnreadable = false;
  return {
    createId: () => storage.createId(),
    load(): Checkpoint | null {
      const value = storage.load();
      if (value === null || value === undefined) {
        hasLastUnreadable = false;
        return null;
      }
      try {
        const checkpoint = decode(value);
        hasLastUnreadable = false;
        rawFrom.set(checkpoint as object, value);
        return checkpoint;
      } catch (error) {
        lastUnreadable = value;
        hasLastUnreadable = true;
        throw error;
      }
    },
    save(checkpoint: Checkpoint): void {
      storage.save(decode(checkpoint));
    },
    removeIf(checkpoint: Checkpoint): void {
      storage.removeIf((rawFrom.get(checkpoint as object) ?? decode(checkpoint)) as Checkpoint);
    },
    /** Removes the record load() classified unreadable, by the identity of
     * the bytes it was classified from - never a fresh reload, which could
     * name a record another writer has since replaced. A byte-aware port's
     * own compare (removeIf) then refuses on its own if that record is gone;
     * this falls back to discardStoredDraft's fresh-reload behavior only when
     * nothing has been classified yet (defensive: a store never calls this
     * without classifying first). */
    discardUnreadable(): void {
      if (!hasLastUnreadable) {
        discardStoredDraft(storage);
        return;
      }
      storage.removeIf(lastUnreadable as Checkpoint);
    },
  };
}
