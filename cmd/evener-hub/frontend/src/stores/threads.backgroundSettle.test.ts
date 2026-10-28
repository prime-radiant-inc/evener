import "fake-indexeddb/auto";
import type { AnyNotification } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";
import { acquireBackgroundThread, resetThreadsStoreForTests, settleBackgroundTranscriptsForTests } from "./threads";

// Mechanism probe for settleBackgroundTranscriptsForTests. Before 6d-2b the
// transcript write/checkpoint work lived in TranscriptResidency and its
// settled() was this helper's drain point. After the reduction that work lives
// in each entry route's TranscriptRepository, so the helper must drain the real
// repository work. This probe holds a live repository write transaction open and
// asserts the helper does not resolve until it is released: a no-op helper
// resolves here immediately and fails the first assertion.
const REF = "local:settle-probe";
// The entry cache's four-store readwrite transaction (sessionEntryCache
// WRITE_STORES). Holding exactly this scope stalls one real repository write.
const WRITE_STORES = ["transcriptItems", "transcriptTurns", "transcriptCoverage", "transcriptMeta"];

let cache: SessionEntryCache;

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `background-settle-probe-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});

afterEach(() => {
  resetThreadsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cache.close();
});

function connectClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => ({
    ...currentRead(params.ref ?? REF),
    requestGeneration: params.requestGeneration,
  }));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function completedFrame(ref: string, position: number): AnyNotification {
  return {
    method: "history/updated",
    params: {
      ref,
      threadId: `thr_${ref}`,
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: position },
      turns: [{ id: `turn_${position}`, status: "completed", itemsView: "full", version: 1, items: [] }],
      items: [
        {
          type: "agentMessage",
          id: `item_${position}`,
          turnId: `turn_${position}`,
          transcriptKey: `apptranscript-item-v2:turn_${position}:0:0`,
          position: { entry: position, item: 0 },
          version: 1,
          text: `${position}:`.padEnd(1024, "x"),
          status: "completed",
        },
      ],
    },
  } satisfies AnyNotification;
}

test("settleBackgroundTranscriptsForTests waits for a real in-flight repository write", async () => {
  const fake = connectClient();
  const background = acquireBackgroundThread(REF);
  let hold: ReturnType<typeof holdNextWriteTransaction> | undefined;
  try {
    await background.ready;
    const repository = productionEntryTranscriptForTests(REF).repository;
    if (!repository) throw new Error("expected the ordinary repository");
    // Drain the acquisition's own read/write work so the next write we hold is
    // the one this notification issues.
    await repository.settled();

    hold = holdNextWriteTransaction(WRITE_STORES);
    fake.emitNotification(completedFrame(REF, 1));
    // The accept path issues the background batch synchronously; wait until the
    // held transaction has been opened and its completion event delivered (the
    // work is durably done yet still in flight for everything awaiting it).
    await hold.reached;

    const settle = settleBackgroundTranscriptsForTests();
    let settled = false;
    void settle.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      },
    );
    // Two real macrotask hops: enough for a no-op helper to have resolved.
    await nextRealTask();
    await nextRealTask();
    expect(settled).toBe(false);

    hold.release();
    await settle;
    expect(settled).toBe(true);
  } finally {
    hold?.release();
    await background.release();
  }
});
