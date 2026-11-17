// @vitest-environment jsdom

// #4816: how many durable projection reads one queued-message press costs, and
// which of them the press itself needs. The issue's second claim is that the
// action's own refresh and the commit feed's notify-driven refresh both run for
// the same ref, so every Edit or Remove pays two full three-store reads.
//
// The count is taken at `listRecoveryDisplayedAt`, whose only production caller
// is `readMutationPersistence` itself (stores/threads.ts:2836). Every other read
// a press causes - the dispatcher's `nextDispatchable`, the pin refresh, the
// recovery scan behind the edit - goes elsewhere, so this counts projection
// reads and nothing else.

import type { Thread, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../../stores/connection";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { MutationOutboxIndexedDB } from "../../../../stores/mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../../stores/threads";
import {
  editQueuedPendingTurn,
  removeQueuedPendingTurn,
  resetPendingTurnsStoreForTests,
  usePendingMessages,
} from "./pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./testing/flushPendingTurnsProjection";

function thread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thread_a",
    sessionId: "session_a",
    preview: "",
    ephemeral: false,
    modelProvider: "",
    createdAt: 1,
    updatedAt: 1,
    status: { type: "active" },
    cwd: "",
    cliVersion: "",
    source: "evener",
    evener: {
      ref: "ref_a",
      activeTurnId: "turn_1",
      capabilities: {
        send: true,
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        changeVisionModel: true,
        queue: true,
        goal: true,
        sharedNotes: true,
        rename: true,
      },
      queue: { revision: 1 },
    },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
    ...overrides,
  };
}

async function connect(): Promise<FakeClient> {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (): ThreadReadResponse => ({ thread: thread() }));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread("ref_a");
  // Offline on purpose: a connected dispatcher attempts the queued row and
  // marks it `attempted`, and an attempted submitting row is no longer the
  // user's to edit (canEditQueuedMessage). The press measured here is the one a
  // reader makes on a queue that is waiting, so the row must keep that shape.
  fake.emitStateChange("reconnecting");
  return fake;
}

function countProjectionReads(storage: MutationOutboxIndexedDB): { stop: () => number } {
  let projectionReads = 0;
  const displayed = storage.listRecoveryDisplayedAt.bind(storage);
  const spy = vi.spyOn(storage, "listRecoveryDisplayedAt").mockImplementation(async (targetRef: string) => {
    projectionReads += 1;
    return displayed(targetRef);
  });
  return {
    stop: () => {
      spy.mockRestore();
      return projectionReads;
    },
  };
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  setMutationClientIdentityForTests(undefined);
  localStorage.clear();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

// Stages one real queued row the way the composer's own queue path does.
async function queued(storage: MutationOutboxIndexedDB, text: string) {
  return storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/queue",
    attachments: [],
    composerText: text,
    payload: { input: [{ type: "text", text }] },
    optimisticDisplay: { input: [{ type: "text", text }] },
  });
}

test("one Edit press issues one projection read of its ref, not two (#4816)", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const record = await queued(storage, "queued work");
  await connect();
  await flushPendingTurnsProjectionForTests();

  const count = countProjectionReads(storage);
  const edited = await editQueuedPendingTurn(record.clientMutationId, "ref_a");
  await flushPendingTurnsProjectionForTests();
  const projectionReads = count.stop();

  expect(edited).toBeDefined();
  expect(projectionReads).toBe(1);
});

test("one Remove press issues one projection read of its ref, not two (#4816)", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const record = await queued(storage, "queued work");
  await connect();
  await flushPendingTurnsProjectionForTests();

  const count = countProjectionReads(storage);
  const removed = await removeQueuedPendingTurn(record.clientMutationId, "ref_a");
  await flushPendingTurnsProjectionForTests();
  const projectionReads = count.stop();

  expect(removed).toBe(true);
  expect(projectionReads).toBe(1);
});

// The issue requires the action's awaited completion guarantee to survive: a
// press resolves only once the projection its own refresh published is the
// durable one. Now that the notify-driven refresh is suppressed for the press's
// own ref, the press's read is the ONLY thing that publishes it - so this
// assertion fails if that awaited refresh is dropped, and it fails with the
// projection still showing the row as queued. That is what makes the elision
// load-bearing rather than a silent swap.
test("a press publishes its own read before it resolves", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const record = await queued(storage, "queued work");
  await connect();
  const pending = renderHook(() => usePendingMessages("ref_a"));
  await flushPendingTurnsProjectionForTests();
  const before = pending.result.current.find((entry) => entry.id === record.clientMutationId);
  expect(before?.recoveryKind).toBeUndefined();

  // The act() scope is React's, not the projection's: it carries the hook
  // update a press causes. No flush runs after the await, so whatever the
  // projection shows here is only what the press itself published before
  // resolving.
  let resolved: Awaited<ReturnType<typeof editQueuedPendingTurn>>;
  await act(async () => {
    resolved = await editQueuedPendingTurn(record.clientMutationId, "ref_a");
  });
  expect(resolved).toBeDefined();
  const after = pending.result.current.find((entry) => entry.id === record.clientMutationId);
  expect(after?.recoveryKind).toBe("editing");
});

// The invariant #4816 must not break: a message that was queued and
// acknowledged is delivered exactly once, including when the process dies
// between the acknowledgment and the delivery. Here the death is real for this
// layer - the mutation runtime is torn down and its storage closed, then a
// fresh runtime opens the same IndexedDB - and the assertion is that the row
// the press moved into recovery is still there exactly once, and not still
// queued for delivery.
test("a press that lands before the process dies leaves exactly one recoverable row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const record = await queued(storage, "queued work");
  await connect();
  await flushPendingTurnsProjectionForTests();

  const edited = await editQueuedPendingTurn(record.clientMutationId, "ref_a");
  expect(edited).toBeDefined();
  await flushPendingTurnsProjectionForTests();

  // Process death: runtime torn down, storage closed, projection reset. Nothing
  // survives in memory to answer the reload from.
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();

  // Reload: a fresh runtime over the same durable IndexedDB.
  const reopened = new MutationOutboxIndexedDB();
  setMutationStorageForTests(reopened);
  await connect();
  await flushPendingTurnsProjectionForTests();

  const recovered = (await reopened.listRecovery("ref_a")).filter(
    (candidate) => candidate.clientMutationId === record.clientMutationId,
  );
  expect(recovered).toHaveLength(1);
  expect(recovered[0]).toMatchObject({ recoveryKind: "editing", composerText: "queued work" });
  // It left the outbox, so no later dispatch can deliver it a second time.
  expect(await reopened.listOutbox("ref_a")).toEqual([]);
  reopened.close();
});
