import { describe, expect, it, vi } from "vitest";
import { assertDraftDiscardable, discardCheckpointedDraft, persistCheckpointedDraft } from "./checkpointedDraftEditor";
import { createDraftRepository } from "./draftCheckpointPort";
import { memoryDraftStorage } from "./testing/draftStorage";

interface Checkpoint {
  id: string;
  value: string;
}

function decode(value: unknown): Checkpoint {
  if (value === null || typeof value !== "object") throw new Error("invalid checkpoint");
  const item = value as Record<string, unknown>;
  if (typeof item.id !== "string" || typeof item.value !== "string") throw new Error("invalid checkpoint");
  return { id: item.id, value: item.value };
}

interface Fields {
  saving: boolean;
  writeUncertain: boolean;
  storageUnavailable: boolean;
  draftUnreadable: boolean;
  draft: { value: string } | null;
  draftConflict: boolean;
  draftError: string | null;
}

function initialFields(overrides: Partial<Fields> = {}): Fields {
  return {
    saving: false,
    writeUncertain: false,
    storageUnavailable: false,
    draftUnreadable: false,
    draft: null,
    draftConflict: true,
    draftError: null,
    ...overrides,
  };
}

function fieldsStore(initial: Fields) {
  let state = initial;
  return {
    getState: () => state,
    setState: (partial: Partial<Fields>) => {
      state = { ...state, ...partial };
    },
  };
}

describe("assertDraftDiscardable", () => {
  it("refuses while a write is saving", () => {
    const store = fieldsStore(initialFields({ saving: true }));
    expect(() => assertDraftDiscardable({ disposed: false }, store.getState, "unavailable")).toThrow("unavailable");
  });

  it("refuses while a write is uncertain", () => {
    const store = fieldsStore(initialFields({ writeUncertain: true }));
    expect(() => assertDraftDiscardable({ disposed: false }, store.getState, "unavailable")).toThrow("unavailable");
  });

  it("refuses once the store is disposed", () => {
    const store = fieldsStore(initialFields());
    expect(() => assertDraftDiscardable({ disposed: true }, store.getState, "unavailable")).toThrow("unavailable");
  });

  it("refuses a genuine port failure", () => {
    const store = fieldsStore(initialFields({ storageUnavailable: true, draftUnreadable: false }));
    expect(() => assertDraftDiscardable({ disposed: false }, store.getState, "unavailable")).toThrow("unavailable");
  });

  it("allows discarding an unreadable record even though the port is marked unavailable - that IS the fix", () => {
    const store = fieldsStore(initialFields({ storageUnavailable: true, draftUnreadable: true }));
    expect(() => assertDraftDiscardable({ disposed: false }, store.getState, "unavailable")).not.toThrow();
  });

  it("allows discarding an idle, readable draft", () => {
    const store = fieldsStore(initialFields());
    expect(() => assertDraftDiscardable({ disposed: false }, store.getState, "unavailable")).not.toThrow();
  });
});

describe("persistCheckpointedDraft", () => {
  it("mints an id and saves the checkpoint, returning it", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    const repo = createDraftRepository(drafts.storage, decode);
    const store = fieldsStore(initialFields());

    const checkpoint = persistCheckpointedDraft(repo, { value: "a" }, store.setState, "save failed");

    expect(checkpoint).toEqual({ id: expect.any(String), value: "a" });
    expect(drafts.stored()).toEqual(checkpoint);
  });

  it("marks storageUnavailable AND draftError, then throws, when the port throws", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    const repo = createDraftRepository(drafts.storage, decode);
    drafts.failSave();
    const store = fieldsStore(initialFields());

    expect(() => persistCheckpointedDraft(repo, { value: "a" }, store.setState, "save failed")).toThrow(
      "save failed",
    );

    expect(store.getState()).toMatchObject({ storageUnavailable: true, draftError: "save failed" });
  });

  it("refuses (storageUnavailable AND draftError) when another writer replaced the classified record", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);
    repo.load();
    // A second writer replaces the record directly on the port, bypassing
    // this repository's own classification - persistCheckpointedDraft's
    // save() must refuse rather than overwrite it.
    drafts.storage.save({ id: "d2", value: "b" });
    const store = fieldsStore(initialFields());

    expect(() => persistCheckpointedDraft(repo, { value: "edited" }, store.setState, "save failed")).toThrow(
      "save failed",
    );

    expect(store.getState()).toMatchObject({ storageUnavailable: true, draftError: "save failed" });
    expect(drafts.stored()).toEqual({ id: "d2", value: "b" });
  });
});

describe("discardCheckpointedDraft", () => {
  it("removes the classified record and clears the draft, storage and unreadable flags", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);
    repo.load();
    const store = fieldsStore(
      initialFields({ draft: { value: "a" }, draftConflict: true, storageUnavailable: true, draftUnreadable: true }),
    );
    const restoreDraft = vi.fn();

    discardCheckpointedDraft(repo, store.getState, store.setState, restoreDraft, "discard failed");

    expect(drafts.stored()).toBeNull();
    expect(store.getState()).toMatchObject({
      draft: null,
      draftConflict: false,
      draftError: null,
      storageUnavailable: false,
      draftUnreadable: false,
    });
    expect(restoreDraft).not.toHaveBeenCalled();
  });

  it("adopts a replacement via restoreDraft when the classified record was already replaced", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);
    repo.load();
    drafts.storage.save({ id: "d2", value: "b" });
    const store = fieldsStore(initialFields({ draft: { value: "a" } }));
    const restoreDraft = vi.fn(() => ({ draft: { value: "b" } }));

    discardCheckpointedDraft(repo, store.getState, store.setState, restoreDraft, "discard failed");

    expect(drafts.stored()).toEqual({ id: "d2", value: "b" });
    expect(restoreDraft).toHaveBeenCalledTimes(1);
    expect(store.getState().draft).toEqual({ value: "b" });
  });

  it("marks storageUnavailable and throws when discardClassified itself throws", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);
    repo.load();
    const store = fieldsStore(initialFields());
    const restoreDraft = vi.fn();
    const throwingRepo = {
      ...repo,
      discardClassified: () => {
        throw new Error("disk unavailable");
      },
    };

    expect(() =>
      discardCheckpointedDraft(throwingRepo, store.getState, store.setState, restoreDraft, "discard failed"),
    ).toThrow("discard failed");

    expect(store.getState()).toMatchObject({ storageUnavailable: true, draftError: "discard failed" });
    expect(restoreDraft).not.toHaveBeenCalled();
  });
});
