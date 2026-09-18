// memoryDraftStorage is an in-memory draft port for any settings store's
// checkpointed editor: one stored checkpoint, cloned on the way in and out the
// way a JSON-backed port would, compared by JSON bytes on removeIf the way the
// native port does, with knobs for the failures it has to survive (a save
// that throws, a stored value that is not a checkpoint). Ids are non-integer
// strings like production's UUIDs: an integer-like id is an index-like key V8
// orders first, which would hide a key-order mismatch between what was saved
// and what removeIf is later given. In-repo test support, not shipped.

import type { DraftPort } from "../draftCheckpointPort";

export interface MemoryDraftStorage<Checkpoint> {
  storage: DraftPort<Checkpoint>;
  /** What the port holds right now, as load() would return it. */
  stored(): Checkpoint | null;
  /** Make save() throw (or stop making it throw). */
  failSave(fail?: boolean): void;
  /** Replace the stored value with something that is not a checkpoint. */
  corrupt(): void;
  /** The checkpoint the last removeIf() was given, as the port received it. */
  lastRemoveIf(): Checkpoint | null;
}

export function memoryDraftStorage<Checkpoint>(initial: unknown = null): MemoryDraftStorage<Checkpoint> {
  let stored: unknown = initial;
  let id = 0;
  let saveFails = false;
  let lastRemoveIf: Checkpoint | null = null;
  const storage: DraftPort<Checkpoint> = {
    createId: () => `draft-${++id}`,
    load: () => structuredClone(stored),
    save: (checkpoint: Checkpoint) => {
      if (saveFails) throw new Error("disk unavailable");
      stored = structuredClone(checkpoint);
    },
    removeIf: (checkpoint: Checkpoint) => {
      lastRemoveIf = structuredClone(checkpoint);
      if (JSON.stringify(checkpoint) !== JSON.stringify(stored)) return false;
      stored = null;
      return true;
    },
  };
  return {
    storage,
    stored: () => structuredClone(stored) as Checkpoint | null,
    failSave(fail = true) {
      saveFails = fail;
    },
    corrupt() {
      stored = { invalid: true };
    },
    lastRemoveIf: () => lastRemoveIf,
  };
}
