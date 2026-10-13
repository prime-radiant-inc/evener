// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { MemoryStorage } from "../storageTestUtils";
import { browserDraftKey, createBrowserDraftRepository } from "./browserDraftRepository";
import { ANNOTATION_NAMESPACES_STORE, COMPOSER_DRAFTS_STORE } from "./localCompositionSchema";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// Draft transactions gained the outbox's watchdog: a stalled completion
// stops waiting and REJECTS, because a Send's fault tolerance only helps
// once the wait settles - without a deadline, an ordinary Send's preparation
// would hang forever on a stalled transaction. The unobserved outcome is
// honestly reported as a storage fault: every drafts operation re-reads
// durable state on retry (conflict, unavailable, already-applied), so the
// caller's own fault path can take over.
test("a stalled draft transaction stops waiting and the same adapter can reopen and read", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "draft-stall",
    storage: new MemoryStorage(),
  });
  const key = browserDraftKey("root", "https://stall.example");
  await repository.seedDraft(key, { text: "unsent draft", skillNames: [] });
  // fake-indexeddb commits this transaction, but its "complete" event - and
  // with it the load waiting on that event - stays held until release().
  const hold = holdNextWriteTransaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  repository.load(key).catch((error: unknown) => {
    failure = error;
  });
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(failure).toMatchObject({ message: "Browser draft repository transaction timed out" });
  hold.release();
  // The watchdog retired the stalled connection; a fresh read still works and
  // reports the durable state the committed transaction left behind.
  expect((await repository.load(key))?.text).toBe("unsent draft");
  repository.close();
});
