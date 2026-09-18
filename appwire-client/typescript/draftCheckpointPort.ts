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
 * `classification` keeps the raw value most recently classified - readable
 * or not - named by WHEN it was classified, not by discardClassified's own
 * call: another store or a newer app version can replace the record between
 * the two, and a fresh storage.load() at discard time would then name (and
 * remove) whatever is there NOW - never the record the user was actually
 * shown. Nothing classified yet has no identity to act on, so
 * discardClassified/replaceClassified refuse rather than classifying blind:
 * every store restores through load() before a user can reach either.
 *
 * load() is not the only thing that classifies: save() writes a new record
 * too, and if it left `classification` pointing at the PRE-write bytes, an
 * edit immediately followed by a discard would refuse (it would still be
 * naming what the edit just replaced) and silently restore the edit instead
 * of discarding it - the identity must track every write, not only reads. */
export function createDraftRepository<Checkpoint extends object>(
  storage: DraftPort<Checkpoint>,
  decode: (value: unknown) => Checkpoint,
): DraftRepository<Checkpoint> {
  const rawFrom = new WeakMap<object, unknown>();
  // What load()/save() most recently classified: nothing yet, the store
  // classified as EMPTY (still a classification - distinct from never having
  // classified at all, so discardClassified/replaceClassified refuse rather
  // than acting on a record an external writer saved AFTER this repository
  // classified the store as empty, one it never classified), or the raw
  // bytes classified from a non-empty store.
  let classification: null | "absent" | { raw: unknown } = null;
  return {
    createId: () => storage.createId(),
    load(): Checkpoint | null {
      const value = storage.load();
      if (value === null || value === undefined) {
        classification = "absent";
        return null;
      }
      classification = { raw: value };
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
      classification = { raw: decoded };
      rawFrom.set(checkpoint as object, decoded);
    },
    removeIf(checkpoint: Checkpoint): boolean {
      return storage.removeIf(rawFrom.get(checkpoint as object) ?? decode(checkpoint));
    },
    /** Removes the record load()/save() most recently classified, readable
     * or not, by the identity of the bytes it was classified from - never a
     * fresh reload, which could name a record another writer has since
     * replaced. A load() that classified the store as EMPTY removes nothing
     * (there is no record this repository classified to discard) and reports
     * false. load() is this repository's only entry to an identity: nothing
     * classified yet refuses rather than classifying blind here - a store
     * always restores through load() before a user can reach discard. */
    discardClassified(): boolean {
      if (classification === null || classification === "absent") return false;
      return storage.removeIf(classification.raw);
    },
    /** Settles the classified record onto `next` atomically. Nothing
     * classified yet (or classified as absent) has no identity to be atomic
     * against, so this refuses rather than writing over storage blind - the
     * same posture discardClassified takes. */
    replaceClassified(next: Checkpoint): boolean {
      if (classification === null || classification === "absent") return false;
      const decoded = decode(next);
      const replaced = storage.replaceIf(classification.raw, decoded);
      if (replaced) {
        classification = { raw: decoded };
        rawFrom.set(next as object, decoded);
      }
      return replaced;
    },
  };
}
