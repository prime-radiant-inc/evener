// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import { draftStorageKey } from "../panes/session/composer/draft";
import { MemoryStorage } from "../storageTestUtils";
import { browserDraftKey, createBrowserDraftRepository } from "./browserDraftRepository";

const ORIGIN = "https://drafts.example";

// Legacy transfer imports once and cleans up after a successful import. A later
// edit from an older tab must survive the next scan: the transfer skipped that
// ref because its durable row already existed, so nothing in this scan imported
// the divergent bytes and deleting them would lose the newer composition.
test("a legacy edit after the first import survives a repeated transfer", async () => {
  const storage = new MemoryStorage();
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "draft-transfer-repeat",
    storage,
  });
  const key = browserDraftKey("ref_a", ORIGIN);
  const legacyKey = draftStorageKey("ref_a");
  storage.setItem(legacyKey, "draft A");

  expect(await repository.transferSupportedDrafts(key.namespace)).toEqual({ state: "committed" });
  expect((await repository.load(key))?.text).toBe("draft A");
  // The import consumed the legacy value exactly as before.
  expect(storage.getItem(legacyKey)).toBeNull();

  // An older tab writes its newer composition under the same legacy key after
  // this scan's cleanup.
  storage.setItem(legacyKey, "draft B");

  expect(await repository.transferSupportedDrafts(key.namespace)).toEqual({ state: "committed" });
  expect((await repository.load(key))?.text).toBe("draft A");
  // This scan imported nothing for ref_a, so it must not destroy draft B.
  expect(storage.getItem(legacyKey)).toBe("draft B");

  repository.close();
});
