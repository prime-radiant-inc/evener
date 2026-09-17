// memoryDraftStorage is an in-memory draft port for any settings store's
// checkpointed editor: one stored checkpoint, cloned on the way in and out the
// way a JSON-backed port would, compared by JSON bytes on removeIf the way the
// native port does, with a call log for the orderings the editor has to keep
// (checkpoint before the request leaves, storage last) and knobs for the
// failures it has to survive (a save that throws, a stored value that is not a
// checkpoint). Ids are non-integer strings like production's UUIDs: an
// integer-like id is an index-like key V8 orders first, which would hide a
// key-order mismatch between what was saved and what removeIf is later given.
// In-repo test support, not shipped.

/** The shape both stores' storage ports share, over any checkpoint type. */
interface DraftPort<Checkpoint> {
  createId(): string;
  load(): unknown;
  save(checkpoint: Checkpoint): void;
  removeIf(checkpoint: Checkpoint): void;
}

export interface MemoryDraftStorage<Checkpoint> {
  storage: DraftPort<Checkpoint>;
  /** What the port holds right now, as load() would return it. */
  stored(): Checkpoint | null;
  /** Every call the port received, in order. A save records whether the
   * checkpoint it was given claims an uncertain outcome. */
  calls: string[];
  /** Make save() throw (or stop making it throw). */
  failSave(fail?: boolean): void;
  /** Replace the stored value with something that is not a checkpoint. */
  corrupt(): void;
  /** The checkpoint the last removeIf() was given, as the port received it. */
  lastRemoveIf(): Checkpoint | null;
}

export function memoryDraftStorage<Checkpoint extends { writeUncertain: boolean }>(
  initial: unknown = null,
): MemoryDraftStorage<Checkpoint> {
  let stored: unknown = initial;
  let id = 0;
  let saveFails = false;
  let lastRemoveIf: Checkpoint | null = null;
  const calls: string[] = [];
  const storage: DraftPort<Checkpoint> = {
    createId: () => `draft-${++id}`,
    load: () => {
      calls.push("load");
      return structuredClone(stored);
    },
    save: (checkpoint) => {
      calls.push(`save:${checkpoint.writeUncertain ? "uncertain" : "settled"}`);
      if (saveFails) throw new Error("disk unavailable");
      stored = structuredClone(checkpoint);
    },
    removeIf: (checkpoint) => {
      calls.push("removeIf");
      lastRemoveIf = structuredClone(checkpoint);
      if (JSON.stringify(checkpoint) === JSON.stringify(stored)) stored = null;
    },
  };
  return {
    storage,
    calls,
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
