// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import { composerDraftStorageKey, draftStorageKey } from "../panes/session/composer/draft";
import { MemoryStorage } from "../storageTestUtils";
import { browserDraftKey, createBrowserDraftRepository } from "./browserDraftRepository";
import { IndexedDBTransactionTimeoutError, runBoundedTransaction } from "./idbConnection";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// Observe the transaction seam without replacing it: the wrapper runs the real
// runner, so the repository's own timeout factory is what the test inspects.
vi.mock("./idbConnection", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./idbConnection")>();
  return { ...actual, runBoundedTransaction: vi.fn(actual.runBoundedTransaction) };
});

const ORIGIN = "https://drafts.example";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const ORIGINAL_SESSION = "local:superseded-identity";
const OTHER_SESSION = "local:resumed-identity";
const OTHER_TEXT = "the resumed session's recovery text";

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

// Both representations can exist at once: an older tab wrote plain v1 text
// while a newer tab's structured v2 record is already there. The transfer
// imports the newer representation (v2 wins the decode), so the divergent v1
// text is the only copy of that older tab's work. Cleanup must consume only
// the representation the import carried; the unpreserved v1 entry survives,
// and the next scan neither re-imports it (the marker rows exist) nor deletes
// it (this scan imported nothing for that key).
test("a divergent v1 draft survives a transfer that imports its v2 twin", async () => {
  const storage = new MemoryStorage();
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "draft-transfer-divergent-v1",
    storage,
  });
  const key = browserDraftKey("ref_a", ORIGIN);
  const v1Key = draftStorageKey("ref_a");
  const v2Key = composerDraftStorageKey("ref_a");
  const structured = JSON.stringify({ text: "newer structured text", skillNames: [] });
  storage.setItem(v2Key, structured);
  storage.setItem(v1Key, "older tab's divergent text");

  expect(await repository.transferSupportedDrafts(key.namespace)).toEqual({ state: "committed" });
  // The durable row carries v2, the representation the import consumed.
  expect((await repository.load(key))?.text).toBe("newer structured text");
  // The imported representation cleans up as before.
  expect(storage.getItem(v2Key)).toBeNull();
  // The v1 bytes the row does not carry are user work and must survive.
  expect(storage.getItem(v1Key)).toBe("older tab's divergent text");

  // A later scan re-inspects the surviving entry. The marker rows (and the
  // durable row) make the import initialization-only, and nothing was imported
  // in this scan, so cleanup leaves the divergent bytes alone - no re-import,
  // no cleanup loop.
  expect(await repository.transferSupportedDrafts(key.namespace)).toEqual({ state: "committed" });
  expect((await repository.load(key))?.text).toBe("newer structured text");
  expect(storage.getItem(v1Key)).toBe("older tab's divergent text");

  repository.close();
});

// retargetOutbox moves a recovery row from the superseded session to the
// resumed one and rewrites its targetRef while PRESERVING its clientMutationId.
// A reader that pairs the superseded session's draft token with that id after
// the retarget would publish the resumed session's text and attachments into
// the original composer, so both readers must refuse the cross-session record.
async function seedRetargetedRecovery() {
  const indexedDB = new IDBFactory();
  const databaseName = `draft-retarget-${crypto.randomUUID()}`;
  const outbox = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    createMutationId: () => "mutation-retargeted",
  });
  const intent: MutationIntent = {
    targetRef: ORIGINAL_SESSION,
    threadId: "thread-original",
    instanceId: "instance-original",
    method: "turn/queue",
    payload: {
      ref: ORIGINAL_SESSION,
      expectedInstanceId: "instance-original",
      input: [
        { type: "text", text: OTHER_TEXT },
        { type: "image", name: "resumed.png", mediaType: "image/png", data: "cmVzdW1lZA==" },
      ],
    },
    attachments: [
      {
        presentationId: "presentation-resumed",
        marker: 1,
        name: "resumed.png",
        mediaType: "image/png",
        blob: new Blob(["resumed"]),
      },
    ],
    optimisticDisplay: null,
  };
  const queued = await outbox.enqueueIntent(intent);
  await outbox.transferToRecovery(queued.clientMutationId, "rejected");
  const moved = await outbox.retargetOutbox(ORIGINAL_SESSION, OTHER_SESSION, {
    threadId: "thread-resumed",
    instanceId: "instance-resumed",
  });
  // The durable state the retarget leaves behind: the same recovery id, now
  // carrying the other session's targetRef and content.
  expect(moved.recovery.map((record) => record.clientMutationId)).toEqual([queued.clientMutationId]);
  expect(moved.recovery.map((record) => record.targetRef)).toEqual([OTHER_SESSION]);
  outbox.close();

  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName,
    storage: new MemoryStorage(),
  });
  const key = browserDraftKey(ORIGINAL_SESSION, ORIGIN);
  const seeded = await repository.seedDraft(key, {
    text: "the superseded session's own draft",
    skillNames: [],
  });
  expect(seeded.state).toBe("committed");
  if (seeded.state !== "committed") throw new Error("draft seed did not commit");
  return { repository, key, recoveryId: queued.clientMutationId, token: seeded.draft.token };
}

test("readRecoveryPair refuses a recovery record retargeted to another session", async () => {
  const harness = await seedRetargetedRecovery();
  try {
    expect(await harness.repository.readRecoveryPair(harness.key, harness.recoveryId)).toBeNull();
  } finally {
    harness.repository.close();
  }
});

test("readAppendBacking refuses a recovery backing retargeted to another session", async () => {
  const harness = await seedRetargetedRecovery();
  try {
    const preparation: ComposerPreparation = {
      key: harness.key,
      token: harness.token,
      backing: { kind: "recovery", recoveryId: harness.recoveryId },
      localGeneration: 0,
    };
    expect(await harness.repository.readAppendBacking(preparation)).toBeNull();
  } finally {
    harness.repository.close();
  }
});

// The drafts watchdog's machine identity is the shared typed error, not a bare
// Error a caller would have to match by prose. The message still names the
// operation.
test("a draft transaction's watchdog reports the typed timeout", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "draft-transaction-timeout",
    storage: new MemoryStorage(),
  });
  const key = browserDraftKey("ref_a", ORIGIN);
  await repository.seedDraft(key, { text: "timed", skillNames: [] });
  const options = vi.mocked(runBoundedTransaction).mock.calls.at(-1)?.[0];
  if (options === undefined) throw new Error("the seed opened no bounded transaction");
  const timeout = options.timeout();
  expect(timeout).toBeInstanceOf(IndexedDBTransactionTimeoutError);
  expect(timeout.message).toBe("Browser draft repository transaction timed out");
  repository.close();
});
