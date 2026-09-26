// memoryDraftStorage is an in-memory draft port for any settings store's
// checkpointed editor: one stored checkpoint, cloned on the way in and out the
// way a JSON-backed port would, compared by JSON bytes on removeIf/replaceIf
// the way the native port does, with knobs for the failures it has to survive
// (a save that throws, a stored value that is not a checkpoint). Ids are
// non-integer strings like production's UUIDs: an integer-like id is an
// index-like key V8 orders first, which would hide a key-order mismatch
// between what was saved and what removeIf is later given. In-repo test
// support, not shipped.

import { canonicalJson, type DraftPort } from "../draftCheckpointPort";

export interface MemoryDraftStorage<Checkpoint> {
  storage: DraftPort<Checkpoint>;
  /** What the port holds right now, exactly as load() would return it -
   * unknown, not Checkpoint: a corrupt() record is not one, and this
   * accessor must be able to show that raw state too. */
  stored(): unknown;
  /** Make save() and insertIfAbsent() throw (or stop making them throw). */
  failSave(fail?: boolean): void;
  /** Make replaceIf() throw (or stop making it throw). */
  failReplace(fail?: boolean): void;
  /** Make load() throw (or stop making it throw) - a genuine port failure,
   * distinct from corrupt()'s present-but-undecodable record. */
  failLoad(fail?: boolean): void;
  /** Replace the stored value with something that is not a checkpoint. */
  corrupt(): void;
  /** The value the last removeIf() was given, exactly as the port received
   * it - unknown, not Checkpoint: discardStoredDraft() deliberately passes a
   * raw unreadable value through removeIf(), so this can hold bytes outside
   * Checkpoint too. */
  lastRemoveIf(): unknown;
}

export function memoryDraftStorage<Checkpoint>(initial: unknown = null): MemoryDraftStorage<Checkpoint> {
  let stored: unknown = initial;
  let id = 0;
  let saveFails = false;
  let replaceFails = false;
  let loadFails = false;
  let lastRemoveIf: unknown = null;
  const storage: DraftPort<Checkpoint> = {
    createId: () => `draft-${++id}`,
    load: () => {
      if (loadFails) throw new Error("disk unavailable");
      return structuredClone(stored);
    },
    save: (checkpoint: Checkpoint) => {
      if (saveFails) throw new Error("disk unavailable");
      stored = structuredClone(checkpoint);
    },
    insertIfAbsent: (checkpoint: Checkpoint) => {
      if (saveFails) throw new Error("disk unavailable");
      if (stored !== null) return false;
      stored = structuredClone(checkpoint);
      return true;
    },
    removeIf: (identity: unknown) => {
      lastRemoveIf = structuredClone(identity);
      // Nothing stored is never a match, whatever identity is named - a null
      // `stored` would otherwise collide with a null/undefined identity's
      // own canonical encoding. Compared canonically (key order normalized),
      // the same compare a byte-aware port runs, so a same-fields-different-
      // key-order record behaves identically here and in production.
      if (stored === null || canonicalJson(identity) !== canonicalJson(stored)) return false;
      stored = null;
      return true;
    },
    replaceIf: (expected: unknown, next: Checkpoint) => {
      if (replaceFails) throw new Error("disk unavailable");
      if (stored === null || canonicalJson(expected) !== canonicalJson(stored)) return false;
      stored = structuredClone(next);
      return true;
    },
  };
  return {
    storage,
    stored: () => structuredClone(stored),
    failSave(fail = true) {
      saveFails = fail;
    },
    failReplace(fail = true) {
      replaceFails = fail;
    },
    failLoad(fail = true) {
      loadFails = fail;
    },
    corrupt() {
      stored = { invalid: true };
    },
    lastRemoveIf: () => lastRemoveIf,
  };
}
