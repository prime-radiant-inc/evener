import { deferred } from "@evener/appwire-client/testing/deferred";
import { appliedReceipt } from "../mutationReceiptTestUtils";
import { connectFakeClient } from "../testing/connectionTestUtils";
import { ALL_CAPABILITIES as CAPABILITIES, PAST_THREAD_CAPABILITIES, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import {
  type AnyNotification,
  APPWIRE_PROTOCOL_VERSION,
  ErrorTranscriptHistoryFailed,
  hydrateThread,
  type ModelControlSnapshot,
  modelControlDisplay,
  type NotesHumanSetResponse,
  RequestTimeoutError,
  type ThreadModelSetResponse,
  type ThreadReadResponse,
  WireError,
} from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { clearDraft, readComposerDraft, writeDraft } from "../panes/session/composer/draft";

import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { resetConnectionStoreForTests } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { editHumanNote, syncHumanNote, useHumanNoteDraft } from "./humanNoteDrafts";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { deleteMutationDatabase } from "./mutationOutboxTestUtils";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  emitAtResponseCut,
  markResponseCut,
  runScheduledHydrationRetry,
  type ScheduledHydrationRetry,
  scheduledHydrationRetries,
} from "./threadHydrationTestUtils";
import {
  consumedQueueEntriesFor,
  installHydrationRetrySchedulerForTests,
  readMutationPersistence,
  resetThreadsStoreForTests,
  retryBlockedMutation,
  setMutationStorageForTests,
  settleDisplayCopy,
  threadsStore,
} from "./threads";
import { ConflictError } from "./threadsErrors";
import {
  clearResponse,
  connectMutationClient,
  ensureActiveMutationTarget,
  flushIndexedDBUntil,
  flushUntil,
  mutationReceipt,
  nextHandledRequest,
  readResponse,
  settleCallerContinuations,
} from "./threadsSessionActionsTestUtils";

let restoreHydrationRetryScheduler: (() => void) | null = null;

let cache: SessionEntryCache;

function modelControlSnapshot(
  revision: number,
  options: {
    instanceId?: string;
    bootGeneration?: string;
    activeProvider?: string;
    activeModel?: string;
    desiredProvider?: string;
    desiredModel?: string;
    lifecycle?: "pending" | "applied" | "rejected";
    clientMutationId?: string;
  } = {},
): ModelControlSnapshot {
  const desired =
    options.desiredProvider && options.desiredModel
      ? {
          clientMutationId: options.clientMutationId ?? `fixture-mutation-${revision}`,
          modelProvider: options.desiredProvider,
          model: options.desiredModel,
          lifecycle: options.lifecycle ?? "pending",
        }
      : undefined;
  return {
    authority: {
      instanceId: options.instanceId ?? "thr_ref_a",
      bootGeneration: options.bootGeneration ?? "1",
    },
    revision,
    active: {
      modelProvider: options.activeProvider ?? "anthropic",
      model: options.activeModel ?? "claude-sonnet-4-5",
      reasoningEffortLevels: ["low", "high"],
      supportsReasoning: true,
    },
    ...(desired ? { desired } : {}),
  };
}

function capableModelRead(ref: string, snapshot: ModelControlSnapshot): ThreadReadResponse {
  return readResponse(ref, {
    modelProvider: snapshot.active.modelProvider,
    evener: {
      ref,
      capabilities: { ...CAPABILITIES, modelControlVersion: 1 },
      modelControl: snapshot,
      queue: { revision: 0 },
    },
  });
}

// The read a RESUMED daemon answers with. readResponse's own generation is
// "1" (the daemon the pane read before), so a fixture that means "a new boot"
// names the snapshot's generation as the response's too - exactly the pair the
// reducer compares when it accepts a read's authority.
function resumedModelRead(ref: string, snapshot: ModelControlSnapshot): ThreadReadResponse {
  return { ...capableModelRead(ref, snapshot), bootGeneration: snapshot.authority.bootGeneration };
}

function acceptedModelResponse(snapshot: ModelControlSnapshot) {
  return {
    disposition: "accepted" as const,
    modelControl: snapshot,
  };
}

beforeEach(async () => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetWorkspaceStoreForTests();
  resetSubagentModuleStoreForTests();
  scheduledHydrationRetries.length = 0;
  restoreHydrationRetryScheduler = installHydrationRetrySchedulerForTests((attempt, retry) => {
    const scheduled: ScheduledHydrationRetry = { attempt, retry, cancelled: false };
    scheduledHydrationRetries.push(scheduled);
    return () => {
      scheduled.cancelled = true;
    };
  });
  // The original mutation-database reset runs only on this imported fake IDB.
  expect(indexedDB).toBeInstanceOf(IDBFactory);
  await deleteMutationDatabase();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `session-actions-entry-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});

afterEach(() => {
  cleanup();
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});

describe("Manager start-time projection", () => {
  const ref = "local:historical";

  test.each([true, false, undefined])(
    "Manager %s refresh retains identity, loaded pages and draft through reconnect",
    async (manager) => {
      const fake = connectFakeClient();
      let name = "name-before";
      const turn = (entry: number) => ({
        id: `turn_${entry}`,
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: `item_${entry}`,
            transcriptKey: `entry-${entry}/item-0`,
            turnId: `turn_${entry}`,
            type: "agentMessage",
            text: `sentinel-${entry}`,
            version: entry,
            position: { entry, item: 0 },
          },
        ],
      });
      fake.on("thread/read", () => ({
        ...readResponse(ref, {
          name,
          turns: [turn(30)],
          evener: {
            ref,
            capabilities: CAPABILITIES,
            queue: {},
            manager,
          },
        }),
        snapshot: { incarnation: "inc-1", length: 31 },
        olderCursor: "page-2",
      }));
      fake.on("thread/turns/list", ({ cursor }) => ({
        data: [turn(cursor === "page-2" ? 20 : 10)],
        nextCursor: cursor === "page-2" ? "page-3" : undefined,
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 31 },
      }));
      await threadsStore.getState().ensureThread(ref);
      await threadsStore.getState().loadOlderTurns(ref);
      await threadsStore.getState().loadOlderTurns(ref);
      writeDraft(ref, "unsent-sentinel");
      const retained = () => {
        expect(threadsStore.getState().threads.get(ref)).toMatchObject({
          ref,
          instanceId: `thr_${ref}`,
          manager: manager === true,
        });
        expect(
          threadsStore
            .getState()
            .threads.get(ref)
            ?.turns.flatMap((item) => item.items)
            .map((item) => item.id),
        ).toEqual(["item_10", "item_20", "item_30"]);
        expect(
          threadsStore
            .getState()
            .threads.get(ref)
            ?.turns.flatMap((item) => item.items)
            .find((item) => item.id === "item_10")?.text,
        ).toBe("sentinel-10");
        expect(readComposerDraft(ref).text).toBe("unsent-sentinel");
      };
      retained();
      name = "name-after-resync";
      fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
      await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.name).toBe(name));
      retained();
      fake.emitStateChange("reconnecting");
      name = "name-after-reconnect";
      fake.emitReady();
      await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.name).toBe(name));
      // Reconnect deliberately starts a fresh transcript baseline. An epochless
      // hint does not, and must retain every loaded page.
      expect(readComposerDraft(ref).text).toBe("unsent-sentinel");
      expect(
        threadsStore
          .getState()
          .threads.get(ref)
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.id),
      ).toEqual(["item_30"]);
      await threadsStore.getState().loadOlderTurns(ref);
      await threadsStore.getState().loadOlderTurns(ref);
      retained();
      clearDraft(ref);
    },
  );

  test("Manager subscriber hint during an in-flight read gets the latest session projection", async () => {
    const fake = connectFakeClient();
    const reading = deferred<void>();
    const held = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => {
      reading.resolve();
      return held.promise;
    });
    const opening = threadsStore.getState().ensureThread(ref);
    await reading.promise;
    fake.on("thread/read", () =>
      readResponse(ref, {
        name: "latest-session-name",
        evener: {
          ref,
          capabilities: CAPABILITIES,
          queue: {},
          manager: true,
        },
      }),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
    held.resolve(
      readResponse(ref, {
        evener: { ref, capabilities: CAPABILITIES, queue: {}, manager: true },
      }),
    );
    await opening;
    await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.name).toBe("latest-session-name"));
    expect(threadsStore.getState().threads.get(ref)?.manager).toBe(true);
    expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThanOrEqual(2);
  });

  test("Manager start flag survives a subsequent repository publication", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse(ref, { evener: { ref, capabilities: CAPABILITIES, queue: {}, manager: true } }),
    );
    await threadsStore.getState().ensureThread(ref);
    expect(threadsStore.getState().threads.get(ref)?.manager).toBe(true);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { ref, threadId: `thr_${ref}`, status: { type: "active" } },
    });
    await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("active"));
    expect(threadsStore.getState().threads.get(ref)).toMatchObject({ ref, manager: true });
  });
});

test("an ignored history frame keeps the accepted copy until a visible item settles it", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m1" });
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", clientMutationId: "m1", input: [{ type: "text", text: "hello" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "hello" }] },
  });
  await storage.markAttempted(outbox.clientMutationId);
  expect(await storage.settleReceipt(outbox.clientMutationId, "pending")).toBe(true);
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  const settleSpy = vi.spyOn(storage, "settleApplied");

  const history = (epoch: number, bootGeneration = "1") => ({
    method: "history/updated" as const,
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      bootGeneration,
      epoch,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_m1", status: "inProgress", itemsView: "full", version: 1 }],
      items: [
        {
          id: "item_m1",
          turnId: "turn_m1",
          type: "userMessage",
          text: "hello",
          clientMutationId: "m1",
          version: 1,
        },
      ],
    },
  });

  // An older epoch is ignored by the history state machine, so the item never
  // enters the model. Settlement must not delete the only pending copy.
  fake.emitNotification(history(0));
  await flushIndexedDBUntil(() => false);
  expect(settleSpy).not.toHaveBeenCalled();
  expect(await storage.getOptimistic("m1")).toBeDefined();

  // An older boot generation is ignored by the same state machine even at an
  // equal epoch: its snapshot is from a daemon this history already replaced,
  // so the frame proves nothing about what the current transcript holds.
  fake.emitNotification(history(1, "0"));
  await flushIndexedDBUntil(() => false);
  expect(settleSpy).not.toHaveBeenCalled();
  expect(await storage.getOptimistic("m1")).toBeDefined();

  // The same item on the current epoch becomes visible, and only then settles.
  fake.emitNotification(history(1));
  await flushIndexedDBUntil(async () => (await storage.getOptimistic("m1")) === undefined);
  expect(await storage.getOptimistic("m1")).toBeUndefined();
  storage.close();
});

test("a failed history read keeps the accepted copy when a frame names its identity", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m1" });
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", clientMutationId: "m1", input: [{ type: "text", text: "hello" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "hello" }] },
  });
  await storage.markAttempted(outbox.clientMutationId);
  expect(await storage.settleReceipt(outbox.clientMutationId, "pending")).toBe(true);
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("thread/read", () => {
    throw new WireError("thread history failed", -32603, {
      evenerErrorInfo: ErrorTranscriptHistoryFailed,
    });
  });
  await expect(threadsStore.getState().refreshThread("ref_a")).rejects.toThrow();
  await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.history?.failed).toBeDefined());
  const settleSpy = vi.spyOn(storage, "settleApplied");

  fake.emitNotification({
    method: "history/updated",
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_m1", status: "inProgress", itemsView: "full", version: 1 }],
      items: [
        {
          id: "item_m1",
          turnId: "turn_m1",
          type: "userMessage",
          text: "hello",
          clientMutationId: "m1",
          version: 1,
        },
      ],
    },
  });
  await flushIndexedDBUntil(() => false);
  expect(settleSpy).not.toHaveBeenCalled();
  expect(await storage.getOptimistic("m1")).toBeDefined();
  storage.close();
});

test("an authoritative hydration keeps the confirmed queue lineage on the retained copy", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m1" });
  await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", clientMutationId: "m1", input: [{ type: "text", text: "queued" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "queued" }] },
  });
  await storage.markAttempted("m1");
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 1 },
        pendingMutations: [
          {
            clientMutationId: "m1",
            method: "turn/queue",
            executionState: "accepted",
            projectionState: "pending",
            queueEntryIds: ["q1"],
          },
        ],
      },
    }),
  );
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await flushIndexedDBUntil(async () => (await storage.getOptimistic("m1"))?.queueEntryIds?.[0] === "q1");

  expect((await storage.getOptimistic("m1"))?.queueEntryIds).toEqual(["q1"]);
  storage.close();
});

test("a queue frame during a read keeps the published snapshot stale", async () => {
  const fake = connectMutationClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 1 },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");
  expect(threadsStore.getState().mutationSnapshotRefs.has("ref_a")).toBe(true);

  const read = deferred<ThreadReadResponse>();
  const started = deferred<void>();
  fake.on("thread/read", () => {
    started.resolve();
    return read.promise;
  });
  const reading = threadsStore.getState().refreshThread("ref_a");
  await started.promise;
  fake.emitNotification({
    method: "thread/queueChanged",
    params: { threadId: "thr_ref_a", ref: "ref_a", queue: { revision: 2 } },
  });
  read.resolve(
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 2 },
      },
    }),
  );
  await reading;

  expect(threadsStore.getState().mutationSnapshotRefs.has("ref_a")).toBe(false);
});

test("a queue frame keeps the content of a consumed row this client never owned", async () => {
  const fake = connectMutationClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 1, depth: 1, ids: ["q1"], clientMutationIds: ["m9"], texts: ["queued elsewhere"] },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");

  const read = deferred<ThreadReadResponse>();
  const started = deferred<void>();
  fake.on("thread/read", () => {
    started.resolve();
    return read.promise;
  });
  const reading = threadsStore.getState().refreshThread("ref_a");
  await started.promise;
  fake.emitNotification({
    method: "thread/queueChanged",
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      queue: { revision: 2, depth: 0, ids: [] },
      consumedClientMutationIds: ["m9"],
    },
  });
  read.resolve(
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 2, depth: 0, ids: [] },
      },
    }),
  );
  await reading;

  expect(consumedQueueEntriesFor("ref_a")).toEqual([
    expect.objectContaining({ entryId: "q1", text: "queued elsewhere" }),
  ]);
  expect(consumedQueueEntriesFor("ref_a")).toBe(consumedQueueEntriesFor("ref_a"));
});

test("a queue frame keeps the content of a row a promotion removed without naming it", async () => {
  // An older daemon omits consumedClientMutationIds on promotion. The message
  // is no longer queued and nothing records it here, so the disappearance is
  // the only evidence it left the queue. Without carrying it the row vanishes
  // for the rest of the delivery wait.
  const fake = connectMutationClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 1, depth: 1, ids: ["q1"], clientMutationIds: ["m9"], texts: ["queued elsewhere"] },
      },
    }),
  );
  await threadsStore.getState().ensureThread("ref_a");

  fake.emitNotification({
    method: "thread/queueChanged",
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      queue: { revision: 2, depth: 0, ids: [] },
    },
  });

  expect(consumedQueueEntriesFor("ref_a")).toEqual([
    expect.objectContaining({ entryId: "q1", text: "queued elsewhere" }),
  ]);
});

// A cancellation from an older daemon names no consumed ids at all, so no
// consumed frame ever carried the entry. Its only evidence is the provenance
// state's retired input list, which must settle the retained copy on its own
// or the canceled message stays an actionless Delivering row until reconnect.
test("a retired input settles a retained copy no consumed frame named", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m9" });
  await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", clientMutationId: "m9", input: [{ type: "text", text: "queued elsewhere" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "queued elsewhere" }] },
  });
  await storage.markAttempted("m9");
  expect(await storage.settleReceipt("m9", "pending", ["q1"])).toBe(true);
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 1 },
        pendingMutations: [],
      },
    }),
  );
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  // The daemon retires the input with no queue frame: an older daemon's
  // cancellation looks exactly like this.
  fake.emitNotification({
    method: "thread/conversation-provenance/changed",
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      provenance: {
        version: 1,
        authoritative: true,
        revision: 2,
        instanceId: "inc",
        admittedInputs: [],
        retiredInputs: ["client:m9"],
        frontiers: [],
      },
    },
  } as never);
  await flushIndexedDBUntil(async () => (await storage.getOptimistic("m9")) === undefined);

  expect(await storage.getOptimistic("m9")).toBeUndefined();
});

test("an authoritative hydration retires an absent retained queue source", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m1" });
  await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", clientMutationId: "m1", input: [{ type: "text", text: "queued" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "queued" }] },
  });
  await storage.markAttempted("m1");
  expect(await storage.settleReceipt("m1", "pending", ["q1"])).toBe(true);
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        mutationStateAuthoritative: true,
        queue: { revision: 2 },
        pendingMutations: [],
      },
      turns: [
        {
          id: "turn_replacement",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_replacement",
              turnId: "turn_replacement",
              type: "userMessage",
              text: "queued",
              clientMutationId: "replacement",
              version: 1,
            },
          ],
        },
      ],
    }),
  );
  fake.on("turn/queue", (params) => ({
    receipt: { ...mutationReceipt(params.clientMutationId), projectionState: "removed" },
  }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await flushIndexedDBUntil(async () => (await storage.getOptimistic("m1")) === undefined);

  expect(await storage.getOptimistic("m1")).toBeUndefined();
  storage.close();
});

test("queue evidence keeps the accepted display copy without settling it", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m1" });
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", clientMutationId: "m1", input: [{ type: "text", text: "hello" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "hello" }] },
  });
  await storage.markAttempted(outbox.clientMutationId);
  expect(await storage.settleReceipt(outbox.clientMutationId, "pending")).toBe(true);
  expect(await storage.getOptimistic(outbox.clientMutationId)).toBeDefined();
  const settleSpy = vi.spyOn(storage, "settleApplied");

  fake.emitNotification({
    method: "thread/queueChanged",
    params: {
      threadId: "thr_ref_a",
      ref: "ref_a",
      queue: {
        revision: 1,
        depth: 1,
        ids: ["q1"],
        clientMutationIds: ["m1"],
        texts: ["hello"],
        preview: ["hello"],
      },
    },
  });
  await flushIndexedDBUntil(() => false);
  expect(settleSpy).not.toHaveBeenCalled();
  expect(await storage.getOptimistic("m1")).toBeDefined();
  storage.close();
});

test("settleDisplayCopy retires a retained queue display copy", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m1" });
  setMutationStorageForTests(storage);
  connectMutationClient();
  const outbox = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", clientMutationId: "m1", input: [{ type: "text", text: "hello" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "hello" }] },
  });
  await storage.markAttempted(outbox.clientMutationId);
  expect(await storage.settleReceipt(outbox.clientMutationId, "pending")).toBe(true);
  expect(await storage.getOptimistic(outbox.clientMutationId)).toBeDefined();
  await settleDisplayCopy(outbox.clientMutationId, "ref_a");
  expect(await storage.getOptimistic(outbox.clientMutationId)).toBeUndefined();
  storage.close();
});

describe("useThreadsStore session actions (setModel/setReasoningEffort/setGoal/rename/compact/shutdown/clearThread/forkFromTurn)", () => {
  test("setModel refuses locally until a version-1 read establishes authority", async () => {
    const fake = connectFakeClient();

    await expect(threadsStore.getState().setModel("ref_a", "anthropic", "claude-opus-4-1")).rejects.toBeInstanceOf(
      Error,
    );

    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
  });

  test.each([
    {
      shape: "a cold read that never carried authority",
      coldRead: (ref: string) =>
        readResponse(ref, {
          status: { type: "notLoaded" },
          evener: { ref, capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
        }),
      retireAuthority: (_fake: FakeClient) => {},
      staleBootGeneration: undefined,
    },
    {
      shape: "a cold status whose pane still holds the exited daemon's ready snapshot",
      // The pane was hydrated LIVE: this is the daemon that has since exited,
      // and its ready authority is the one the pick must NOT dispatch with.
      coldRead: (ref: string) => capableModelRead(ref, modelControlSnapshot(1, { bootGeneration: "1" })),
      // The exit itself. A status frame never touches modelControlState, so the
      // model keeps `ready` with the retired boot generation.
      retireAuthority: (fake: FakeClient) => {
        act(() => {
          fake.emitNotification({
            method: "thread/status/changed",
            params: {
              threadId: "thr_ref_a",
              ref: "ref_a",
              status: { type: "notLoaded" },
              capabilities: PAST_THREAD_CAPABILITIES,
            },
          });
        });
      },
      staleBootGeneration: "1",
    },
  ])(
    "a pick on $shape resumes once and sets the model once on the resumed authority",
    async ({ coldRead, retireAuthority, staleBootGeneration }) => {
      const fake = connectFakeClient();
      const ref = "ref_a";
      fake.on("thread/read", () => coldRead(ref));
      await threadsStore.getState().ensureThread(ref);
      retireAuthority(fake);
      expect(threadsStore.getState().threads.get(ref)?.status.type).toBe("notLoaded");
      expect(threadsStore.getState().threads.get(ref)?.modelControlState.kind).toBe(
        staleBootGeneration === undefined ? "unsupported" : "ready",
      );

      // The resumed daemon is a NEW boot generation, and the read that follows
      // the resume is the only thing that establishes its authority.
      const resumedSnapshot = modelControlSnapshot(1, { bootGeneration: "2" });
      fake.on("thread/resume", () => ({ thread: capableModelRead(ref, resumedSnapshot).thread }));
      fake.on("thread/read", () => resumedModelRead(ref, resumedSnapshot));
      const accepted = modelControlSnapshot(2, {
        bootGeneration: "2",
        desiredProvider: "openai",
        desiredModel: "gpt-5.5",
        clientMutationId: "11111111-1111-4111-8111-111111111111",
      });
      vi.spyOn(crypto, "randomUUID").mockReturnValue("11111111-1111-4111-8111-111111111111");
      // The resumed daemon, like the real one (server/appwire_thread_settings.go's
      // expectedInstanceID/expectedBootGeneration fence), refuses a request bound
      // to the exited daemon's generation.
      let setAttempts = 0;
      fake.on("thread/model/set", (params) => {
        setAttempts += 1;
        if (params.expectedBootGeneration !== resumedSnapshot.authority.bootGeneration) {
          throw new WireError("authority changed", -32602, {
            evenerErrorInfo: "authorityStale",
            mutationOutcome: "notAccepted",
            retryDisposition: "none",
          });
        }
        return acceptedModelResponse(accepted);
      });

      await threadsStore.getState().setModel(ref, "openai", "gpt-5.5");

      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
      expect(setAttempts).toBe(1);
      expect(fake.calls.filter((call) => call.method === "thread/model/set")).toEqual([
        {
          method: "thread/model/set",
          params: {
            ref,
            modelProvider: "openai",
            model: "gpt-5.5",
            clientMutationId: "11111111-1111-4111-8111-111111111111",
            expectedInstanceId: "thr_ref_a",
            expectedBootGeneration: "2",
          },
        },
      ]);
      expect(threadsStore.getState().threads.get(ref)?.modelControlState).toEqual({
        kind: "ready",
        snapshot: accepted,
      });
      expect(modelControlDisplay(threadsStore.getState().threads.get(ref)!)).toEqual({
        modelProvider: "openai",
        model: "gpt-5.5",
      });
    },
  );

  test.each([
    // A connection below v8 refuses thread/model/set before a frame is built,
    // so resuming the daemon for that pick would be a real side effect the
    // mutation can never justify. The refusal is the wire's own pre-send one.
    { protocolVersion: "evener-appwire-v7", resumed: 0 },
    { protocolVersion: APPWIRE_PROTOCOL_VERSION, resumed: 1 },
  ])(
    "a cold pick on a $protocolVersion connection whose resume read establishes no authority rejects without a model request",
    async ({ protocolVersion, resumed }) => {
      const fake = connectFakeClient();
      const handshake = fake.initializeResult;
      if (handshake === null) throw new Error("fixture: no handshake to re-version");
      fake.scriptHandshake({ ...handshake, protocolVersion });
      const ref = "ref_a";
      const cold = (target: string) =>
        readResponse(target, {
          status: { type: "notLoaded" },
          evener: { ref: target, capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
        });
      fake.on("thread/read", (params) => cold(params.ref ?? ref));
      await threadsStore.getState().ensureThread(ref);
      fake.on("thread/resume", () => ({ thread: cold(ref).thread }));

      const pick = threadsStore.getState().setModel(ref, "openai", "gpt-5.5");
      if (protocolVersion === APPWIRE_PROTOCOL_VERSION) {
        await expect(pick).rejects.toBeInstanceOf(Error);
      } else {
        await expect(pick).rejects.toMatchObject({ evenerErrorInfo: "olderHubProtocol" });
      }

      expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(resumed);
    },
  );

  test.each([
    {
      shape: "a watched session with no pane parked on it",
      claim: async () => {
        await threadsStore.getState().watchThread("ref_a");
        return () => {
          threadsStore.getState().releaseWatchedThread("ref_a");
        };
      },
      resolve: "ref_a",
    },
    {
      shape: "a resume that resolves an identity no pane has followed",
      // The pane stays on the original ref: the identity the resume resolves is
      // the one with no pane, no watcher and no pin - the post-resume refresh
      // would skip it, so the pick would be refused after the resume ran.
      claim: async () => {
        await threadsStore.getState().ensureThread("ref_a");
        return () => {
          threadsStore.getState().releaseThread("ref_a");
        };
      },
      resolve: "ref_b",
    },
    {
      shape: "a watched session whose resume resolves an identity no pane has followed",
      // Neither the original ref nor the resolved one has a pane: the driver
      // itself must read the resolved identity, because no pane ever will.
      claim: async () => {
        await threadsStore.getState().watchThread("ref_a");
        return () => {
          threadsStore.getState().releaseWatchedThread("ref_a");
        };
      },
      resolve: "ref_b",
    },
  ])("a cold pick on $shape dispatches once on the ref the resume resolved", async ({ claim, resolve }) => {
    const fake = connectFakeClient();
    const ref = "ref_a";
    // The cold read: past-thread capabilities, no authority - what a watched
    // lifecycle (or an unparked ref) holds for a session whose daemon exited.
    const cold = () =>
      readResponse(ref, {
        status: { type: "notLoaded" },
        evener: { ref, capabilities: PAST_THREAD_CAPABILITIES, queue: { revision: 0 } },
      });
    fake.on("thread/read", () => cold());
    const release = await claim();
    try {
      expect(threadsStore.getState().threads.get(ref)?.modelControlState.kind ?? "uninitialized").not.toBe("ready");

      // The resumed daemon's read is the one that establishes authority, and
      // no pane will ever issue it: the driver itself must hold a read claim
      // through the resume so this read lands.
      const resolvedRef = resolve;
      const resolvedSnapshot = modelControlSnapshot(1, {
        bootGeneration: "2",
        instanceId: `thr_${resolvedRef}`,
      });
      fake.on("thread/read", (params) =>
        params.ref === resolvedRef
          ? resumedModelRead(resolvedRef, resolvedSnapshot)
          : readResponse(String(params.ref), {
              status: { type: "notLoaded" },
              evener: {
                ref: String(params.ref),
                capabilities: PAST_THREAD_CAPABILITIES,
                queue: { revision: 0 },
              },
            }),
      );
      fake.on("thread/resume", () => ({ thread: capableModelRead(resolvedRef, resolvedSnapshot).thread }));
      const accepted = modelControlSnapshot(2, {
        bootGeneration: "2",
        instanceId: `thr_${resolvedRef}`,
        desiredProvider: "openai",
        desiredModel: "gpt-5.5",
        clientMutationId: "11111111-1111-4111-8111-111111111111",
      });
      vi.spyOn(crypto, "randomUUID").mockReturnValue("11111111-1111-4111-8111-111111111111");
      fake.on("thread/model/set", () => acceptedModelResponse(accepted));

      await threadsStore.getState().setModel(ref, "openai", "gpt-5.5");

      expect(fake.calls.filter((call) => call.method === "thread/resume")).toHaveLength(1);
      expect(fake.calls.filter((call) => call.method === "thread/model/set")).toEqual([
        {
          method: "thread/model/set",
          params: {
            ref: resolvedRef,
            modelProvider: "openai",
            model: "gpt-5.5",
            clientMutationId: "11111111-1111-4111-8111-111111111111",
            expectedInstanceId: `thr_${resolvedRef}`,
            expectedBootGeneration: "2",
          },
        },
      ]);
    } finally {
      await release();
    }
  });

  test("a capable setModel sends captured authority and shows only the accepted response", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(1);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");
    vi.spyOn(crypto, "randomUUID").mockReturnValue("11111111-1111-4111-8111-111111111111");

    const response = deferred<{
      disposition: string;
      modelControl: ModelControlSnapshot;
    }>();
    const request = nextHandledRequest(fake, "thread/model/set", () => response.promise);
    const setting = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    expect(await request).toEqual({
      ref: "ref_a",
      modelProvider: "openai",
      model: "gpt-5.5",
      clientMutationId: "11111111-1111-4111-8111-111111111111",
      expectedInstanceId: "thr_ref_a",
      expectedBootGeneration: "1",
    });
    expect(modelControlDisplay(threadsStore.getState().threads.get("ref_a")!)).toEqual({
      modelProvider: "anthropic",
      model: "claude-sonnet-4-5",
    });

    const accepted = modelControlSnapshot(2, {
      desiredProvider: "openai",
      desiredModel: "gpt-5.5",
      clientMutationId: "11111111-1111-4111-8111-111111111111",
    });
    response.resolve(acceptedModelResponse(accepted));
    await setting;

    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: accepted,
    });
    expect(modelControlDisplay(threadsStore.getState().threads.get("ref_a")!)).toEqual({
      modelProvider: "openai",
      model: "gpt-5.5",
    });
  });

  test("a model change waits for the current reconnect read and sends exactly once after it becomes ready", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const read = deferred<ThreadReadResponse>();
    const readStarted = nextHandledRequest(fake, "thread/read", () => read.promise);
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await readStarted;

    const accepted = modelControlSnapshot(2, {
      desiredProvider: "openai",
      desiredModel: "gpt-5.5",
      clientMutationId: "11111111-1111-4111-8111-111111111111",
    });
    vi.spyOn(crypto, "randomUUID").mockReturnValue("11111111-1111-4111-8111-111111111111");
    fake.on("thread/model/set", () => acceptedModelResponse(accepted));

    const setting = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    await Promise.resolve();
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("uninitialized");

    read.resolve(capableModelRead("ref_a", modelControlSnapshot(1)));
    await setting;

    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(1);
    expect(modelControlDisplay(threadsStore.getState().threads.get("ref_a")!)).toEqual({
      modelProvider: "openai",
      model: "gpt-5.5",
    });
  });

  test.each([
    {
      outcome: "a daemon rejection",
      settle(read: ReturnType<typeof deferred<ThreadReadResponse>>) {
        read.reject(new WireError("read refused", -32014));
      },
    },
    {
      outcome: "a transport timeout",
      settle(read: ReturnType<typeof deferred<ThreadReadResponse>>) {
        read.reject(new RequestTimeoutError("read timed out"));
      },
    },
    {
      outcome: "a malformed version-1 response",
      settle(read: ReturnType<typeof deferred<ThreadReadResponse>>) {
        const malformed = capableModelRead("ref_a", modelControlSnapshot(1));
        if (malformed.thread.evener) delete malformed.thread.evener.modelControl;
        read.resolve(malformed);
      },
    },
  ])("$outcome settles a waiting model change without dispatch", async ({ settle }) => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const read = deferred<ThreadReadResponse>();
    const readStarted = nextHandledRequest(fake, "thread/read", () => read.promise);
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await readStarted;

    const setting = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    const rejected = expect(setting).rejects.toThrow();
    settle(read);

    await rejected;
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
  });

  test("retiring an establishing read settles its waiting model change without dispatch", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const read = deferred<ThreadReadResponse>();
    const readStarted = nextHandledRequest(fake, "thread/read", () => read.promise);
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await readStarted;

    const setting = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    const rejected = expect(setting).rejects.toThrow();
    fake.emitStateChange("reconnecting");

    await rejected;
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
    read.resolve(capableModelRead("ref_a", modelControlSnapshot(1)));
  });

  test("a failed replacement read settles waiting model changes without another dispatch", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const recovery = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => recovery.promise);
    fake.on("thread/model/set", () => {
      throw new WireError("authority changed", -32602, {
        evenerErrorInfo: "authorityStale",
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    });
    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toBeInstanceOf(WireError);

    const waiting = threadsStore.getState().setModel("ref_a", "google", "gemini-3-pro");
    const rejected = expect(waiting).rejects.toBeInstanceOf(RequestTimeoutError);
    recovery.reject(new RequestTimeoutError("replacement read timed out"));

    await rejected;
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(1);
  });

  test.each([
    ["empty", {}],
    ["version-only", { modelControlVersion: 1 }],
    ["disposition-only", { disposition: "accepted" }],
    ["snapshot-only", { modelControl: modelControlSnapshot(2) }],
  ])("an %s dispatched response enters one-read recovery and is never replayed", async (_shape, response) => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const recovery = deferred<ThreadReadResponse>();
    let recoveryReads = 0;
    fake.on("thread/read", () => {
      recoveryReads += 1;
      return recovery.promise;
    });
    fake.on("thread/model/set", () => response as never);

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toMatchObject({
      evenerErrorInfo: "mutationOutcomeUnknown",
    });
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");
    expect(recoveryReads).toBe(1);
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(1);

    recovery.resolve(capableModelRead("ref_a", modelControlSnapshot(2)));
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(1);
  });

  test.each([
    ["client transport loss", new Error("connection lost after dispatch")],
    [
      "forwarded outcome uncertainty",
      new WireError("daemon response lost", -32603, {
        evenerErrorInfo: "mutationOutcomeUnknown",
        mutationOutcome: "unknown",
        retryDisposition: "blocked",
      }),
    ],
  ])("%s enters one-read recovery and is never replayed", async (_failure, error) => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const recovery = deferred<ThreadReadResponse>();
    let recoveryReads = 0;
    fake.on("thread/read", () => {
      recoveryReads += 1;
      return recovery.promise;
    });
    fake.on("thread/model/set", () => {
      throw error;
    });

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toThrow();
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");
    expect(recoveryReads).toBe(1);
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(1);

    recovery.resolve(capableModelRead("ref_a", modelControlSnapshot(2)));
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");
    fake.on("thread/model/set", () =>
      acceptedModelResponse(
        modelControlSnapshot(3, {
          desiredProvider: "google",
          desiredModel: "gemini-3-pro",
          lifecycle: "applied",
        }),
      ),
    );
    await threadsStore.getState().setModel("ref_a", "google", "gemini-3-pro");
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toMatchObject([
      { params: { model: "gpt-5.5" } },
      { params: { model: "gemini-3-pro" } },
    ]);
  });

  test("a typed stale-authority refusal enters one-read recovery while preserving the direct refusal", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(1);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");

    const recovery = deferred<ThreadReadResponse>();
    let recoveryReads = 0;
    fake.on("thread/read", () => {
      recoveryReads += 1;
      return recovery.promise;
    });
    fake.on("thread/model/set", () => {
      throw new WireError("authority changed", -32602, {
        evenerErrorInfo: "authorityStale",
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    });

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toBeInstanceOf(WireError);
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");
    expect(recoveryReads).toBe(1);

    recovery.resolve(capableModelRead("ref_a", modelControlSnapshot(2)));
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");
  });

  test("a mismatched response authority starts exactly one held replacement read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const recovery = deferred<ThreadReadResponse>();
    let recoveryReads = 0;
    fake.on("thread/read", () => {
      recoveryReads += 1;
      return recovery.promise;
    });
    fake.on("thread/model/set", () =>
      acceptedModelResponse(
        modelControlSnapshot(2, {
          instanceId: "replacement-instance",
          bootGeneration: "2",
          desiredProvider: "openai",
          desiredModel: "gpt-5.5",
        }),
      ),
    );

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toMatchObject({
      evenerErrorInfo: "mutationOutcomeUnknown",
    });
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");
    expect(recoveryReads).toBe(1);

    recovery.resolve(capableModelRead("ref_a", modelControlSnapshot(2)));
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");
  });

  test("a model response accepted during a read remains the repository's canonical snapshot", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(1);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");

    const read = deferred<ThreadReadResponse>();
    const readStarted = nextHandledRequest(fake, "thread/read", () => read.promise);
    const refreshing = threadsStore.getState().refreshThread("ref_a");
    await readStarted;
    const accepted = modelControlSnapshot(2, {
      desiredProvider: "openai",
      desiredModel: "gpt-5.5",
      clientMutationId: "55555555-5555-4555-8555-555555555555",
    });
    fake.on("thread/model/set", () => ({
      disposition: "accepted",
      modelControl: accepted,
    }));

    await threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    fake.emitNotification({
      method: "thread/status/changed",
      params: { ref: "ref_a", threadId: "thr_ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: accepted,
    });

    read.resolve(capableModelRead("ref_a", initial));
    await refreshing;
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: accepted,
    });
  });

  // A recovery read can carry valid same-boot model control and still be
  // discarded on its transcript half: a window shorter than the one held, from
  // the same incarnation, is an identity-race loss and publishes no rows. The
  // store used to return on `readAccepted === false` before the repository
  // could reconcile the envelope, so retirement failed the pick parked on that
  // read and left the picker `recovering` with the transcript and mutation
  // state intact but no way for the user to change the model.
  test("a discarded recovery read still settles the waiting pick from its same-boot envelope", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(1);
    fake.on("thread/read", () => ({
      ...capableModelRead("ref_a", initial),
      snapshot: { incarnation: "inc-1", length: 5 },
    }));
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.history?.length).toBe(5);

    // The daemon reports a pick it cannot apply, so the store enters recovery
    // and arms one replacement read.
    const recovery = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => recovery.promise);
    fake.on("thread/model/set", () => {
      throw new WireError("authority changed", -32602, {
        evenerErrorInfo: "authorityStale",
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    });
    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toBeInstanceOf(WireError);
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");

    // That replacement answers for the SAME boot, and its window is behind the
    // one held, so the transcript half is discarded.
    recovery.resolve({
      ...capableModelRead("ref_a", modelControlSnapshot(2)),
      snapshot: { incarnation: "inc-1", length: 2 },
    });
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");

    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("ready");
    // The held transcript and its mutation state survive the discarded window.
    expect(threadsStore.getState().threads.get("ref_a")?.history?.length).toBe(5);
    // And the next pick dispatches under the authority that read established.
    fake.on("thread/model/set", () => acceptedModelResponse(modelControlSnapshot(3)));
    await threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(2);
  });

  test("a conflicting equal-revision model response starts recovery without invalidating history", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(2);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/model/set", () => ({
      disposition: "accepted",
      modelControl: modelControlSnapshot(2, { activeProvider: "google", activeModel: "gemini-3-pro" }),
    }));

    await threadsStore.getState().setModel("ref_a", "google", "gemini-3-pro");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.modelControlState.kind).toBe("recovering");
    expect(model?.history?.invalidatedAtGeneration).toBeUndefined();
  });

  test.each([
    ["an equal-revision conflict", { activeProvider: "google", activeModel: "gemini-3-pro" }],
    ["an authority change", { instanceId: "replacement-instance", bootGeneration: "2" }],
  ])("%s in a live notification starts one replacement read", async (_case, overrides) => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(2);
    const recoveryAuthority = "instanceId" in overrides ? overrides : {};
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");

    const recovery = deferred<ThreadReadResponse>();
    let recoveryReads = 0;
    fake.on("thread/read", () => {
      recoveryReads += 1;
      return recovery.promise;
    });
    const conflicting = modelControlSnapshot(2, overrides);

    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: conflicting },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");
    expect(threadsStore.getState().threads.get("ref_a")?.history?.invalidatedAtGeneration).toBeUndefined();
    await flushUntil(() => recoveryReads === 1);

    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: conflicting },
    });
    expect(recoveryReads).toBe(1);

    recovery.resolve(capableModelRead("ref_a", modelControlSnapshot(3, recoveryAuthority)));
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");
  });

  test("an equal-revision conflict buffered during the establishing read starts one replacement read", async () => {
    const fake = connectFakeClient();
    const establishing = deferred<ThreadReadResponse>();
    const recovery = deferred<ThreadReadResponse>();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      return reads === 1 ? establishing.promise : recovery.promise;
    });

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads === 1);
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: {
        ref: "ref_a",
        threadId: "thr_ref_a",
        modelControl: modelControlSnapshot(2, { activeProvider: "google", activeModel: "gemini-3-pro" }),
      },
    });
    establishing.resolve(capableModelRead("ref_a", modelControlSnapshot(2)));

    await ensuring;
    await flushUntil(() => reads === 2);
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");
    expect(threadsStore.getState().threads.get("ref_a")?.history?.invalidatedAtGeneration).toBeUndefined();

    const recovered = modelControlSnapshot(3, { activeProvider: "google", activeModel: "gemini-3-pro" });
    recovery.resolve(capableModelRead("ref_a", recovered));
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");

    expect(reads).toBe(2);
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: recovered,
    });
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
  });

  test("an equal-revision conflict delivered after the response cut starts one replacement read", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(2);
    // A conflicting frame that lands after the read's response was cut but
    // before its snapshot published is replayed at publish time, not at the
    // cut. Nothing else in that window schedules the read that resolves it.
    const cut = { reached: false };
    const establishing = deferred<ThreadReadResponse>();
    let recoveryReads = 0;
    fake.on("thread/read", () => {
      recoveryReads += 1;
      return recoveryReads === 1 ? establishing.promise : Promise.resolve(capableModelRead("ref_a", initial));
    });
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => recoveryReads === 1);
    establishing.resolve(markResponseCut(capableModelRead("ref_a", initial), cut));
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "thread/model/controlChanged",
        params: {
          ref: "ref_a",
          threadId: "thr_ref_a",
          modelControl: modelControlSnapshot(2, { activeProvider: "google", activeModel: "gemini-3-pro" }),
        },
      }),
    );
    await ensuring;

    await flushUntil(() => recoveryReads === 2);
    expect(recoveryReads, "the post-cut conflict started exactly one replacement read").toBe(2);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");
    expect(recoveryReads, "the settled recovery scheduled no further read").toBe(2);
  });

  test("a superseded pick rejects and keeps the winning snapshot", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(2);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");
    // Another client's pick landed first, so the daemon answers with the
    // winner's snapshot and the `superseded` disposition.
    const winner = modelControlSnapshot(3, { activeProvider: "google", activeModel: "gemini-3-pro" });
    fake.on("thread/model/set", () => ({
      disposition: "superseded",
      modelControl: winner,
    }));

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toThrow();
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: winner,
    });
  });

  test("a superseded pick still rejects after a refresh advances the read generation", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(2);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");

    // Hold the pick's reply open, then start a same-session refresh so the
    // read generation moves past the fence this request captured. The fence
    // then forbids this reply from publishing a snapshot - which is correct -
    // but the caller still must not be told its losing pick succeeded.
    const reply = deferred<ThreadModelSetResponse>();
    fake.on("thread/model/set", () => reply.promise);
    const pick = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    await flushUntil(() => fake.calls.some((call) => call.method === "thread/model/set"));
    // A same-session refresh, exactly as the reviewer described: the store's
    // ready handler re-reads every tracked ref, and that read advances the
    // generation the pick captured.
    const refreshed = modelControlSnapshot(3, { activeProvider: "google", activeModel: "gemini-3-pro" });
    fake.on("thread/read", () => capableModelRead("ref_a", refreshed));
    // A real reconnect is a transition into "ready", not a repeat of it: the
    // fake ignores emitReady when the client is already ready, so leave and
    // come back the way the connection does.
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");

    reply.resolve({ disposition: "superseded", modelControl: refreshed });

    await expect(pick).rejects.toBeInstanceOf(ConflictError);
    // The refresh's own read owns publication, and its snapshot stands. The
    // point is that the fence did not turn the refusal into a success.
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: refreshed,
    });
  });

  // A queued pick can pass admission and then fail deferred validation before
  // the daemon refreshes the reply's snapshot. The disposition then reads
  // accepted while this exact mutation's desired record has already settled as
  // rejected, and the palette toasted success for a switch that did not happen.
  // A still-pending record is queued and may yet apply, so it stays a success.
  test("a pick the reply records as rejected rejects instead of confirming", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/model/set", (params) => ({
      disposition: "accepted",
      modelControl: modelControlSnapshot(2, {
        desiredProvider: "openai",
        desiredModel: "gpt-5.5",
        lifecycle: "rejected",
        clientMutationId: params.clientMutationId,
      }),
    }));

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toMatchObject({
      code: -32013,
    });
    // The adopted snapshot is the daemon's truth; only the confirmation is
    // withheld.
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("ready");
  });

  test("a pick the reply records as still pending resolves", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/model/set", (params) => ({
      disposition: "accepted",
      modelControl: modelControlSnapshot(2, {
        desiredProvider: "openai",
        desiredModel: "gpt-5.5",
        lifecycle: "pending",
        clientMutationId: params.clientMutationId,
      }),
    }));

    await threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
  });

  // The phone resolved successfully in this shape because a same-session read
  // had retired the request's publication fence before the reply arrived. The
  // fence governs publication only; the validated lifecycle decides what the
  // caller is told.
  test("a rejected lifecycle still rejects after a refresh advances the read generation", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");

    const reply = deferred<ThreadModelSetResponse>();
    const submitted = deferred<{ clientMutationId: string }>();
    fake.on("thread/model/set", (params) => {
      submitted.resolve(params);
      return reply.promise;
    });
    const pick = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    await flushUntil(() => fake.calls.some((call) => call.method === "thread/model/set"));
    const submittedParams = await submitted.promise;
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(3)));
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");

    reply.resolve({
      disposition: "accepted",
      modelControl: modelControlSnapshot(4, {
        desiredProvider: "openai",
        desiredModel: "gpt-5.5",
        lifecycle: "rejected",
        clientMutationId: submittedParams.clientMutationId,
      }),
    });

    await expect(pick).rejects.toMatchObject({ code: -32013 });
  });

  test("a replayed switch frame gives the model it advanced to its own default tier", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(2);
    const reading = deferred<ThreadReadResponse>();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      return reads === 1 ? reading.promise : Promise.resolve(capableModelRead("ref_a", initial));
    });
    const opening = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads === 1);

    // The frames a switch to B publishes while this read is in flight. The
    // snapshot frame advances the active model, and the legacy frame carries
    // the one profile fact the snapshot does not: B's default tier. The read
    // that eventually publishes must not leave the effort control showing A's
    // default under B.
    const advanced = modelControlSnapshot(3, { activeProvider: "google", activeModel: "gemini-3-pro" });
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: advanced },
    });
    fake.emitNotification({
      method: "thread/model/changed",
      params: {
        ref: "ref_a",
        threadId: "thr_ref_a",
        modelProvider: "google",
        model: "gemini-3-pro",
        reasoningEffortLevels: ["low", "high"],
        supportsReasoning: true,
        defaultReasoningEffort: "high",
      },
    } as never);
    reading.resolve(capableModelRead("ref_a", advanced));

    await opening;
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.modelControlState.kind === "ready");
    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.model).toBe("gemini-3-pro");
    expect(model?.defaultReasoningEffort, "the replayed frame's tier for the model replay advanced to").toBe("high");
  });

  test("a tracked thread owns model-control recovery shared with a rich watcher", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(2);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });

    const recovery = deferred<ThreadReadResponse>();
    let recoveryReads = 0;
    fake.on("thread/read", () => {
      recoveryReads += 1;
      return recovery.promise;
    });
    const conflict = modelControlSnapshot(2, { activeProvider: "google", activeModel: "gemini-3-pro" });
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: conflict },
    });
    await flushUntil(() => recoveryReads > 0);

    let pickSettled = false;
    fake.on("thread/model/set", () => ({
      disposition: "accepted",
      modelControl: modelControlSnapshot(4, {
        activeProvider: "openai",
        activeModel: "gpt-5.5",
        desiredProvider: "openai",
        desiredModel: "gpt-5.5",
      }),
    }));
    const pick = threadsStore
      .getState()
      .setModel("ref_a", "openai", "gpt-5.5")
      .then(
        () => ({ ok: true as const }),
        (error: unknown) => ({ ok: false as const, error }),
      )
      .finally(() => {
        pickSettled = true;
      });
    await Promise.resolve();
    expect(pickSettled).toBe(false);
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
    expect(recoveryReads).toBe(1);

    recovery.resolve(capableModelRead("ref_a", modelControlSnapshot(3)));
    await expect(pick).resolves.toEqual({ ok: true });

    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("ready");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.modelControlState.kind).toBe("ready");
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(1);
  });

  test("a direct setModel refusal preserves the accepted shared snapshot", async () => {
    const fake = connectFakeClient();
    const accepted = modelControlSnapshot(2, {
      desiredProvider: "openai",
      desiredModel: "gpt-5.5",
      clientMutationId: "11111111-1111-4111-8111-111111111111",
    });
    fake.on("thread/read", () => capableModelRead("ref_a", accepted));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/model/set", () => {
      throw new WireError("model unavailable", -32013, { evenerErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().setModel("ref_a", "missing", "model")).rejects.toBeInstanceOf(ConflictError);

    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: accepted,
    });
  });

  test("foreign model-control notifications converge tracked and watched views and rejected reads show active", async () => {
    const fake = connectFakeClient();
    let snapshot = modelControlSnapshot(1);
    fake.on("thread/read", () => capableModelRead("ref_a", snapshot));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    snapshot = modelControlSnapshot(2, {
      desiredProvider: "google",
      desiredModel: "gemini-3-pro",
      clientMutationId: "22222222-2222-4222-8222-222222222222",
    });
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: snapshot },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({ kind: "ready", snapshot });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.modelControlState).toEqual({ kind: "ready", snapshot });

    snapshot = modelControlSnapshot(3, {
      desiredProvider: "google",
      desiredModel: "gemini-3-pro",
      lifecycle: "rejected",
      clientMutationId: "22222222-2222-4222-8222-222222222222",
    });
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: snapshot },
    });
    expect(modelControlDisplay(threadsStore.getState().threads.get("ref_a")!)).toEqual({
      modelProvider: "anthropic",
      model: "claude-sonnet-4-5",
    });

    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({ kind: "ready", snapshot });
    expect(modelControlDisplay(threadsStore.getState().threads.get("ref_a")!)).toEqual({
      modelProvider: "anthropic",
      model: "claude-sonnet-4-5",
    });
  });

  test("same-model response and notification settle without a legacy model-change event", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(1);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");
    const responseSnapshot = modelControlSnapshot(2, {
      desiredProvider: "anthropic",
      desiredModel: "claude-sonnet-4-5",
      lifecycle: "applied",
      clientMutationId: "33333333-3333-4333-8333-333333333333",
    });
    fake.on("thread/model/set", () => ({
      disposition: "accepted",
      modelControl: responseSnapshot,
    }));

    await threadsStore.getState().setModel("ref_a", "anthropic", "claude-sonnet-4-5");
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: responseSnapshot,
    });

    const notificationSnapshot = modelControlSnapshot(3, {
      desiredProvider: "anthropic",
      desiredModel: "claude-sonnet-4-5",
      lifecycle: "applied",
      clientMutationId: "33333333-3333-4333-8333-333333333333",
    });
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: notificationSnapshot },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: notificationSnapshot,
    });
  });

  test("a model-control notification ahead of the establishing read replays only for that read authority", async () => {
    const fake = connectFakeClient();
    const read = deferred<ThreadReadResponse>();
    const started = nextHandledRequest(fake, "thread/read", () => read.promise);
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await started;
    const pushed = modelControlSnapshot(2, {
      desiredProvider: "openai",
      desiredModel: "gpt-5.5",
      clientMutationId: "44444444-4444-4444-8444-444444444444",
    });
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: { ref: "ref_a", threadId: "thr_ref_a", modelControl: pushed },
    });
    read.resolve(capableModelRead("ref_a", modelControlSnapshot(1)));
    await ensuring;

    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: pushed,
    });
  });

  test("an unsupported establishing read discards a split model-control notification", async () => {
    const fake = connectFakeClient();
    const read = deferred<ThreadReadResponse>();
    const started = nextHandledRequest(fake, "thread/read", () => read.promise);
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await started;
    fake.emitNotification({
      method: "thread/model/controlChanged",
      params: {
        ref: "ref_a",
        threadId: "thr_ref_a",
        modelControl: modelControlSnapshot(2, { desiredProvider: "openai", desiredModel: "gpt-5.5" }),
      },
    });
    read.resolve(readResponse("ref_a", { modelProvider: "legacy-canonical" }));
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model).toMatchObject({
      modelProvider: "legacy-canonical",
      model: "legacy-canonical",
    });
    expect(model?.modelControlState).toEqual({
      kind: "unsupported",
      canonicalDisplay: { modelProvider: "legacy-canonical", model: "legacy-canonical" },
    });
  });

  test("an accepted unsupported refresh downgrades a capable model atomically", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/read", () => readResponse("ref_a", { modelProvider: "legacy-after-downgrade" }));

    await threadsStore.getState().refreshThread("ref_a");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model).toMatchObject({
      modelProvider: "legacy-after-downgrade",
      model: "legacy-after-downgrade",
    });
    expect(model?.modelControlState).toEqual({
      kind: "unsupported",
      canonicalDisplay: { modelProvider: "legacy-after-downgrade", model: "legacy-after-downgrade" },
    });
  });

  test("an unknown model-control version remains readable and disables model changes", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        modelProvider: "future-canonical",
        evener: {
          ref: "ref_a",
          capabilities: { ...CAPABILITIES, modelControlVersion: 2 },
          modelControl: modelControlSnapshot(1),
          queue: { revision: 0 },
        },
      }),
    );

    await threadsStore.getState().ensureThread("ref_a");

    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "unsupported",
      canonicalDisplay: { modelProvider: "future-canonical", model: "future-canonical" },
    });
    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toThrow();
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
  });

  test("a response issued before a replacement read cannot invalidate its new authority", async () => {
    const fake = connectFakeClient();
    const initial = modelControlSnapshot(1);
    fake.on("thread/read", () => capableModelRead("ref_a", initial));
    await threadsStore.getState().ensureThread("ref_a");
    const response = deferred<{
      disposition: string;
      modelControl: ModelControlSnapshot;
    }>();
    vi.spyOn(crypto, "randomUUID").mockReturnValue("66666666-6666-4666-8666-666666666666");
    const started = nextHandledRequest(fake, "thread/model/set", () => response.promise);
    const setting = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    await started;

    const replacement = modelControlSnapshot(1, {
      instanceId: "replacement-instance",
      bootGeneration: "2",
      activeProvider: "google",
      activeModel: "gemini-3-pro",
    });
    const replacementRead = deferred<ThreadReadResponse>();
    const replacementReadStarted = nextHandledRequest(fake, "thread/read", () => replacementRead.promise);
    const refreshing = threadsStore.getState().refreshThread("ref_a");
    await replacementReadStarted;

    response.resolve({
      disposition: "accepted",
      modelControl: modelControlSnapshot(9, {
        desiredProvider: "openai",
        desiredModel: "gpt-5.5",
        clientMutationId: "66666666-6666-4666-8666-666666666666",
      }),
    });
    await setting;
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: initial,
    });

    replacementRead.resolve({
      ...capableModelRead("ref_a", replacement),
      bootGeneration: "2",
      thread: testThread("ref_a", {
        id: "replacement-instance",
        modelProvider: "google",
        evener: {
          ref: "ref_a",
          capabilities: { ...CAPABILITIES, modelControlVersion: 1 },
          modelControl: replacement,
          queue: { revision: 0 },
        },
      }),
    });
    await refreshing;

    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState).toEqual({
      kind: "ready",
      snapshot: replacement,
    });
  });

  test("setVisionModel sends thread/vision-model/set with {ref, visionModel}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/vision-model/set", () => ({}));

    await threadsStore.getState().setVisionModel("ref_a", "off");

    const call = fake.calls.find((c) => c.method === "thread/vision-model/set");
    expect(call?.params).toEqual({ ref: "ref_a", visionModel: "off" });
  });

  test("setVisionModel maps a Conflict rejection to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/vision-model/set", () => {
      throw new WireError("vision model unavailable", -32013, { evenerErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().setVisionModel("ref_a", "off")).rejects.toBeInstanceOf(ConflictError);
  });

  test("setReasoningEffort sends thread/reasoning-effort/set with {ref, reasoningEffort: level}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/reasoning-effort/set", () => ({}));

    await threadsStore.getState().setReasoningEffort("ref_a", "high");

    const call = fake.calls.find((c) => c.method === "thread/reasoning-effort/set");
    expect(call?.params).toEqual({ ref: "ref_a", reasoningEffort: "high" });
  });

  test("setGoal sends goal/set and commits the successful result to the tracked model", async () => {
    const fake = connectFakeClient();
    fake.on("goal/set", () => ({ started: true }));

    threadsStore.setState({
      threads: new Map([["ref_a", hydrateThread(readResponse("ref_a"), "ref_a", 1000)]]),
    });

    const result = await threadsStore.getState().setGoal("ref_a", "ship wave 5");

    const call = fake.calls.find((c) => c.method === "goal/set");
    expect(call?.params).toEqual({ ref: "ref_a", objective: "ship wave 5" });
    expect(result).toEqual({ started: true });
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual({
      objective: "ship wave 5",
      status: "active",
      iterations: 0,
    });

    await threadsStore.getState().setGoal("ref_a", "");
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toBeNull();
  });

  test("setGoal does not overwrite a newer accepted goal notification in either tracked map", async () => {
    const fake = connectFakeClient();
    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const requestReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );

    const model = hydrateThread(readResponse("ref_a"), "ref_a", 1000);
    threadsStore.setState({
      threads: new Map([["ref_a", model]]),
      watchedThreads: new Map([["ref_a", model]]),
    });

    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await requestReachedHandler;

    const pushedGoal = { objective: "newer pushed objective", status: "active", iterations: 4 };
    fake.emitNotification({
      method: "evener/goal/updated",
      params: { threadId: model.threadId, ref: "ref_a", goal: pushedGoal },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(pushedGoal);

    resolveSetGoal({ started: true });
    await pending;

    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(pushedGoal);
  });

  test("setGoal does not overwrite a newer authoritative hydration", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const setGoalReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );
    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await setGoalReachedHandler;

    const refreshReads: Array<(response: ThreadReadResponse) => void> = [];
    let resolveRefreshReadsReached: () => void = () => {
      throw new Error("both hydration handlers were not reached");
    };
    const refreshReadsReached = new Promise<void>((resolve) => {
      resolveRefreshReadsReached = resolve;
    });
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          refreshReads.push(resolve);
          if (refreshReads.length === 2) resolveRefreshReadsReached();
        }),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await refreshReadsReached;

    const hydratedGoal = { objective: "authoritative objective", status: "active" as const, iterations: 6 };
    const authoritativeResponse = readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        goal: hydratedGoal,
      },
    });
    for (const resolveRead of refreshReads) resolveRead(authoritativeResponse);
    await flushUntil(
      () =>
        threadsStore.getState().threads.get("ref_a")?.goal?.objective === hydratedGoal.objective &&
        threadsStore.getState().watchedThreads.get("ref_a")?.goal?.objective === hydratedGoal.objective,
    );
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(hydratedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(hydratedGoal);

    resolveSetGoal({ started: true });
    await pending;

    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(hydratedGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(hydratedGoal);
  });

  test("setGoal fallback survives an unaccepted contradictory notification", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    const refreshReads: Array<(response: ThreadReadResponse) => void> = [];
    let resolveRefreshReadsReached: () => void = () => {
      throw new Error("both hydration handlers were not reached");
    };
    const refreshReadsReached = new Promise<void>((resolve) => {
      resolveRefreshReadsReached = resolve;
    });
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          refreshReads.push(resolve);
          if (refreshReads.length === 2) resolveRefreshReadsReached();
        }),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await refreshReadsReached;

    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const requestReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );

    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await requestReachedHandler;
    fake.emitNotification({
      method: "evener/goal/updated",
      params: {
        threadId: "thr_conflicting",
        ref: "ref_a",
        goal: { objective: "contradictory objective", status: "active", iterations: 9 },
      },
    });

    resolveSetGoal({ started: true });
    await pending;

    const fallbackGoal = { objective: "local objective", status: "active", iterations: 0 };
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(fallbackGoal);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.goal).toEqual(fallbackGoal);

    threadsStore.getState().releaseThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");
    for (const resolveRead of refreshReads) resolveRead(readResponse("ref_a"));
  });

  test("buffered accepted goal notification invalidates a pending response fallback", async () => {
    const fake = connectFakeClient();
    let resolveRead: (response: ThreadReadResponse) => void = () => {
      throw new Error("thread/read handler was not reached");
    };
    const readReachedHandler = nextHandledRequest(
      fake,
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );
    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await readReachedHandler;

    let resolveSetGoal: (response: { started: boolean }) => void = () => {
      throw new Error("goal/set handler was not reached");
    };
    const setGoalReachedHandler = nextHandledRequest(
      fake,
      "goal/set",
      () =>
        new Promise((resolve) => {
          resolveSetGoal = resolve;
        }),
    );
    const pending = threadsStore.getState().setGoal("ref_a", "local objective");
    await setGoalReachedHandler;

    const cut = { reached: false };
    resolveRead(markResponseCut(readResponse("ref_a"), cut));
    const pushedGoal = { objective: "buffered pushed objective", status: "active" as const, iterations: 3 };
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "evener/goal/updated",
        params: { threadId: "thr_ref_a", ref: "ref_a", goal: pushedGoal },
      }),
    );
    await ensuring;
    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);

    resolveSetGoal({ started: true });
    await pending;

    expect(threadsStore.getState().threads.get("ref_a")?.goal).toEqual(pushedGoal);
  });

  test("removeURL refuses a session the write gate rejects and sends nothing", async () => {
    const fake = connectMutationClient();
    const ref = "ref_readonly";
    fake.on("thread/read", (params) => readResponse(params.ref ?? ref, { status: { type: "restartRequired" } }));
    await threadsStore.getState().ensureThread(ref);
    await expect(threadsStore.getState().removeURL(ref, "url_1")).rejects.toThrow(/cannot accept notes/i);
    expect(fake.calls.filter((call) => call.method === "urls/remove")).toEqual([]);
  });

  test("setHumanNote does not overwrite a newer authoritative hydration", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    let resolveSetNote: (response: NotesHumanSetResponse) => void = () => {
      throw new Error("notes/human/set handler was not reached");
    };
    const setNoteReachedHandler = nextHandledRequest(
      fake,
      "notes/human/set",
      () =>
        new Promise((resolve) => {
          resolveSetNote = resolve;
        }),
    );
    const pending = threadsStore.getState().setHumanNote("ref_a", "local note");
    const params = await setNoteReachedHandler;

    const refreshReads: Array<(response: ThreadReadResponse) => void> = [];
    let resolveRefreshReadsReached: () => void = () => {
      throw new Error("both hydration handlers were not reached");
    };
    const refreshReadsReached = new Promise<void>((resolve) => {
      resolveRefreshReadsReached = resolve;
    });
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          refreshReads.push(resolve);
          if (refreshReads.length === 2) resolveRefreshReadsReached();
        }),
    );
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await refreshReadsReached;

    const authoritativeResponse = readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        humanNote: "authoritative note",
      },
    });
    for (const resolveRead of refreshReads) resolveRead(authoritativeResponse);
    await flushUntil(
      () =>
        threadsStore.getState().threads.get("ref_a")?.humanNote === "authoritative note" &&
        threadsStore.getState().watchedThreads.get("ref_a")?.humanNote === "authoritative note",
    );
    expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("authoritative note");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.humanNote).toBe("authoritative note");

    resolveSetNote({
      note: "local note",
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: "thr_ref_a",
        disposition: "applied",
        projectionState: "notProjected",
      },
    });
    await pending;
    await flushPendingTurnsProjectionForTests();
    expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);

    expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("authoritative note");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.humanNote).toBe("authoritative note");
  });

  test.each(["response", "push", "hydration", "newer draft"] as const)(
    "retry after authoritative absence respects %s authority and the submitted draft generation",
    async (boundary) => {
      const indexedDB = new IDBFactory();
      setMutationStorageForTests(new MutationOutboxIndexedDB({ indexedDB }));
      const fake = connectFakeClient();
      // A mounted draft consumer implies its session pane in production;
      // holding the ref keeps the acknowledged draft from the pane-store
      // eviction sweep this acknowledgment now schedules.
      act(() => {
        workspaceStore.setState({
          panes: [{ id: "p_retry", type: "session", params: { ref: "ref_a" }, slot: "main" }],
          focusedPaneId: "p_retry",
        });
      });
      const snapshot = (humanNote: string) =>
        readResponse("ref_a", {
          evener: { ref: "ref_a", capabilities: CAPABILITIES, humanNote, queue: { revision: 0 } },
        });
      fake.on("thread/read", () => snapshot("A"));
      fake.on("notes/human/set", (params) => ({
        note: params.note ?? "",
        receipt: {
          clientMutationId: params.clientMutationId,
          threadId: "thr_ref_a",
          disposition: "applied",
          projectionState: "notProjected",
        },
      }));
      await threadsStore.getState().ensureThread("ref_a");
      // Response-only control proves this same runtime can acknowledge an ordinary write.
      await threadsStore.getState().setHumanNote("ref_a", "smoke");
      await flushPendingTurnsProjectionForTests();
      expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);
      expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("smoke");
      syncHumanNote("ref_a", "smoke");
      const { result } = renderHook(() => useHumanNoteDraft("ref_a"));
      const firstRequest = nextHandledRequest(fake, "notes/human/set", () => {
        throw new RequestTimeoutError("request lost before acceptance");
      });
      // The draft hook is already mounted: the dispatch, the lost-response
      // settlement, and the outbox persistence all publish to it, so the whole
      // region through the persisted submitted draft is owned.
      const record = await act(async () => {
        const submitted = await threadsStore.getState().setHumanNote("ref_a", "B");
        await firstRequest;
        // vitest's wait: Testing Library's turns the act environment off while
        // it polls, and these publishes would then warn inside this act() (#3928).
        await vi.waitFor(() => expect(result.current?.submitted?.id).toBe(submitted.clientMutationId));
        return submitted;
      });
      expect(result.current?.text).toBe("B");
      const inspector = new MutationOutboxIndexedDB({ indexedDB });
      expect((await inspector.getOutbox(record.clientMutationId))?.payload).toEqual(record.payload);
      fake.on("thread/read", () => snapshot("smoke"));
      const reply = deferred<NotesHumanSetResponse>();
      const retried = nextHandledRequest(fake, "notes/human/set", () => reply.promise);
      // The ready event triggers outbox rediscovery, whose persistence refresh
      // republishes the retained draft to the mounted hook; own the region
      // through the retried request it dispatches.
      const params = await act(async () => {
        fake.emitStateChange("reconnecting");
        fake.emitReady();
        return retried;
      });
      expect(params).toEqual(record.payload);
      expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("smoke");
      if (boundary === "push") {
        act(() =>
          fake.emitNotification({
            method: "evener/notes/updated",
            params: { ref: "ref_a", threadId: "thr_ref_a", humanNote: "new authority" },
          }),
        );
      } else if (boundary === "hydration") {
        fake.on("thread/read", () => snapshot("new authority"));
        act(() =>
          fake.emitNotification({ method: "evener/thread/resync", params: { ref: "ref_a", threadId: "thr_ref_a" } }),
        );
        await waitFor(() => expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("new authority"));
      } else if (boundary === "newer draft") {
        act(() => editHumanNote("ref_a", "C"));
      }
      await act(async () =>
        reply.resolve({
          note: "B",
          receipt: {
            clientMutationId: params.clientMutationId,
            threadId: "thr_ref_a",
            disposition: "applied",
            projectionState: "notProjected",
          },
        }),
      );
      await flushPendingTurnsProjectionForTests();
      expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);
      const authoritative = boundary === "push" || boundary === "hydration" ? "new authority" : "B";
      expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe(authoritative);
      expect(result.current).toMatchObject(
        boundary === "newer draft"
          ? { text: "C", dirty: true, saved: false }
          : { text: authoritative, dirty: false, saved: true },
      );
      expect(await inspector.listOptimistic()).toEqual([]);
      inspector.close();
    },
  );

  test("a rejoin note identity acknowledges canonical text without inventing a chat item", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("notes/human/set", () => {
      throw new RequestTimeoutError("lost note response");
    });
    const record = await threadsStore.getState().setHumanNote("ref_a", "raw draft");
    syncHumanNote("ref_a", "A");
    // The mounted draft consumer implies a session pane in production; hold
    // the ref so the acknowledgment's eviction sweep keeps the record.
    act(() => {
      workspaceStore.setState({
        panes: [{ id: "p_rejoin", type: "session", params: { ref: "ref_a" }, slot: "main" }],
        focusedPaneId: "p_rejoin",
      });
    });
    const { result } = renderHook(() => useHumanNoteDraft("ref_a"));
    await waitFor(() => expect(result.current?.submitted?.id).toBe(record.clientMutationId));
    const canonical = " \n\tcanonical\u00a0e\u0301🙂  ";
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        evener: {
          ...readResponse("ref_a").thread.evener,
          humanNote: canonical,
          pendingMutations: [
            {
              clientMutationId: record.clientMutationId,
              method: "notes/human/set",
              executionState: "applied",
              projectionState: "pending",
            },
          ],
        },
        turns: [],
      }),
    );
    act(() => {
      fake.emitStateChange("reconnecting");
      fake.emitReady();
    });
    await waitFor(() => expect(result.current).toMatchObject({ text: canonical, dirty: false, saved: true }));
    await flushPendingTurnsProjectionForTests();
    expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);
    expect((await readMutationPersistence("ref_a")).optimistic).toEqual([]);
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
  });

  test("a lost note response reconnects with the same independently persisted identity and raw payload", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const reached = nextHandledRequest(fake, "notes/human/set", () => {
      throw new RequestTimeoutError("lost note response");
    });
    const raw = " \tline one\nline two\u00a0e\u0301🙂  ";
    const record = await threadsStore.getState().setHumanNote("ref_a", raw);
    const first = await reached;
    const independent = new MutationOutboxIndexedDB();
    expect((await independent.getOutbox(record.clientMutationId))?.payload).toEqual(first);
    expect(first.note).toBe(raw);
    const retried = nextHandledRequest(fake, "notes/human/set", (params) => ({
      note: raw,
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: "thr_ref_a",
        disposition: "replayed",
        projectionState: "notProjected",
      },
    }));
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    expect(await retried).toEqual(first);
    await flushPendingTurnsProjectionForTests();
    expect(await independent.getOutbox(record.clientMutationId)).toBeUndefined();
    expect(await independent.listOptimistic()).toEqual([]);
    independent.close();
  });

  test.each(["ended", "capability"])("blocked notes do not retry after %s loss", async (loss) => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const storage = new MutationOutboxIndexedDB();
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "notes/human/set",
      payload: { ref: "ref_a", note: "blocked sentinel", expectedInstanceId: "thr_ref_a" },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.markUnknown(record.clientMutationId, "blockedUnknown");
    const model = threadsStore.getState().threads.get("ref_a");
    if (!model) throw new Error("missing hydrated model");
    threadsStore.setState({
      threads: new Map([
        [
          "ref_a",
          loss === "ended"
            ? { ...model, status: { type: "ended" } }
            : { ...model, capabilities: { ...model.capabilities, sharedNotes: false } },
        ],
      ]),
      mutationAuthorityRefs: new Set(["ref_a"]),
    });
    expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
    expect(fake.calls.filter((call) => call.method === "notes/human/set")).toEqual([]);
    storage.close();
  });

  test("a urls/updated push arriving during a setHumanNote await does not drop the note commit", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    let resolveSetNote: (response: NotesHumanSetResponse) => void = () => {
      throw new Error("notes/human/set handler was not reached");
    };
    const setNoteReachedHandler = nextHandledRequest(
      fake,
      "notes/human/set",
      () =>
        new Promise((resolve) => {
          resolveSetNote = resolve;
        }),
    );
    const pending = threadsStore.getState().setHumanNote("ref_a", "local note");
    const params = await setNoteReachedHandler;

    // An unrelated urls push carries no note state: it must retire only the
    // urls fallback, leaving the in-flight note commit intact.
    fake.emitNotification({
      method: "evener/urls/updated",
      params: { threadId: "thr_ref_a", ref: "ref_a", urls: [{ id: "u1", url: "https://x.test/y" }] },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.sessionUrls).toEqual([{ id: "u1", url: "https://x.test/y" }]);

    resolveSetNote({
      note: "local note",
      receipt: {
        clientMutationId: params.clientMutationId,
        threadId: "thr_ref_a",
        disposition: "applied",
        projectionState: "notProjected",
      },
    });
    await pending;
    await flushPendingTurnsProjectionForTests();
    expect((await readMutationPersistence("ref_a")).outbox).toEqual([]);

    expect(threadsStore.getState().threads.get("ref_a")?.humanNote).toBe("local note");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.humanNote).toBe("local note");
  });

  test("rename sends evener/thread/name/set with {ref, name}", async () => {
    const fake = connectFakeClient();
    fake.on("evener/thread/name/set", () => ({}));

    await threadsStore.getState().rename("ref_a", "New title");

    const call = fake.calls.find((c) => c.method === "evener/thread/name/set");
    expect(call?.params).toEqual({ ref: "ref_a", name: "New title" });
  });

  test("compact sends thread/compact/start with {ref}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/compact/start", () => ({}));

    await threadsStore.getState().compact("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/compact/start");
    expect(call?.params).toEqual({ ref: "ref_a" });
  });

  test("shutdown sends thread/shutdown with {ref}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/shutdown", () => ({}));

    await threadsStore.getState().shutdown("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/shutdown");
    expect(call?.params).toEqual({ ref: "ref_a" });
  });

  test("forkFromTurn sends thread/fork with {ref, ...opts} and returns the response verbatim", async () => {
    const fake = connectFakeClient();
    fake.on("thread/fork", () => ({ thread: testThread("ref_child"), originalInput: undefined }));

    const result = await threadsStore
      .getState()
      .forkFromTurn("ref_a", { sourceItemKey: "apptranscript-item-v2:turn_1:0:0", editedInput: "edited text" });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    expect(call?.params).toEqual({
      ref: "ref_a",
      sourceItemKey: "apptranscript-item-v2:turn_1:0:0",
      editedInput: "edited text",
    });
    expect(result.thread.evener.ref).toBe("ref_child");
  });

  test("forkFromTurn supports the aside mode's mutually-exclusive param set", async () => {
    const fake = connectFakeClient();
    fake.on("thread/fork", () => ({ thread: testThread("ref_aside") }));

    await threadsStore.getState().forkFromTurn("ref_a", { aside: true });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    // sourceItemKey carries `omitempty` on the wire (appwire/types_turn.go), so
    // an aside-mode caller that never set it (aside is mutually exclusive
    // with it) sends no field at all.
    expect(call?.params).toEqual({ ref: "ref_a", aside: true });
  });

  // The durable clear response is the authoritative replacement snapshot; the
  // dispatcher applies it before settling the outbox record.
  test("clearThread queues thread/clear with the instance fence and applies the response snapshot", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/clear");
    expect(call?.params).toEqual({
      ref: "ref_a",
      expectedInstanceId: "thr_ref_a",
      clientMutationId: expect.any(String),
    });
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
  });

  test("clearThread removes model-control authority until a replacement read is accepted", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => capableModelRead("ref_a", modelControlSnapshot(1)));
    await threadsStore.getState().ensureThread("ref_a");
    const untrustedClearSnapshot = modelControlSnapshot(1, {
      instanceId: "replacement-instance",
      activeProvider: "google",
      activeModel: "gemini-3-pro",
    });
    fake.on("thread/clear", (params) =>
      clearResponse(
        params,
        testThread("ref_a", {
          id: "replacement-instance",
          modelProvider: "google",
          evener: {
            ref: "ref_a",
            capabilities: { ...CAPABILITIES, modelControlVersion: 1 },
            modelControl: untrustedClearSnapshot,
            queue: { revision: 0 },
          },
        }),
      ),
    );
    const replacementRead = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => replacementRead.promise);
    await threadsStore.getState().clearThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("recovering");

    fake.on("thread/model/set", (params) =>
      acceptedModelResponse(
        modelControlSnapshot(2, {
          instanceId: "replacement-instance",
          desiredProvider: params.modelProvider,
          desiredModel: params.model,
          clientMutationId: params.clientMutationId,
        }),
      ),
    );
    const setting = threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5");
    expect(fake.calls.filter((call) => call.method === "thread/model/set")).toHaveLength(0);
    replacementRead.resolve({
      ...capableModelRead("ref_a", untrustedClearSnapshot),
      thread: testThread("ref_a", {
        id: "replacement-instance",
        modelProvider: "google",
        evener: {
          ref: "ref_a",
          capabilities: { ...CAPABILITIES, modelControlVersion: 1 },
          modelControl: untrustedClearSnapshot,
          queue: { revision: 0 },
        },
      }),
    });
    await setting;
    expect(fake.calls.filter((call) => call.method === "thread/model/set").at(-1)?.params).toMatchObject({
      ref: "ref_a",
      modelProvider: "openai",
      model: "gpt-5.5",
    });
  });

  test("clearThread updates a watched model tracking the same ref too", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toEqual([]);
  });

  // Dual-map atomicity (round-2 fix): thread/clear's response snapshot lands
  // in threads and watchedThreads through ONE setState (applyClearResponse's
  // single patch), so a synchronous subscriber never sees threads cleared
  // while watchedThreads still holds the old turns.
  test("clearThread replaces both maps in one setState - no synchronous subscriber sees split state", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));

    const snapshots: { tracked: number; watched: number }[] = [];
    const unsubscribe = threadsStore.subscribe((state) => {
      snapshots.push({
        tracked: state.threads.get("ref_a")?.turns.length ?? -1,
        watched: state.watchedThreads.get("ref_a")?.turns.length ?? -1,
      });
    });
    try {
      await threadsStore.getState().clearThread("ref_a");
    } finally {
      unsubscribe();
    }

    for (const [i, snap] of snapshots.entries()) {
      expect(snap.tracked, `snapshot ${i} must not be split`).toBe(snap.watched);
    }
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toEqual([]);
  });

  test("clearThread retains a transport failure for retry and leaves the tracked model untouched", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/clear", () => {
      throw new Error("turn in progress");
    });

    const before = threadsStore.getState().threads.get("ref_a");
    await threadsStore.getState().clearThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")).toBe(before);
    const persistence = await readMutationPersistence("ref_a");
    expect(persistence.outbox).toHaveLength(1);
    expect(persistence.outbox[0]?.method).toBe("thread/clear");
  });

  // A canceled row's only exits are explicit Retry and the thread going
  // away: when the clear lands, the row leaves with the history it belonged
  // to (docs/design/stop-cancellation-outbox.md §6).
  test("clearThread removes the ref's canceled rows once the clear lands", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await threadsStore.getState().ensureThread("ref_a");
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "stopped before sending" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    await flushPendingTurnsProjectionForTests();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
  });

  // RoboRev's detached-promise finding: discardCanceledMutations fires the pin
  // refresh without awaiting it, and refreshMutationPins reads storage
  // (listOutbox/listOptimistic) - reads that can reject (a timeout, a
  // VersionError, a retired connection) after the discard already committed.
  // The fired promise must stay inside the discard's best-effort envelope
  // rather than escaping as an unhandled rejection. Reaching the end of this
  // test instead of the runner failing on an unhandled rejection is the
  // assertion, the same rule as the credentials retry test.
  test("a pin-refresh read failing after a successful discard stays best-effort, not an unhandled rejection", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await threadsStore.getState().ensureThread("ref_a");
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_a",
      threadId: "thr_ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "stopped before sending" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_a");

    // The discard commits; the pin refresh that follows reads storage, and
    // that read fails. The wrapper only arms the failure after the real
    // discard resolves and fires it exactly once - clearThread's own awaited
    // pin refresh that follows must keep the real reads - so the discard
    // itself stays real and only its fired refresh pays the failure.
    const realDiscardCanceled = storage.discardCanceled.bind(storage);
    const realListOptimistic = storage.listOptimistic.bind(storage);
    storage.discardCanceled = (targetRef: string) =>
      realDiscardCanceled(targetRef).then((discarded) => {
        storage.listOptimistic = () => {
          storage.listOptimistic = realListOptimistic;
          return Promise.reject(new Error("pin refresh read failed"));
        };
        return discarded;
      });

    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { turns: [] })));
    await threadsStore.getState().clearThread("ref_a");

    await flushPendingTurnsProjectionForTests();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
    // Let a would-be unhandled rejection surface: a few real event-loop turns,
    // each a real storage read the same runtime serves.
    const probe = new MutationOutboxIndexedDB();
    for (let round = 0; round < 5; round += 1) await probe.listOutbox("ref_a");
    probe.close();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
  });

  // The other half of §6's removal rule: the hub's durable deletion fence is
  // the moment the ref provably no longer exists, so its canceled rows go too.
  test("a hub-deleted thread's canceled rows are removed when the deletion fence lands", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectFakeClient();
    const canceled = await storage.enqueueIntent({
      targetRef: "ref_gone",
      threadId: "thr_ref_gone",
      method: "turn/queue",
      payload: { ref: "ref_gone", input: [{ type: "text", text: "stopped before sending" }] },
      attachments: [],
      optimisticDisplay: null,
    });
    await storage.cancelUnattempted("ref_gone");
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });

    // A deleted ref's ensureThread never settles (its retry loop treats every
    // rejection as transient); the deletion fence, not the promise, is the
    // observable outcome.
    void threadsStore.getState().ensureThread("ref_gone");

    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("ref_gone")).toBe(true);
    });
    await flushPendingTurnsProjectionForTests();
    expect(await storage.getOutbox(canceled.clientMutationId)).toBeUndefined();
  });

  // UI-01: a durable deletion fence is terminal for the ref's hydration
  // lifecycle. A pane retains its claim, but the hub has proven the ref gone,
  // so the retry loop must not keep reading an unrecoverable target and the
  // acquisition promise must settle instead of hanging forever.
  test("a durably deleted ref's hydration is terminal: no retry and a settled acquisition", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });

    const acquisition = threadsStore.getState().ensureThread("local:ref_gone");
    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("local:ref_gone")).toBe(true);
    });
    // The acquisition settles rather than awaiting a retry that can never
    // publish into this owner generation.
    await expect(acquisition).resolves.toBeUndefined();
    expect(reads).toBe(1);
    // Nothing is left to advance: the fence armed no retry.
    expect(scheduledHydrationRetries).toHaveLength(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  });

  // UI-01's cached-model half: a fence that lands after the ref hydrated must
  // still expose the deletion. The store deliberately keeps the (now stale)
  // model, so the deleted surface has to be read from deletedRefs rather than
  // from "no model".
  test("a deletion fence recorded after hydration exposes the deletion while the cached model remains", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_gone", { name: "Soon gone" }));
    await threadsStore.getState().ensureThread("ref_gone");
    expect(threadsStore.getState().threads.has("ref_gone")).toBe(true);

    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    await expect(threadsStore.getState().refreshThread("ref_gone")).rejects.toThrow(/deleted/);
    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("ref_gone")).toBe(true);
    });
    // The stale model is retained, so the deletion is observable independently
    // of it, and no retry is armed against the unrecoverable target.
    expect(threadsStore.getState().threads.has("ref_gone")).toBe(true);
    expect(scheduledHydrationRetries).toHaveLength(0);
  });

  // Control: the terminal deleted path must not swallow ordinary transient
  // failures - a transport rejection still schedules exactly one retry that
  // recovers the ref.
  test("a transient read failure still schedules one retry and recovers", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      if (reads === 1) throw new Error("transport hiccup");
      return readResponse("ref_a", { name: "Recovered" });
    });

    const acquisition = threadsStore.getState().ensureThread("ref_a");
    await waitFor(() => {
      expect(scheduledHydrationRetries).toHaveLength(1);
    });
    expect(threadsStore.getState().deletedRefs.has("ref_a")).toBe(false);

    runScheduledHydrationRetry();
    await expect(acquisition).resolves.toBeUndefined();
    await waitFor(() => {
      expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    });
    expect(reads).toBe(2);
  });

  // RoboRev on #3010 (Medium): a fence must retire an already-scheduled retry
  // immediately, not leave it to fire. Both hydrate paths call
  // scheduleOwnedHydrationRetry right after recording the fence, and its
  // deleted branch retires the lifecycle - so the timer is cancelled before the
  // rejection even propagates.
  test("a deletion fence cancels an already-scheduled retry immediately and settles the acquisition", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => {
      throw new Error("transport hiccup");
    });
    const acquisition = threadsStore.getState().ensureThread("ref_a");
    await waitFor(() => {
      expect(scheduledHydrationRetries).toHaveLength(1);
    });
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(false);

    // The thread is deleted before the retry timer fires: the next read is
    // fenced, and the live retry must be retired there and then.
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:ref_a", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    await expect(threadsStore.getState().refreshThread("ref_a")).rejects.toThrow(/deleted/);

    // No timer advance: the fence retired the lifecycle synchronously.
    expect(scheduledHydrationRetries[0]?.cancelled).toBe(true);
    await expect(acquisition).resolves.toBeUndefined();
    expect(threadsStore.getState().deletedRefs.has("ref_a")).toBe(true);
  });

  // RoboRev on #3010 (Low): the watched (subagent/delegate card) path needs the
  // same terminal coverage as the pane path - a watcher that failed to settle
  // would hang its caller indefinitely.
  test("a durably deleted watched ref's hydration is terminal: no retry and a settled acquisition", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    fake.on("thread/read", () => {
      reads += 1;
      throw new WireError("target has been deleted: local:ref_gone", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });

    const acquisition = threadsStore.getState().watchThread("local:ref_gone");
    await waitFor(() => {
      expect(threadsStore.getState().deletedRefs.has("local:ref_gone")).toBe(true);
    });
    await expect(acquisition).resolves.toBeUndefined();
    expect(reads).toBe(1);
    expect(scheduledHydrationRetries).toHaveLength(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  });

  // Every ordinary direct-request session action reports a wire Conflict as
  // ConflictError. setModel has an authority-bearing setup and is covered by
  // its dedicated refusal test above.
  const conflictingActions: Array<[string, (state: ReturnType<typeof threadsStore.getState>) => Promise<unknown>]> = [
    ["thread/reasoning-effort/set", (s) => s.setReasoningEffort("ref_a", "high")],
    ["thread/vision-model/set", (s) => s.setVisionModel("ref_a", "off")],
    ["evener/thread/name/set", (s) => s.rename("ref_a", "New title")],
    ["thread/compact/start", (s) => s.compact("ref_a")],
    ["thread/fork", (s) => s.forkFromTurn("ref_a", { aside: true })],
    ["thread/shutdown", (s) => s.shutdown("ref_a")],
  ];
  test.each(conflictingActions)("%s maps a Conflict rejection to ConflictError", async (method, run) => {
    const fake = connectFakeClient();
    fake.on(method as never, () => {
      throw new WireError("unavailable", -32013, { evenerErrorInfo: "conflict" });
    });

    await expect(run(threadsStore.getState())).rejects.toBeInstanceOf(ConflictError);
  });

  // Issue #195's RCA / PR #211's rejected transport-queuing design: a
  // mutation whose first attempt may already be executing server-side must
  // never blind-retry across a reconnect - it could double-fire a
  // non-idempotent method. Unlike the read-only actions (listJobs and
  // siblings, gated through requireReadyClient), every session action here
  // calls client.request() directly with no ready-wait, so it keeps
  // AppwireClient's synchronous "cannot call ... while reconnecting"
  // rejection. One representative case stands in for the whole family -
  // each wraps the identical direct client.request() call (proven above);
  // repeating this per method would test the same rejection path over and
  // over rather than add real coverage.
  test("setReasoningEffort rejects synchronously while reconnecting, unlike the read-only actions", async () => {
    const fake = connectFakeClient();
    fake.emitStateChange("reconnecting");

    // The behavior is the rejection plus zero dispatched calls; the client's
    // own "cannot call ... while ..." wording is copy, not an oracle.
    await expect(threadsStore.getState().setReasoningEffort("ref_a", "high")).rejects.toThrow();
    expect(fake.calls).toHaveLength(0); // never sent - the real client never reaches socket.send() either
  });
});

describe("useThreadsStore.send", () => {
  test("calls turn/start with text and a base64 image attachment (wire InputItem.data/mediaType/name - appwire/types_turn.go)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "hello", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" }]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "hello" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
  });

  test("send with no attachments sends text-only input", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore.getState().send("ref_a", "hello");
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [{ type: "text", text: "hello" }],
    });
  });

  // kata 6nmz: a raw "[image N]" marker on the wire misleads small models -
  // haiku read one as a file path and called read_file("[image 1]") instead of
  // looking at the vision block it was sitting next to. The composer keeps the
  // marker as its tile anchor; the wire gets prose.
  test("translates the composer's [image N] markers to prose on the wire (kata 6nmz)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "[image 1]Describe the attached image", [
        { marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "(attached image 1: pic.png)Describe the attached image" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
  });

  // kata 1gm2: the marker number rides InputAttachment so the composer, the
  // durable record and the recovery draft can pair text to attachment by
  // identity. It is client-side state and the daemon must never see it.
  test("the attachment's marker number never reaches the wire (kata 1gm2)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "[image 7]look", [{ marker: 7, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" }]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "(attached image 7: pic.png)look" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
    expect(JSON.stringify(params)).not.toContain("marker");
  });

  test("an unnamed attachment's marker translates without a dangling name separator (kata 6nmz)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "look: [image 1]", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=" }]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "look: (attached image 1)" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
  });

  test("a marker-less text reaches the wire byte-identical, untrimmed (kata 6nmz / floor §1.12)", async () => {
    const fake = connectMutationClient();
    const dispatched = nextHandledRequest(fake, "turn/start", (params) => ({
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore
      .getState()
      .send("ref_a", "  keep\n  every\n  byte  ", [
        { marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ]);
    const params = await dispatched;

    expect(params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [
        { type: "text", text: "  keep\n  every\n  byte  " },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
  });

  // Issue #195's RCA / PR #211's rejected transport-queuing design: a
  // mutation whose first attempt may already be executing server-side must
  // never blind-retry across a reconnect - it could double-fire a
  // non-idempotent method like turn/start. Unlike the read-only actions
  // (listJobs and siblings, gated through requireReadyClient), the mutation
  // outbox's own enqueueMutationIntent (stores/threads.ts) checks
  // client.state itself and rejects synchronously with no ready-wait -
  // representative of the whole outbox family (steer/queue/interrupt/
  // drainAsSteer/promoteQueuedAsSteer/cancelQueued all funnel through the
  // same gate).
  test("rejects synchronously while reconnecting, unlike the read-only actions (representative outbox mutation)", async () => {
    const fake = connectFakeClient();
    fake.emitStateChange("reconnecting");

    await expect(threadsStore.getState().send("ref_a", "hello")).rejects.toThrow(
      /cannot enqueue mutation while reconnecting/,
    );
    expect(fake.calls).toHaveLength(0); // never sent
  });
});

describe("useThreadsStore.steer / queue / interrupt", () => {
  async function ensureActiveTurn(fake: FakeClient, ref: string): Promise<void> {
    fake.on("thread/read", (params) =>
      readResponse((params as { ref: string }).ref, {
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
        evener: {
          ref: (params as { ref: string }).ref,
          capabilities: CAPABILITIES,
          queue: { revision: 7 },
          activeTurnId: "turn_1",
        },
      }),
    );
    await threadsStore.getState().ensureThread(ref);
  }

  test("steer sends turn/steer with the composer input and no turn id", async () => {
    const fake = connectMutationClient();
    await ensureActiveTurn(fake, "ref_a");
    fake.on("turn/steer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().steer("ref_a", "steer text");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/steer"));

    const call = fake.calls.find((c) => c.method === "turn/steer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      input: [{ type: "text", text: "steer text" }],
    });
  });

  test("steer includes a base64 image attachment when provided", async () => {
    const fake = connectMutationClient();
    await ensureActiveTurn(fake, "ref_a");
    fake.on("turn/steer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore
      .getState()
      .steer("ref_a", "steer text", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=" }]);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/steer"));

    const call = fake.calls.find((c) => c.method === "turn/steer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      input: [
        { type: "text", text: "steer text" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
  });

  // Stop never names a turn, even here, where this client is tracking one.
  // Naming it could only ever make Stop fail -- the id goes stale when a turn
  // rolls over between the click and the request -- and a refused Stop is the
  // failure the button exists to prevent.
  test("interrupt asks for whatever is running even when this client knows the turn id", async () => {
    const fake = connectMutationClient();
    await ensureActiveTurn(fake, "ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().interrupt("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));

    const call = fake.calls.find((c) => c.method === "turn/interrupt");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
    });
  });

  // Same request from the opposite client state: the session is working, so
  // Stop is on screen, but no turn id has reached this client -- a turn the
  // session started for itself, a boundary between two turns of one drain, or a
  // cold client. This is the window a turn-scoped Stop could not address at all,
  // and the request it produces is identical to the one above.
  test("interrupt with no turn id asks the daemon to stop whatever is running", async () => {
    const fake = connectMutationClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref, { status: { type: "active" } }));
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId ?? "").toBe("");
    fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().interrupt("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));

    const call = fake.calls.find((c) => c.method === "turn/interrupt");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
    });
  });

  test("queue sends turn/queue with the composer input and no turn id", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().queue("ref_a", "queued text");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    const call = fake.calls.find((c) => c.method === "turn/queue");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      input: [{ type: "text", text: "queued text" }],
    });
  });

  test("a queue receipt's confirmed lineage reaches the accepted copy", async () => {
    const storage = new MutationOutboxIndexedDB();
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/queue", (params) => ({
      receipt: {
        ...mutationReceipt(params.clientMutationId),
        projectionState: "pending",
        queueEntryIds: ["entry_1"],
      },
    }));

    await threadsStore.getState().queue("ref_a", "queued text");
    await flushIndexedDBUntil(async () => {
      const [accepted] = await storage.listOptimistic("ref_a");
      return accepted?.queueEntryIds?.includes("entry_1") === true;
    });

    const [accepted] = await storage.listOptimistic("ref_a");
    expect(accepted?.queueEntryIds).toEqual(["entry_1"]);
    storage.close();
  });

  test("queue includes a base64 image attachment when provided", async () => {
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore
      .getState()
      .queue("ref_a", "", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=", name: "x.png" }]);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    const call = fake.calls.find((c) => c.method === "turn/queue");
    // queueText allows empty text when attachments are present (parity
    // finding §B: "image-only queue entries are valid") - buildInput's
    // text.trim() guard means an empty string contributes no text item.
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      input: [{ type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "x.png" }],
    });
  });
});

// drainAsSteer (kata 0bq1 Path B): the plan's terse locked-interfaces block
// shows `drainAsSteer(ref)`, but the wire method it calls
// (TurnDrainAsSteerParams, appwire/types_turn.go) carries an optional
// Input the daemon appends before draining ("Input lets clients atomically
// append the current composer payload before the drain"), and the parity
// floor's Path B row requires exactly that ("anything + non-empty queue ...
// turn/drainAsSteer carrying the textarea text/items so the daemon
// appends-then-drains atomically" - parity-m5-composer.md §A). Shipping a
// bare `drainAsSteer(ref)` would silently drop the composer's pending
// text/attachments on every Path-B drain, contradicting both the parity
// floor and the "optimistic pending applies uniformly to
// send/steer/queue/drain" binding constraint (which needs to know WHAT was
// submitted to render a pending chip). This store therefore ships
// `drainAsSteer(ref, text, attachments?)`, mirroring send/steer/queue's own
// shape exactly - flagged in the T1 report as an interpretation, not a
// silent deviation.
describe("useThreadsStore.drainAsSteer", () => {
  test("sends turn/drainAsSteer with the composer's text and attachments as input", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/drainAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore
      .getState()
      .drainAsSteer("ref_a", "drain text", [{ marker: 1, mediaType: "image/png", data: "aGVsbG8=" }]);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/drainAsSteer"));

    const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      expectedQueueRevision: 7,
      input: [
        { type: "text", text: "drain text" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
  });

  test("sends an empty input array when the composer was empty (draining the queue alone)", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/drainAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().drainAsSteer("ref_a", "");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/drainAsSteer"));

    const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      expectedQueueRevision: 7,
      input: [],
    });
  });

  // A drain's own thread/queueChanged push names the client mutation ids it
  // consumed (issue #1704). The frame proves the transition, not that the
  // replacement is visible, so the record survives until an authoritative
  // snapshot shows it is no longer queued, pending, or recorded.
  test("a queueChanged naming consumed ids keeps the record until an authoritative snapshot", async () => {
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({
      receipt: appliedReceipt(params.clientMutationId, "thr_ref_a", { projectionState: "pending" }),
    }));

    await threadsStore.getState().queue("ref_a", "queued");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    const inspector = new MutationOutboxIndexedDB();
    let record: Awaited<ReturnType<typeof inspector.listOptimistic>>[number] | undefined;
    for (let attempt = 0; attempt < 20 && !record; attempt += 1) {
      [record] = await inspector.listOptimistic("ref_a");
    }
    if (!record) throw new Error("queued record never reached the optimistic store");

    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 8 },
        consumedClientMutationIds: [record.clientMutationId],
      },
    });

    await flushIndexedDBUntil(() => false);
    expect(await inspector.getOptimistic(record.clientMutationId)).toBeDefined();

    fake.on("thread/read", () =>
      readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          mutationStateAuthoritative: true,
          queue: { revision: 8 },
        },
        turns: [
          {
            id: "turn_replacement",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: "item_replacement",
                turnId: "turn_replacement",
                type: "userMessage",
                text: "queued",
                clientMutationId: "replacement",
                version: 1,
              },
            ],
          },
        ],
      }),
    );
    await threadsStore.getState().refreshThread("ref_a");
    await flushIndexedDBUntil(async () => (await inspector.getOptimistic(record.clientMutationId)) === undefined);

    expect(await inspector.getOptimistic(record.clientMutationId)).toBeUndefined();
    inspector.close();
  });

  test("a consumed queue row requests an authoritative refresh and stays until it resolves", async () => {
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({
      receipt: appliedReceipt(params.clientMutationId, "thr_ref_a", { projectionState: "pending" }),
    }));
    await threadsStore.getState().queue("ref_a", "queued");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    const inspector = new MutationOutboxIndexedDB();
    let record: Awaited<ReturnType<typeof inspector.listOptimistic>>[number] | undefined;
    for (let attempt = 0; attempt < 20 && !record; attempt += 1) {
      [record] = await inspector.listOptimistic("ref_a");
    }
    if (!record) throw new Error("queued record never reached the optimistic store");

    const read = deferred<ThreadReadResponse>();
    const readsBefore = fake.calls.filter((call) => call.method === "thread/read").length;
    fake.on("thread/read", () => read.promise);
    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 8 },
        consumedClientMutationIds: [record.clientMutationId],
      },
    });
    await flushUntil(() => fake.calls.filter((call) => call.method === "thread/read").length > readsBefore);
    expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThan(readsBefore);
    expect(await inspector.getOptimistic(record.clientMutationId)).toBeDefined();

    read.resolve(
      readResponse("ref_a", {
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          mutationStateAuthoritative: true,
          queue: { revision: 8 },
          pendingMutations: [
            {
              clientMutationId: "replacement",
              method: "turn/promoteQueuedAsSteer",
              executionState: "accepted",
              projectionState: "pending",
              queueEntryIds: ["q1"],
            },
          ],
        },
      }),
    );
    await flushIndexedDBUntil(() => false);
    expect(await inspector.getOptimistic(record.clientMutationId)).toBeDefined();
    inspector.close();
  });

  test("a consumed queue row survives a failed authoritative read", async () => {
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({
      receipt: appliedReceipt(params.clientMutationId, "thr_ref_a", { projectionState: "pending" }),
    }));
    await threadsStore.getState().queue("ref_a", "queued");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    const inspector = new MutationOutboxIndexedDB();
    let record: Awaited<ReturnType<typeof inspector.listOptimistic>>[number] | undefined;
    for (let attempt = 0; attempt < 20 && !record; attempt += 1) {
      [record] = await inspector.listOptimistic("ref_a");
    }
    if (!record) throw new Error("queued record never reached the optimistic store");

    const notification = {
      method: "thread/queueChanged" as const,
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 8 },
        consumedClientMutationIds: [record.clientMutationId],
      },
    };
    fake.emitNotification(notification);
    await flushIndexedDBUntil(() => false);
    expect(await inspector.getOptimistic(record.clientMutationId)).toBeDefined();

    fake.on("thread/read", () => {
      throw new Error("authoritative read failed");
    });
    await expect(threadsStore.getState().refreshThread("ref_a")).rejects.toThrow();
    expect(await inspector.getOptimistic(record.clientMutationId)).toBeDefined();
    inspector.close();
  });

  // A malformed consumedClientMutationIds (anything that is not an array of
  // non-empty strings) must be ignored outright: a string is itself iterable
  // character-by-character, so an unguarded spread would settle a record
  // named by coincidence rather than by the daemon. The fixed id below ("a")
  // is deliberately a substring of the malformed value, so an unguarded
  // spread of "abc" would wrongly retire it.
  test("a malformed consumedClientMutationIds on a queueChanged push is ignored, not iterated", async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "a" });
    setMutationStorageForTests(storage);
    const fake = connectMutationClient();
    fake.on("turn/queue", (params) => ({
      receipt: appliedReceipt(params.clientMutationId, "thr_ref_a", { projectionState: "pending" }),
    }));

    await threadsStore.getState().queue("ref_a", "queued");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));

    let record: Awaited<ReturnType<typeof storage.listOptimistic>>[number] | undefined;
    for (let attempt = 0; attempt < 20 && !record; attempt += 1) {
      [record] = await storage.listOptimistic("ref_a");
    }
    if (!record) throw new Error("queued record never reached the optimistic store");
    expect(record.clientMutationId).toBe("a");

    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 8 },
        consumedClientMutationIds: "abc",
      },
    } as unknown as AnyNotification);
    await settleCallerContinuations();

    expect(await storage.getOptimistic("a")).toBeDefined();
    storage.close();
  });
});

describe("useThreadsStore.promoteQueuedAsSteer / cancelQueued", () => {
  test("promoteQueuedAsSteer sends turn/promoteQueuedAsSteer with {ref, index, expectedEntryId}", async () => {
    const fake = connectMutationClient();
    await ensureActiveMutationTarget(fake, "ref_a");
    fake.on("turn/promoteQueuedAsSteer", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));

    await threadsStore.getState().promoteQueuedAsSteer("ref_a", 1, "entry_2", { text: "queued message" });
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/promoteQueuedAsSteer"));

    const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      index: 1,
      clientMutationId: expect.any(String),
      expectedInstanceId: "thr_ref_a",
      expectedEntryId: "entry_2",
    });
  });

  test("cancelQueued durably enqueues turn/cancelQueued with {ref, index, expectedEntryId}", async () => {
    const fake = connectMutationClient();
    fake.on("turn/cancelQueued", (params) => ({
      removedText: "queued message",
      removedImages: 2,
      receipt: mutationReceipt(params.clientMutationId),
    }));

    await threadsStore.getState().cancelQueued("ref_a", 0, "entry_1");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/cancelQueued"));

    const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
    expect(call?.params).toEqual({
      ref: "ref_a",
      index: 0,
      clientMutationId: expect.any(String),
      expectedEntryId: "entry_1",
    });
  });
});
