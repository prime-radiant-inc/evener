// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { MutationIntent } from "../../../../stores/mutationOutbox";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests } from "../../../../stores/threads";
import {
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
  useBlockedMutationEntries,
} from "./pendingTurnsStore";

function queueIntent(targetRef: string, text: string): MutationIntent {
  const input = [{ type: "text" as const, text }];
  return {
    targetRef,
    threadId: `thread-${targetRef}`,
    method: "turn/queue",
    payload: { ref: targetRef, expectedTurnId: "turn-a", input },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
  };
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  globalThis.indexedDB = new IDBFactory();
});

test("an all-target refresh replaces the complete blocked projection", async () => {
  let nextId = 0;
  const storage = new MutationOutboxIndexedDB({
    indexedDB: globalThis.indexedDB,
    databaseName: "pending-turns-edge",
    createMutationId: () => `mutation-${++nextId}`,
  });
  setMutationStorageForTests(storage);

  const blockedA = await storage.enqueueIntent(queueIntent("ref-a", "blocked a"));
  const blockedB = await storage.enqueueIntent(queueIntent("ref-b", "blocked b"));
  await storage.enqueueIntent(queueIntent("ref-a", "still submitting"));
  await storage.markUnknown(blockedA.clientMutationId, "blockedUnknown");
  await storage.markUnknown(blockedB.clientMutationId, "blockedUnknown");

  await act(async () => {
    expect(await refreshPendingTurnsProjection()).toBe(true);
  });

  const refA = renderHook(() => useBlockedMutationEntries("ref-a"));
  const refB = renderHook(() => useBlockedMutationEntries("ref-b"));
  expect(refA.result.current).toEqual([
    expect.objectContaining({
      clientMutationId: blockedA.clientMutationId,
      state: "blockedUnknown",
      targetRef: "ref-a",
    }),
  ]);
  expect(refB.result.current).toEqual([
    expect.objectContaining({
      clientMutationId: blockedB.clientMutationId,
      state: "blockedUnknown",
      targetRef: "ref-b",
    }),
  ]);

  await storage.settleApplied(blockedA.clientMutationId);
  await act(async () => {
    expect(await refreshPendingTurnsProjection()).toBe(true);
  });

  expect(refA.result.current).toEqual([]);
  expect(refB.result.current.map((record) => record.clientMutationId)).toEqual([blockedB.clientMutationId]);
  storage.close();
});
