// Draft-owned provenance for onboarding: an explicit model choice bumps a
// revision even when the value does not change, so automatic discovery can
// tell "the person chose this" from "we filled it in" without comparing model
// strings. The connection scope is also draft-owned, so it survives a remount
// and stays with its own draft rather than leaking to another scope.

import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { defaultsKeyFor, GLOBAL_MODEL_KEY, GLOBAL_WORKING_DIR_KEY } from "./spawnDefaults";
import {
  markModelChoice,
  reseedSpawnDirectory,
  resetSpawnDraftsForTests,
  selectSpawnDirectory,
  setDraftField,
  spawnDraftsStore,
} from "./spawnDrafts";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  localStorage.clear();
  resetSpawnDraftsForTests();
});

afterEach(() => {
  localStorage.clear();
  resetSpawnDraftsForTests();
});

describe("modelChoiceRevision", () => {
  test("starts at 0 when the draft has no saved model", () => {
    const draft = selectSpawnDirectory("/tmp/no-saved-model");
    expect(draft.fields.getState().model).toBe("");
    expect(draft.fields.getState().modelChoiceRevision).toBe(0);
  });

  test("starts at 1 for a nonempty saved model", () => {
    localStorage.setItem(GLOBAL_WORKING_DIR_KEY, "/tmp/saved-model");
    localStorage.setItem(GLOBAL_MODEL_KEY, "openai/gpt-5");

    const draft = selectSpawnDirectory("/tmp/saved-model");
    expect(draft.fields.getState().model).toBe("openai/gpt-5");
    expect(draft.fields.getState().modelChoiceRevision).toBe(1);
  });

  test("increments for every explicit choice, including an unchanged empty model", () => {
    const draft = selectSpawnDirectory("/tmp/explicit-empty");
    expect(draft.fields.getState().model).toBe("");
    expect(draft.fields.getState().modelChoiceRevision).toBe(0);

    markModelChoice(draft);
    markModelChoice(draft);

    // The value is still empty: a revision, not a string comparison, is what
    // records that the person explicitly chose the empty/default value twice.
    expect(draft.fields.getState().model).toBe("");
    expect(draft.fields.getState().modelChoiceRevision).toBe(2);
  });

  test("is not the same-value no-op setDraftField performs", () => {
    const draft = selectSpawnDirectory("/tmp/same-value");
    setDraftField(draft, "model", "team-local/chosen");
    expect(draft.fields.getState().modelChoiceRevision).toBe(0);

    // Setting the identical value is deliberately a no-op in the draft owner,
    // so provenance has to be recorded through markModelChoice at the entry
    // point instead.
    setDraftField(draft, "model", "team-local/chosen");
    expect(draft.fields.getState().modelChoiceRevision).toBe(0);

    markModelChoice(draft);
    expect(draft.fields.getState().model).toBe("team-local/chosen");
    expect(draft.fields.getState().modelChoiceRevision).toBe(1);
  });

  test("survives leaving and re-selecting the same draft", () => {
    const draft = selectSpawnDirectory("/tmp/revision-a");
    markModelChoice(draft);
    selectSpawnDirectory("/tmp/revision-b");
    const returned = selectSpawnDirectory("/tmp/revision-a");

    expect(returned).toBe(draft);
    expect(returned.fields.getState().modelChoiceRevision).toBe(1);
  });
});

describe("providerConnectionScope", () => {
  test("starts null and is not persisted", () => {
    const draft = selectSpawnDirectory("/tmp/scope-none");
    expect(draft.fields.getState().providerConnectionScope).toBeNull();
    expect(localStorage.length).toBe(0);
  });

  test("belongs to one draft and survives leaving and returning to it", () => {
    const first = selectSpawnDirectory("/tmp/scope-a");
    const scope = { source: "local", harness: "evener", cwd: "/tmp/scope-a" };
    setDraftField(first, "providerConnectionScope", scope);
    markModelChoice(first);

    const second = selectSpawnDirectory("/tmp/scope-b");
    expect(second.fields.getState().providerConnectionScope).toBeNull();
    expect(second.fields.getState().modelChoiceRevision).toBe(0);

    const returned = selectSpawnDirectory("/tmp/scope-a");
    expect(returned).toBe(first);
    expect(returned.fields.getState().providerConnectionScope).toEqual(scope);
    expect(localStorage.length).toBe(0);
  });

  test("is separate from the saved per-project defaults blob", () => {
    localStorage.setItem(defaultsKeyFor("/tmp/scope-saved"), JSON.stringify({ model: "openai/gpt-5" }));
    const draft = selectSpawnDirectory("/tmp/scope-saved");
    setDraftField(draft, "providerConnectionScope", {
      source: "local",
      harness: "external",
      cwd: "/tmp/scope-saved",
    });

    // The in-memory scope is not an onboarding-completed flag and never rides
    // the sticky defaults a later mount reads.
    expect(JSON.parse(localStorage.getItem(defaultsKeyFor("/tmp/scope-saved")) ?? "null")).toEqual({
      model: "openai/gpt-5",
    });
  });

  test("a reseeded draft keeps its choice revision and replaces the scope it stores", () => {
    const draft = selectSpawnDirectory("/tmp/scope-reseed");
    setDraftField(draft, "providerConnectionScope", {
      source: "local",
      harness: "evener",
      cwd: "/tmp/scope-reseed",
    });
    markModelChoice(draft);
    reseedSpawnDirectory(draft, "/tmp/scope-reseed-moved");

    const current = spawnDraftsStore.getState().current;
    expect(current?.fields.getState().modelChoiceRevision).toBe(1);
    expect(current?.fields.getState().providerConnectionScope?.cwd).toBe("/tmp/scope-reseed");
  });
});
