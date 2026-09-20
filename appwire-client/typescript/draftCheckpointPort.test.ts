import { describe, expect, it } from "vitest";
import { createDraftRepository, type DraftPort, discardStoredDraft } from "./draftCheckpointPort";
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
  it("reports whether reload saw the same classified identity, not just the same decoded content", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);

    repo.load();
    expect(repo.reload()).toEqual({ checkpoint: { id: "d1", value: "a" }, sameIdentity: true });

    // Equal decoded fields do not prove that the same record survived: a new
    // writer's identity must take the ordinary replacement path.
    drafts.storage.save({ id: "d2", value: "a" });
    expect(repo.reload()).toEqual({ checkpoint: { id: "d2", value: "a" }, sameIdentity: false });
  });

  it("reports a changed identity when a replacement decodes identically but has different raw bytes", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a", futureField: 1 });
    const repo = createDraftRepository(drafts.storage, decode);

    repo.load();
    // The replacement decodes to the SAME checkpoint, { id: "d1", value: "a" }
    // - only its canonical raw bytes differ, by the extra field `decode`
    // drops. Equal decoded fields do not make it the classified record: an
    // identity compare over decoded values would carry the previous
    // classification forward across a record that was actually replaced.
    drafts.storage.save({ id: "d1", value: "a" });
    expect(repo.reload()).toEqual({ checkpoint: { id: "d1", value: "a" }, sameIdentity: false });
  });

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

  it("save() refuses to overwrite a record another writer replaced after this repository classified it", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);

    repo.load();
    // A second writer replaces the record directly on the port, bypassing
    // this repository's own classification - the same race
    // discardClassified/replaceClassified already guard against, but save()
    // (editDraft/saveDraft/rebaseDraft's own persist) did not.
    drafts.storage.save({ id: "d2", value: "b" });

    expect(repo.save({ id: "d1", value: "edited" })).toBe(false);
    expect(drafts.stored()).toEqual({ id: "d2", value: "b" });
  });

  it("save() succeeds and reclassifies when nothing has replaced the record since it was classified", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);

    repo.load();

    expect(repo.save({ id: "d1", value: "edited" })).toBe(true);
    expect(drafts.stored()).toEqual({ id: "d1", value: "edited" });
  });

  it("save() refuses to overwrite a record another writer inserted after this repository classified as absent", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);

    const loaded = repo.load() as Checkpoint;
    expect(repo.removeIf(loaded)).toBe(true);

    // Another writer creates a BRAND NEW record directly on the port while
    // this repository still classifies the store as absent - there was no
    // existing record for save() to CAS against, so an unconditional write
    // (the bug #1884 tracked) would silently clobber it.
    drafts.storage.save({ id: "external", value: "x" });

    expect(repo.save({ id: "d2", value: "b" })).toBe(false);
    expect(drafts.stored()).toEqual({ id: "external", value: "x" });
  });

  it("save() succeeds unconditionally after removeIf classified the store as absent", () => {
    // removeIf's own success means storage is genuinely empty now, the same
    // postcondition load() classifying "absent" describes - a later save()
    // must not CAS against the just-removed (and now stale) identity.
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);

    const loaded = repo.load() as Checkpoint;
    expect(repo.removeIf(loaded)).toBe(true);

    expect(repo.save({ id: "d2", value: "b" })).toBe(true);
    expect(drafts.stored()).toEqual({ id: "d2", value: "b" });
  });

  it("save() succeeds unconditionally after discardClassified classified the store as absent", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const repo = createDraftRepository(drafts.storage, decode);

    repo.load();
    expect(repo.discardClassified()).toBe(true);

    expect(repo.save({ id: "d2", value: "b" })).toBe(true);
    expect(drafts.stored()).toEqual({ id: "d2", value: "b" });
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

  it("discardClassified and replaceClassified refuse when nothing has been classified yet, even over a corrupt record", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    const repo = createDraftRepository(drafts.storage, decode);

    drafts.corrupt();
    // This repository has never called load() or save(): it has no identity
    // to act on, so both calls refuse rather than classifying via a fresh
    // reload here - even a record nothing could read is not this call's to
    // touch until this repository has classified it.
    expect(repo.discardClassified()).toBe(false);
    expect(repo.replaceClassified({ id: "d1", value: "a" })).toBe(false);

    expect(drafts.stored()).toEqual({ invalid: true });
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

    expect(discardStoredDraft(drafts.storage)).toBe("removed");

    expect(drafts.stored()).toBeNull();
  });

  it("does nothing when nothing is stored", () => {
    const drafts = memoryDraftStorage<Checkpoint>();

    expect(discardStoredDraft(drafts.storage)).toBe("absent");

    expect(drafts.stored()).toBeNull();
  });

  // A discard call with no live repository has no identity from when the
  // caller was originally shown the unreadable record - only what is
  // stored NOW. Without isReadable, that gap would let it delete a record a
  // concurrent writer replaced with something this build can read since.
  it("refuses to remove a record that now decodes as valid, given an isReadable check", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const isReadable = (value: unknown) => {
      try {
        decode(value);
        return true;
      } catch {
        return false;
      }
    };

    expect(discardStoredDraft(drafts.storage, isReadable)).toBe("refused");

    expect(drafts.stored()).toEqual({ id: "d1", value: "a" });
  });

  it("still removes a record isReadable reports as unreadable", () => {
    const drafts = memoryDraftStorage<Checkpoint>();
    drafts.corrupt();
    const isReadable = (value: unknown) => {
      try {
        decode(value);
        return true;
      } catch {
        return false;
      }
    };

    expect(discardStoredDraft(drafts.storage, isReadable)).toBe("removed");

    expect(drafts.stored()).toBeNull();
  });

  // removeIf's compare-and-swap can fail for a reason isReadable never sees:
  // a concurrent writer replaced the unreadable record with a DIFFERENT
  // unreadable one between load() and removeIf(). That record is still
  // present - "absent" would tell a caller (the offline discard button)
  // there was nothing there to clear, when there is.
  it("reports 'refused', not 'absent', when removeIf fails but a replacement record is still present", () => {
    let loadCount = 0;
    const first = { corrupt: true, marker: 1 };
    const second = { corrupt: true, marker: 2 };
    const storage: DraftPort<Checkpoint> = {
      createId: () => "id",
      load: () => (loadCount++ === 0 ? first : second),
      save: () => {},
      insertIfAbsent: () => true,
      // Simulates the race: the identity this call names no longer matches
      // what is stored (a concurrent writer already replaced it).
      removeIf: () => false,
      replaceIf: () => false,
    };

    expect(discardStoredDraft(storage)).toBe("refused");
  });

  it("reports 'absent' when removeIf fails and a re-read finds the record genuinely gone", () => {
    let loadCount = 0;
    const first = { corrupt: true, marker: 1 };
    const storage: DraftPort<Checkpoint> = {
      createId: () => "id",
      load: () => (loadCount++ === 0 ? first : null),
      save: () => {},
      insertIfAbsent: () => true,
      removeIf: () => false,
      replaceIf: () => false,
    };

    expect(discardStoredDraft(storage)).toBe("absent");
  });

  // Every DraftPort method may throw a genuine storage failure (see the
  // port's own docs) - unlike a live repository's discardDraft, there is no
  // caller-supplied catch upstream of this call, so a throw here must
  // degrade to an outcome, the same posture readDraftOutcome takes on load(),
  // rather than escape a store-free caller's event handler uncaught.
  it("degrades to 'storageUnavailable' when load() throws", () => {
    const storage: DraftPort<Checkpoint> = {
      createId: () => "id",
      load: () => {
        throw new Error("disk unavailable");
      },
      save: () => {},
      insertIfAbsent: () => true,
      removeIf: () => false,
      replaceIf: () => false,
    };

    expect(discardStoredDraft(storage)).toBe("storageUnavailable");
  });

  it("degrades to 'storageUnavailable' when removeIf() throws", () => {
    const drafts = memoryDraftStorage<Checkpoint>({ id: "d1", value: "a" });
    const storage: DraftPort<Checkpoint> = {
      ...drafts.storage,
      removeIf: () => {
        throw new Error("disk unavailable");
      },
    };

    expect(discardStoredDraft(storage)).toBe("storageUnavailable");
  });

  it("degrades to 'storageUnavailable' when the re-read after a failed removeIf throws", () => {
    let loadCount = 0;
    const storage: DraftPort<Checkpoint> = {
      createId: () => "id",
      load: () => {
        loadCount++;
        if (loadCount === 1) return { corrupt: true, marker: 1 };
        throw new Error("disk unavailable");
      },
      save: () => {},
      insertIfAbsent: () => true,
      removeIf: () => false,
      replaceIf: () => false,
    };

    expect(discardStoredDraft(storage)).toBe("storageUnavailable");
  });
});

// memoryDraftStorage's own removeIf/replaceIf compare by JSON.stringify -
// JSON.stringify(null) and JSON.stringify(undefined) both stringify to
// values that must never accidentally equal each other or an absent
// record's own comparison, or a caller checking "is anything stored" via a
// null/undefined identity would see a false compare-and-swap success.
describe("memoryDraftStorage", () => {
  it("removeIf(null) reports false when nothing is stored, never a false match", () => {
    const drafts = memoryDraftStorage<Checkpoint>();

    expect(drafts.storage.removeIf(null)).toBe(false);
    expect(drafts.stored()).toBeNull();
  });

  it("removeIf(undefined) reports false when nothing is stored, never a false match", () => {
    const drafts = memoryDraftStorage<Checkpoint>();

    expect(drafts.storage.removeIf(undefined)).toBe(false);
    expect(drafts.stored()).toBeNull();
  });

  it("replaceIf(null, ...) reports false when nothing is stored, never a false match", () => {
    const drafts = memoryDraftStorage<Checkpoint>();

    expect(drafts.storage.replaceIf(null, { id: "d1", value: "a" })).toBe(false);
    expect(drafts.stored()).toBeNull();
  });

  it("replaceIf(undefined, ...) reports false when nothing is stored, never a false match", () => {
    const drafts = memoryDraftStorage<Checkpoint>();

    expect(drafts.storage.replaceIf(undefined, { id: "d1", value: "a" })).toBe(false);
    expect(drafts.stored()).toBeNull();
  });
});
