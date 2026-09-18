import { describe, expect, it } from "vitest";
import { createDraftRepository, discardStoredDraft } from "./draftCheckpointPort";
import { memoryDraftStorage } from "./testing/draftStorage";

interface Checkpoint {
  id: string;
  value: string;
}

function decode(value: unknown): Checkpoint {
  if (value === null || typeof value !== "object") throw new Error("invalid checkpoint");
  const item = value as Record<string, unknown>;
  if (typeof item.id !== "string" || typeof item.value !== "string") throw new Error("invalid checkpoint");
  // Only the fields this decoder knows survive - a record written by a
  // build with extra fields normalizes to one without them.
  return { id: item.id, value: item.value };
}

describe("createDraftRepository", () => {
  it("removeIf on a checkpoint load() returned removes the exact stored bytes, extra fields included", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a", futureField: 1 });
    const repo = createDraftRepository(drafts.storage, decode);

    const loaded = repo.load();
    expect(loaded).toEqual({ id: "d1", value: "a" });
    repo.removeIf(loaded as Checkpoint);

    expect(drafts.stored()).toBeNull();
  });

  it("discardClassified after save removes the record save() just wrote", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    const repo = createDraftRepository(drafts.storage, decode);

    repo.save({ id: "d1", value: "a" });
    repo.discardClassified();

    expect(drafts.stored()).toBeNull();
  });

  it("discardClassified refuses when another writer replaced the classified record", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);

    repo.load();
    // A second writer replaces the record directly on the port, bypassing
    // this repository's own classification.
    drafts.storage.save({ id: "d2", value: "b" });
    repo.discardClassified();

    expect(drafts.stored()).toEqual({ id: "d2", value: "b" });
  });

  it("discardClassified falls back to a fresh reload and removes a corrupt record nothing has classified yet", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    const repo = createDraftRepository(drafts.storage, decode);

    drafts.corrupt();
    repo.discardClassified();

    expect(drafts.stored()).toBeNull();
  });
});

describe("discardStoredDraft", () => {
  it("removes an unreadable stored record with no repository and no decode", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    drafts.corrupt();

    discardStoredDraft(drafts.storage);

    expect(drafts.stored()).toBeNull();
  });

  it("does nothing when nothing is stored", () => {
    const drafts = memoryDraftStorage<Checkpoint>();

    discardStoredDraft(drafts.storage);

    expect(drafts.stored()).toBeNull();
  });
});
