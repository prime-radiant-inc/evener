// The checkpointed draft editor's port and repository, generic over each
// store's own checkpoint shape: keybindingsStore.ts keeps its own
// draftCheckpoint decoder (and its own invalid-draft message); a later piece
// of the SDK migration brings a second store onto this same repository, and
// everything downstream of "decode this value or throw" is what the two will
// share, byte for byte. This module is that shared downstream half.

/** The shape a checkpointed draft editor's storage port has, over any
 * checkpoint type - the same shape testing/draftStorage.ts's in-memory test
 * double implements. */
export interface DraftPort<Checkpoint> {
  createId(): string;
  load(): unknown;
  save(checkpoint: Checkpoint): void;
  /** Removes the stored record only if `identity` still names it; reports
   * whether it did. `identity` is usually a Checkpoint this port itself
   * produced (via load() or save()), but discardStoredDraft() also hands it
   * the RAW value load() returned for a record no build can decode - typed
   * `unknown`, not Checkpoint, so a conforming port never assumes the
   * declared checkpoint shape and compares by whatever bytes it actually
   * holds. */
  removeIf(identity: unknown): boolean;
  /** Replaces the stored record with `next` only if `expected` still names
   * it (the same raw-or-decoded identity removeIf takes); reports whether it
   * did. The atomic twin of removeIf: a load-then-save pair has the
   * identical race a load-then-remove pair would (the reason removeIf exists
   * at all) - a concurrent writer's checkpoint landing between the two would
   * be silently overwritten by an unconditional save. */
  replaceIf(expected: unknown, next: Checkpoint): boolean;
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
 * publishes the state. One implementation either way. Reports whether
 * anything was actually removed. */
export function discardStoredDraft<Checkpoint>(storage: DraftPort<Checkpoint>): boolean {
  const value = storage.load();
  if (value === null || value === undefined) return false;
  return storage.removeIf(value);
}

export interface DraftRepository<Checkpoint> {
  createId(): string;
  load(): Checkpoint | null;
  save(checkpoint: Checkpoint): void;
  removeIf(checkpoint: Checkpoint): boolean;
  discardClassified(): boolean;
  /** Settles the classified record onto `next` atomically against the
   * identity load()/save() most recently classified. Reports whether it
   * did; a refusal means another writer replaced the record while this
   * store's write was in flight, and the caller must adopt that
   * replacement (load() again) rather than overwrite it. */
  replaceClassified(next: Checkpoint): boolean;
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
 * before - such a checkpoint carries no unknown fields to begin with. A
 * checkpoint that DOES have a rawFrom entry (load() produced it) but is then
 * handed to save()/replaceClassified() has that entry updated to the bytes
 * just written - otherwise a later removeIf on that SAME reference would
 * still hand the port the pre-write bytes, which a byte-aware port compares
 * against what save() already overwrote and refuses to touch.
 *
 * lastClassified keeps the raw value most recently classified - readable or
 * not - named by WHEN it was classified, not by discardClassified's own
 * call: another store or a newer app version can replace the record between
 * the two, and a fresh storage.load() at discard time would then name (and
 * remove) whatever is there NOW - never the record the user was actually
 * shown. One field for both cases, because a discard is the same operation
 * either way: remove the classified record, by its own identity, and report
 * whether that succeeded.
 *
 * load() is not the only thing that classifies: save() writes a new record
 * too, and if it left lastClassified pointing at the PRE-write bytes, an
 * edit immediately followed by a discard would refuse (it would still be
 * naming what the edit just replaced) and silently restore the edit instead
 * of discarding it - the identity must track every write, not only reads. */
export function createDraftRepository<Checkpoint extends object>(
  storage: DraftPort<Checkpoint>,
  decode: (value: unknown) => Checkpoint,
): DraftRepository<Checkpoint> {
  const rawFrom = new WeakMap<object, unknown>();
  let lastClassified: unknown;
  let hasClassified = false;
  // Distinct from hasClassified: load() classifying an EMPTY store is still a
  // classification (of absence), not "nothing classified yet". Without this,
  // discardClassified() read an empty load() the same as never having
  // classified at all, took the fresh-reload fallback, and removed a record
  // an external writer saved AFTER this repository classified the store as
  // empty - a record it never classified.
  let classifiedAbsent = false;
  return {
    createId: () => storage.createId(),
    load(): Checkpoint | null {
      const value = storage.load();
      if (value === null || value === undefined) {
        hasClassified = true;
        classifiedAbsent = true;
        return null;
      }
      lastClassified = value;
      hasClassified = true;
      classifiedAbsent = false;
      const checkpoint = decode(value);
      rawFrom.set(checkpoint as object, value);
      return checkpoint;
    },
    save(checkpoint: Checkpoint): void {
      const decoded = decode(checkpoint);
      storage.save(decoded);
      // What was just written IS now the classified record: no raw bytes to
      // recover (this build built it), so the decoded value is its own
      // identity, the same fallback removeIf already uses for a checkpoint
      // load() never produced. If `checkpoint` itself came from an earlier
      // load(), its own rawFrom entry now points at the bytes THIS call
      // wrote - otherwise a removeIf on that same reference would still
      // name the pre-save bytes, which save() already overwrote.
      lastClassified = decoded;
      hasClassified = true;
      classifiedAbsent = false;
      rawFrom.set(checkpoint as object, decoded);
    },
    removeIf(checkpoint: Checkpoint): boolean {
      return storage.removeIf(rawFrom.get(checkpoint as object) ?? decode(checkpoint));
    },
    /** Removes the record load() most recently classified, readable or not,
     * by the identity of the bytes it was classified from - never a fresh
     * reload, which could name a record another writer has since replaced.
     * A load() that classified the store as EMPTY removes nothing (there is
     * no record this repository classified to discard) and reports false.
     * Nothing classified AT ALL (defensive: a store never calls this
     * without classifying first) has no identity to act on either, so this
     * classifies NOW via a fresh load() rather than removing blind: a
     * readable record is not this call's to discard (some writer stored it
     * without this repository ever showing it to a user) and is left alone;
     * only an unreadable one - nothing any build could have shown - is
     * removed, matching discardStoredDraft's own contract. */
    discardClassified(): boolean {
      if (classifiedAbsent) return false;
      if (hasClassified) return storage.removeIf(lastClassified);
      const value = storage.load();
      if (value === null || value === undefined) return false;
      try {
        decode(value);
        return false;
      } catch {
        return storage.removeIf(value);
      }
    },
    /** Settles the classified record onto `next` atomically. Nothing
     * classified yet (or classified as absent) has no identity to be atomic
     * against - the same case discardClassified treats as nothing-to-act-on
     * - but rather than overwriting blind, this classifies NOW via a fresh
     * load(): a readable record found there is not this call's to replace
     * (refuses, so the caller re-reads/surfaces it, the same posture a
     * refused replaceIf gets below); only empty or unreadable storage is
     * fair game to write over unconditionally. */
    replaceClassified(next: Checkpoint): boolean {
      const decoded = decode(next);
      if (!hasClassified || classifiedAbsent) {
        const value = storage.load();
        if (value !== null && value !== undefined) {
          try {
            decode(value);
            return false;
          } catch {
            // Unreadable: nothing readable to lose, fall through to write.
          }
        }
        storage.save(decoded);
        lastClassified = decoded;
        hasClassified = true;
        classifiedAbsent = false;
        rawFrom.set(next as object, decoded);
        return true;
      }
      const replaced = storage.replaceIf(lastClassified, decoded);
      if (replaced) {
        lastClassified = decoded;
        hasClassified = true;
        classifiedAbsent = false;
        rawFrom.set(next as object, decoded);
      }
      return replaced;
    },
  };
}
