// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import type { ComposerPreparation } from "../panes/session/composer/composerContract";
import { draftStorageKey } from "../panes/session/composer/draft";
import { MemoryStorage } from "../storageTestUtils";
import { browserDraftKey, createBrowserDraftRepository } from "./browserDraftRepository";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

const ORIGIN = "https://drafts.example";

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
