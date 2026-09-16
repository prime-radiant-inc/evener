// memoryKeybindingDraftStorage is an in-memory KeybindingDraftStorage for the
// draft editor's tests: one stored checkpoint, cloned on the way in and out
// the way a JSON-backed port would, with knobs for the failures the editor
// has to survive (a save that throws, a stored value that is not a
// checkpoint). In-repo test support, not shipped.

import type { KeybindingDraftCheckpoint, KeybindingDraftStorage } from "../keybindingsStore";

export interface MemoryKeybindingDraftStorage {
  storage: KeybindingDraftStorage;
  /** What the port holds right now, as load() would return it. */
  stored(): unknown;
  /** Make save() throw (or stop making it throw). */
  failSave(fail?: boolean): void;
  /** Replace the stored value with something that is not a checkpoint. */
  corrupt(): void;
}

export function memoryKeybindingDraftStorage(): MemoryKeybindingDraftStorage {
  let stored: unknown = null;
  let id = 0;
  let saveFails = false;
  const storage: KeybindingDraftStorage = {
    createId: () => String(++id),
    load: () => structuredClone(stored),
    save: (checkpoint: KeybindingDraftCheckpoint) => {
      if (saveFails) throw new Error("disk unavailable");
      stored = structuredClone(checkpoint);
    },
    removeIf: (checkpoint: KeybindingDraftCheckpoint) => {
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
  };
}
