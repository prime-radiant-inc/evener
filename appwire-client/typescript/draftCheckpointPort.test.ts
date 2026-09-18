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

  it("discardClassified is a no-op after load() observes empty storage, even when another writer saves afterward", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    const repo = createDraftRepository(drafts.storage, decode);

    expect(repo.load()).toBeNull();
    // Another writer saves directly to the port after this repository
    // classified the store as empty - a record it never classified.
    drafts.storage.save({ id: "external", value: "x" });
    expect(repo.discardClassified()).toBe(false);

    expect(drafts.stored()).toEqual({ id: "external", value: "x" });
  });

  it("removeIf after re-saving a loaded checkpoint matches the just-written bytes, not the pre-save raw identity", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a", futureField: 1 });
    const repo = createDraftRepository(drafts.storage, decode);

    const loaded = repo.load() as Checkpoint;
    repo.save(loaded);

    expect(repo.removeIf(loaded)).toBe(true);
    expect(drafts.stored()).toBeNull();
  });

  it("removeIf on the same reference load() returned matches what a later save() actually wrote", () => {
    const { storage } = memoryDraftStorage<Checkpoint>({ id: "a", value: "one" });
    const repo = createDraftRepository(storage, decode);
    const loaded = repo.load();
    if (loaded === null) throw new Error("test setup: expected a stored checkpoint");

    // A store that classifies a record, edits it in place and saves it back
    // - the SAME reference load() returned, now holding different content.
    loaded.value = "two";
    repo.save(loaded);

    // removeIf, given that SAME reference, must match what is now actually
    // stored (the edited bytes) - not the pre-edit bytes rawFrom recorded
    // when load() first classified it.
    expect(repo.removeIf(loaded)).toBe(true);
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
