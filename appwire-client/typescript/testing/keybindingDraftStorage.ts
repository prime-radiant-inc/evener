// memoryKeybindingDraftStorage is an in-memory KeybindingDraftStorage for the
// draft editor's tests: one stored checkpoint, cloned on the way in and out
// the way a JSON-backed port would, compared by JSON bytes on removeIf the way
// the native port does, with knobs for the failures the editor has to survive
// (a save that throws, a stored value that is not a checkpoint). Ids are
// non-integer strings like production's UUIDs: an integer-like id is an
// index-like key V8 orders first, which would hide a key-order mismatch
// between what was saved and what removeIf is later given. In-repo test
// support, not shipped.

import type { KeybindingDraftCheckpoint, KeybindingDraftStorage } from "../keybindingsStore";

export interface MemoryKeybindingDraftStorage {
  storage: KeybindingDraftStorage;
  /** What the port holds right now, as load() would return it. */
  stored(): unknown;
  /** Make save() throw (or stop making it throw). */
  failSave(fail?: boolean): void;
  /** Replace the stored value with something that is not a checkpoint. */
  corrupt(): void;
  /** The checkpoint the last removeIf() was given, as the port received it. */
  lastRemoveIf(): KeybindingDraftCheckpoint | null;
}

export function memoryKeybindingDraftStorage(): MemoryKeybindingDraftStorage {
  let stored: unknown = null;
  let id = 0;
  let saveFails = false;
  let lastRemoveIf: KeybindingDraftCheckpoint | null = null;
  const storage: KeybindingDraftStorage = {
    createId: () => `draft-${++id}`,
    load: () => structuredClone(stored),
    save: (checkpoint: KeybindingDraftCheckpoint) => {
      if (saveFails) throw new Error("disk unavailable");
      stored = structuredClone(checkpoint);
    },
    removeIf: (checkpoint: KeybindingDraftCheckpoint) => {
      lastRemoveIf = structuredClone(checkpoint);
      if (JSON.stringify(checkpoint) === JSON.stringify(stored)) stored = null;
    },
  };
  return {
    storage,
    stored: () => structuredClone(stored),
    failSave(fail = true) {
      saveFails = fail;
    },
    corrupt() {
      stored = { invalid: true };
    },
    lastRemoveIf: () => lastRemoveIf,
  };
}
