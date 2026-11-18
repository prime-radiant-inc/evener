import {
  ALL_CAPABILITIES as CAPABILITIES,
  emitHistoryUpdated as emitHistoryUpdatedFor,
  type TestThreadOverrides,
  testThread,
} from "../threadTestUtils";
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  EvenerDelegateInfo,
  HistoryUpdatedParams,
  Thread,
  ThreadClearResponse,
  ThreadReadResponse,
} from "@evener/appwire-client";
import {
  acquireThreadSubscription,
  DAEMONLESS_BOOT_GENERATION,
  ErrorTranscriptHistoryFailed,
  type ThreadSubscriptionLease,
  WireError,
} from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { resetConnectionStoreForTests } from "./connection";
import {
  existingEntryRoute,
  productionEntryTranscriptForTests,
  setProductionEntryCacheForTests,
} from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { readResponse } from "./threadHydrationTestUtils";
import { pendingThreadHydrationUpdatesForTests, resetThreadsStoreForTests, threadsStore } from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetWorkspaceStoreForTests();
  const factory = new IDBFactory();
  vi.stubGlobal("indexedDB", factory);
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: `parallel-resync-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  for (const lease of establishedMemberships.splice(0)) lease.release();
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();
  cache.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const establishedMemberships: ThreadSubscriptionLease[] = [];
async function establishMembership(fake: FakeClient, ref = "ref_a"): Promise<void> {
  fake.on("thread/read", (params) => echoRead(readResponse(ref), params.requestGeneration));
  const lease = acquireThreadSubscription(fake, ref);
  establishedMemberships.push(lease);
  await lease.ensure();
  fake.calls.length = 0;
}

// Ordinary entry opening crosses real IndexedDB tasks, not just microtasks.
// Observe the same handler/publication boundary before advancing the timeline.
async function flushUntil(done: () => boolean): Promise<void> {
  await vi.waitFor(() => expect(done()).toBe(true));
}

async function flushMicrotasks(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

// Echo the actual transport request without inspecting response getters early.
function echoRead(response: ThreadReadResponse, requestGeneration: number | undefined): ThreadReadResponse {
  response.requestGeneration = requestGeneration;
  return response;
}

// Match the source authority of the versioned history notifications below.
function versionedReadResponse(
  ref: string,
  overrides: TestThreadOverrides = {},
  snapshotLength = 0,
): ThreadReadResponse {
  return {
    ...readResponse(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: snapshotLength },
  };
}

function clearResponse(params: { clientMutationId: string }, thread: Thread): ThreadClearResponse {
  return {
    thread,
    ref: thread.evener.ref,
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: thread.id,
      instanceId: thread.evener.instanceId,
      projectionState: "reflected",
    },
  };
}

// A stable-delegate row waiting on a shell: the hold sample orders by
// holdSequence within one source epoch, so 900 is larger than any sequence a
// replacement daemon numbering from its own start would reach first.
function heldDelegateRow(): EvenerDelegateInfo {
  return {
    runGeneration: 1,
    delegateId: "dlg_held",
    ownerSessionId: "sess_owner",
    rootSessionId: "sess_root",
    childSessionId: "sess_child",
    transcriptRef: "local:01dlg_held",
    type: "delegate",
    lifecycle: "stable",
    phase: "working",
    status: "running",
    outcome: "pending",
    terminal: false,
    resumable: true,
    needsAttention: false,
    projectionRevision: 3,
    drainHeldByJobs: true,
    holdSequence: 900,
    runningJobCount: 2,
    oldestRunningJobStartedAt: "2026-01-03T00:00:00Z",
  };
}

// Preserve the response-cut-to-publication window, not a later settled model.
// The getter alone may run early, so the queue probe must actually detach too.
// Resync callers supply a snapshot witness because their ref already has a model.
// Emit synchronously inside this helper, adding no return microtask before input.
function markResponseCut(
  response: ThreadReadResponse,
  cut: { reached: boolean; bufferedBefore?: number },
  fake: FakeClient,
): ThreadReadResponse {
  // A valid equal-revision queue probe is already represented by the response.
  // Its real detachment proves the cut, even if a getter was inspected early.
  fake.emitNotification({
    method: "thread/queueChanged",
    params: { ref: response.thread.evener.ref, threadId: response.thread.id, queue: { revision: 0 } },
  });
  cut.bufferedBefore = pendingThreadHydrationUpdatesForTests().length;
  expect(cut.bufferedBefore).toBeGreaterThan(0);
  return {
    ...response,
    get olderCursor(): string | undefined {
      cut.reached = true;
      return response.olderCursor;
    },
  };
}

async function emitAtResponseCut(
  cut: { reached: boolean; bufferedBefore?: number },
  ref: string,
  emit: () => void,
  published: () => boolean = () => threadsStore.getState().threads.has(ref),
): Promise<void> {
  for (let i = 0; i < 20 && (!cut.reached || pendingThreadHydrationUpdatesForTests().length > 0); i += 1) {
    await Promise.resolve();
  }
  expect(cut.reached, "the response cut never ran: hydrateThread no longer reads olderCursor").toBe(true);
  expect(pendingThreadHydrationUpdatesForTests(), "the actual cut must detach the pre-response probe").toHaveLength(0);
  expect(published(), "the snapshot published before the cut window was reached").toBe(false);
  emit();
  expect(pendingThreadHydrationUpdatesForTests().length, "post-cut frames must enter the pending tail").toBeGreaterThan(
    0,
  );
}

// emitHistoryUpdated emits one history/updated notification carrying this
// file's standard thread identity plus the caller's distinct content — the
// snapshot, items or turns — and any identity field that differs.
function emitHistoryUpdated(
  client: FakeClient,
  rest: Partial<HistoryUpdatedParams> & Pick<HistoryUpdatedParams, "snapshot">,
): void {
  emitHistoryUpdatedFor(client, "ref_a", "thr_ref_a", rest);
}

describe("useThreadsStore.ensureThread", () => {
  test("an initial authoritative snapshot supersedes notifications buffered before its response", async () => {
    const fake = connectReadyFakeClient();
    const authoritativeSnapshot = readResponse("ref_a", {
      status: { type: "active" },
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
    });
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = (response) => resolve(echoRead(response, params.requestGeneration));
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    expect(resolveRead).not.toBeNull();

    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [
        {
          ...{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
          turnId: "turn_1",
        },
      ],
    });
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
    });

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(resolveRead).not.toBeNull();
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(authoritativeSnapshot);
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.activeTurnId).toBeUndefined();
    expect(model?.turns[0]?.status).toBe("completed");
    expect(model?.turns[0]?.items[0]?.output).toBe("done");
  });

  test("frames before the response cut leave no trace: nothing published to reach, and the cut drops the buffer", async () => {
    const fake = connectReadyFakeClient();
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = (response) => resolve(echoRead(response, params.requestGeneration));
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;

    // A ref-targeted frame naming a turn the snapshot will not carry, then a
    // ref-less frame on the same stream: the pair a buffer would have to route
    // on a thread id taken from the first of them. The second one needs the
    // cast because every notification's params requires `ref` on the wire
    // (ItemLifecycleParams) - the strongest form of the scenario, and still no
    // trace.
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_9", status: "inProgress", itemsView: "", startedAt: 1000 }],
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref-1",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "commandExecution", id: "item_pre_cut_1", turnId: "turn_9", status: "inProgress" },
            turnId: "turn_9",
          },
        ],
      },
    } as AnyNotification);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    finishRead(
      readResponse("ref_a", {
        turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_1"]);
    expect(model?.turns.flatMap((turn) => turn.items.map((item) => item.id))).toEqual([]);
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("an announcement frame landing after the response cut is replayed onto the published snapshot", async () => {
    // The daemon bundles a no-active-turn announcement into one synthetic turn
    // and sends it as a complete turn/completed (systemAnnouncementItem in
    // internal/appprojector/appwire_projection.go). That turn is never the
    // hydration's active turn, so an activeTurnId gate on BUFFERING loses the
    // frame outright: post-cut frames have no other route into the model, and
    // handleNotification also withholds them from the stale published model
    // while a hydration is pending.
    const fake = connectReadyFakeClient();
    const cut = { reached: false };
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = (response) => resolve(echoRead(response, params.requestGeneration));
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(
      markResponseCut(
        versionedReadResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        }),
        cut,
        fake,
      ),
    );
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [
            {
              id: "turn_system",
              status: "completed",
              itemsView: "full",
            },
          ],
          items: [
            {
              type: "systemMessage",
              id: "item_plugin_loaded_1",
              turnId: "turn_system",
              position: { entry: 0, item: 0 },
              description: "Plugin loaded: superpowers",
              text: "",
              eventKind: "plugin_loaded",
              status: "completed",
            },
          ].map((it) => ({ ...it, turnId: it.turnId ?? "turn_system" })),
        },
      } as AnyNotification),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_system", "turn_1"]);
    expect(model?.turns[0]?.items[0]?.id).toBe("item_plugin_loaded_1");
    // The real turn the snapshot was cut on is still in flight.
    expect(model?.activeTurnId).toBe("turn_1");
  });

  test("a ref-qualified announcement frame is buffered on its thread id, not on the active turn", async () => {
    const fake = connectReadyFakeClient();
    const cut = { reached: false };
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = (response) => resolve(echoRead(response, params.requestGeneration));
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(
      markResponseCut(
        versionedReadResponse("ref_a", {
          status: { type: "active" },
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        }),
        cut,
        fake,
      ),
    );
    // R36: history/updated requires ref, and the recorded producer stamps it.
    // Keep the announcement outside the active turn at the same post-cut boundary.
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "history/updated",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [
            {
              id: "turn_system",
              status: "completed",
              itemsView: "full",
            },
          ],
          items: [
            {
              type: "systemMessage",
              id: "item_hook_completed_1",
              turnId: "turn_system",
              position: { entry: 0, item: 0 },
              description: "Hook completed",
              text: "exit 0",
              eventKind: "hook_completed" as const,
              status: "completed",
            },
          ].map((it) => ({ ...it, turnId: it.turnId ?? "turn_system" })),
        },
      } satisfies AnyNotification),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_system", "turn_1"]);
    expect(model?.turns[0]?.items[0]?.id).toBe("item_hook_completed_1");
  });

  test("a turn opened and completed after the response cut arrives whole", async () => {
    // The projector announces a turn it opens implicitly (kata e5r2), so a
    // round that starts and fails inside the post-cut window reaches the
    // buffer as turn/started + turn/completed. Both must be buffered, in
    // order, or the published snapshot shows a turn that never opened or one
    // that never ended.
    const fake = connectReadyFakeClient();
    const cut = { reached: false };
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = (response) => resolve(echoRead(response, params.requestGeneration));
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(
      markResponseCut(
        versionedReadResponse("ref_a", {
          status: { type: "active" },
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
        }),
        cut,
        fake,
      ),
    );
    await emitAtResponseCut(cut, "ref_a", () => {
      emitHistoryUpdated(fake, {
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_7", status: "inProgress", itemsView: "", startedAt: 1000 }],
      });
      emitHistoryUpdated(fake, {
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "commandExecution", id: "item_tool_1", turnId: "turn_7", status: "inProgress" },
            turnId: "turn_7",
          },
        ],
      });
      emitHistoryUpdated(fake, {
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_7", status: "failed", itemsView: "", error: { message: "boom" } }],
      });
    });
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_7"]);
    expect(model?.turns[0]?.status).toBe("failed");
    expect(model?.turns[0]?.items.map((item) => item.id)).toEqual(["item_tool_1"]);
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a thread resync publishes its authoritative replacement snapshot", async () => {
    const fake = connectReadyFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) {
        return echoRead(
          readResponse("ref_a", {
            status: { type: "active" },
            evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: false }, queue: { revision: 0 } },
          }),
          params.requestGeneration,
        );
      }
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.capabilities.queue).toBe(false);

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);

    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads).toHaveLength(2);
    expect(reads[1]?.params).toMatchObject({ ref: "ref_a", includeTurns: true });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();

    const repository = productionEntryTranscriptForTests("ref_a").repository;
    expect(repository).toBeDefined();
    const acceptance = vi.spyOn(repository!, "accept");
    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        status: { type: "active", activeFlags: ["streaming"] },
      },
    });
    replacementRead.resolve?.(
      readResponse("ref_a", {
        status: { type: "active", activeFlags: ["streaming"] },
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.capabilities.queue === true);

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.capabilities.queue).toBe(true);
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
    expect(acceptance.mock.calls.map(([frame]) => frame.method)).toEqual(["thread/status/changed"]);
    expect(acceptance.mock.results.map((receipt) => receipt.value)).toEqual([true]);
    // R63 banks actual acceptance once, even when the response represents it.
    expect(threadsStore.getState().frameTimes.get("ref_a")).toHaveLength(1);
  });

  test("a refresh preserves a live active turn omitted by the snapshot cut", async () => {
    const fake = connectReadyFakeClient();
    let readCount = 0;
    const cut = { reached: false };
    let resolveRefresh: ((response: ThreadReadResponse) => void) | null = null;
    let refreshRequestGeneration: number | undefined;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) {
        return echoRead(
          versionedReadResponse("ref_a", {
            status: { type: "active" },
            turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
            evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
          }),
          params.requestGeneration,
        );
      }
      refreshRequestGeneration = (params as { requestGeneration?: number } | undefined)?.requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        resolveRefresh = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });

    await threadsStore.getState().ensureThread("ref_a");
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_m6", status: "inProgress", itemsView: "", startedAt: 1000 }],
    });
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["turn_m5", "turn_m6"]);

    const refreshing = threadsStore.getState().refreshThread("ref_a");
    await flushUntil(() => resolveRefresh !== null);
    const baselineHydrations = threadsStore.getState().hydrations.get("ref_a") ?? 0;
    const finishRefresh = resolveRefresh as unknown as (response: ThreadReadResponse) => void;
    finishRefresh(
      markResponseCut(
        {
          ...versionedReadResponse(
            "ref_a",
            {
              status: { type: "active" },
              turns: [{ id: "turn_m5", status: "completed", itemsView: "full", items: [] }],
              evener: {
                ref: "ref_a",
                capabilities: CAPABILITIES,
                queue: { revision: 0 },
              },
            },
            1,
          ),
          ...(refreshRequestGeneration !== undefined ? { requestGeneration: refreshRequestGeneration } : {}),
        },
        cut,
        fake,
      ),
    );
    await emitAtResponseCut(
      cut,
      "ref_a",
      () => {
        emitHistoryUpdated(fake, {
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [
            {
              ...{
                type: "agentMessage",
                id: "item_assistant_m6",
                turnId: "turn_m6",
                text: "skillguard turn complete",
                status: "completed",
              },
              turnId: "turn_m6",
            },
          ],
        });
        emitHistoryUpdated(fake, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ id: "turn_m6", status: "completed", itemsView: "" }],
        });
      },
      () => (threadsStore.getState().hydrations.get("ref_a") ?? 0) > baselineHydrations,
    );
    await refreshing;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_m5", "turn_m6"]);
    expect(model?.turns[1]?.items[0]?.text).toBe("skillguard turn complete");
    expect(model?.turns[1]?.status).toBe("completed");
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a refresh does not preserve a same-numbered turn from a replaced instance", async () => {
    const fake = connectReadyFakeClient();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) {
        return echoRead(
          versionedReadResponse(
            "ref_a",
            {
              turns: [{ id: "turn_m5", status: "completed", itemsView: "full", version: 1, items: [] }],
              evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
            },
            1,
          ),
          params.requestGeneration,
        );
      }
      return echoRead(
        {
          ...versionedReadResponse(
            "ref_a",
            {
              turns: [{ id: "turn_m5", status: "completed", itemsView: "full", version: 1, items: [] }],
              evener: {
                ref: "ref_a",
                instanceId: "replacement-instance",
                capabilities: CAPABILITIES,
                queue: { revision: 0 },
                activeTurnId: "turn_m6",
              },
            },
            1,
          ),
          epoch: 2,
          snapshot: { incarnation: "replacement-incarnation", length: 1 },
        },
        params.requestGeneration,
      );
    });

    await threadsStore.getState().ensureThread("ref_a");
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 2 },
      turns: [{ id: "turn_m6", status: "inProgress", itemsView: "", version: 2, startedAt: 1000 }],
    });
    // The ordinary route must actually hold the live turn this refresh drops.
    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["turn_m5", "turn_m6"]);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    await threadsStore.getState().refreshThread("ref_a");

    expect(
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.map((turn) => turn.id),
    ).toEqual(["turn_m5"]);
  });

  test("a thread resync bumps the ref's hydration generation", async () => {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", (params) => echoRead(readResponse("ref_a"), params.requestGeneration));
    await threadsStore.getState().ensureThread("ref_a");
    const initial = threadsStore.getState().hydrations.get("ref_a");
    expect(initial).toBeGreaterThanOrEqual(1);

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => (threadsStore.getState().hydrations.get("ref_a") ?? 0) > (initial ?? 0));

    expect(threadsStore.getState().hydrations.get("ref_a")).toBe((initial ?? 0) + 1);
  });

  test("a targeted resync preserves identical ordered streaming deltas and their frame times", async () => {
    const fake = connectReadyFakeClient();
    const snapshot = versionedReadResponse("ref_a", {
      status: { type: "active", activeFlags: ["streaming"] },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    let replacementRequestGeneration: number | undefined;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(snapshot, params.requestGeneration);
      replacementRequestGeneration = (params as { requestGeneration?: number } | undefined)?.requestGeneration;
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);

    const delta = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "ha", status: "inProgress" }],
      },
    };
    const delta2 = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 2 },
        items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "haha", status: "inProgress" }],
      },
    };
    replacementRead.resolve?.({
      thread: { ...snapshot.thread, name: "replacement" },
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 0 },
      ...(replacementRequestGeneration !== undefined ? { requestGeneration: replacementRequestGeneration } : {}),
    });
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");
    fake.emitNotification(delta);
    fake.emitNotification(delta2);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.text === "haha");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.items[0]?.text).toBe("haha");
    expect(threadsStore.getState().frameTimes.get("ref_a")).toHaveLength(2);
  });

  test("a targeted resync does not replay a pre-response delta already represented by its snapshot", async () => {
    const fake = connectReadyFakeClient();
    const initial = versionedReadResponse("ref_a", {
      status: { type: "active", activeFlags: ["streaming"] },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    const replacement = versionedReadResponse("ref_a", {
      status: { type: "active", activeFlags: ["streaming"] },
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "included", status: "inProgress" }],
        },
      ],
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    });
    let resolveReplacement: ((response: ThreadReadResponse) => void) | undefined;
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((resolve) => {
        resolveReplacement = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => resolveReplacement !== undefined);
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "included", status: "inProgress" }],
    });
    resolveReplacement?.(replacement);
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0]?.text === "included");

    const item = threadsStore.getState().threads.get("ref_a")?.turns[0]?.items[0];
    expect(item?.text).toBe("included");
    expect(item?.pendingText).toBeUndefined();
  });

  test("a targeted resync does not replay a pre-response item start already represented by its snapshot", async () => {
    const fake = connectReadyFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) {
        return echoRead(
          versionedReadResponse("ref_a", {
            turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
            evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
          }),
          params.requestGeneration,
        );
      }
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [{ ...{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" }, turnId: "turn_1" }],
    });
    replacementRead.resolve?.(
      versionedReadResponse("ref_a", {
        name: "replacement",
        turns: [
          {
            id: "turn_1",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                type: "agentMessage",
                id: "item_1",
                turnId: "turn_1",
                text: "snapshot",
                status: "inProgress",
              },
            ],
          },
        ],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");

    const items = threadsStore.getState().threads.get("ref_a")?.turns[0]?.items;
    expect(items).toHaveLength(1);
    expect(items?.[0]?.text).toBe("snapshot");
  });

  test("a targeted resync does not replay a pre-response turn start already represented by its snapshot", async () => {
    const fake = connectReadyFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(versionedReadResponse("ref_a"), params.requestGeneration);
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", version: 1 }],
      items: [],
    });
    replacementRead.resolve?.(
      versionedReadResponse(
        "ref_a",
        {
          name: "replacement",
          turns: [{ id: "turn_1", status: "completed", itemsView: "full", version: 2, items: [] }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
        },
        2,
      ),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns).toHaveLength(1);
    expect(model?.turns[0]?.status).toBe("completed");
    expect(model?.activeTurnId).toBeUndefined();
  });

  test("a targeted resync does not replay a pre-response queue change already represented by its snapshot", async () => {
    const fake = connectReadyFakeClient();
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(readResponse("ref_a"), params.requestGeneration);
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementRead.resolve !== null);
    fake.emitNotification({
      method: "thread/queueChanged",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        queue: { revision: 1, preview: ["pre-cut"] },
      },
    });
    replacementRead.resolve?.(
      readResponse("ref_a", {
        name: "replacement",
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 2, preview: ["snapshot"] },
        },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.name === "replacement");

    expect(threadsStore.getState().threads.get("ref_a")?.queue).toEqual({
      revision: 2,
      preview: ["snapshot"],
    });
  });

  test("a thread resync supersedes an initial same-epoch open hydration", async () => {
    const fake = connectReadyFakeClient();
    await establishMembership(fake);
    const reads: Array<(response: ThreadReadResponse) => void> = [];
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) =>
          reads.push((response) => resolve(echoRead(response, params.requestGeneration))),
        ),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => reads.length === 1);
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => reads.length === 2);

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    reads[1]?.(
      readResponse("ref_a", {
        turns: [{ id: "turn_authoritative", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: true }, queue: { revision: 0 } },
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_authoritative");
    reads[0]?.(
      readResponse("ref_a", {
        turns: [{ id: "turn_stale", status: "completed", itemsView: "full", items: [] }],
        evener: { ref: "ref_a", capabilities: { ...CAPABILITIES, queue: false }, queue: { revision: 0 } },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_authoritative");
    expect(model?.capabilities.queue).toBe(true);
  });

  test("a second thread resync supersedes the first targeted refresh in the same epoch", async () => {
    const fake = connectReadyFakeClient();
    const replacementReads: Array<(response: ThreadReadResponse) => void> = [];
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(readResponse("ref_a"), params.requestGeneration);
      return new Promise<ThreadReadResponse>((resolve) =>
        replacementReads.push((response) => resolve(echoRead(response, params.requestGeneration))),
      );
    });
    await threadsStore.getState().ensureThread("ref_a");

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementReads.length === 1);
    fake.emitNotification({
      method: "thread/status/changed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        status: { type: "active", activeFlags: ["streaming"] },
      },
    });
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a" },
    });
    await flushUntil(() => replacementReads.length === 2);

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);
    replacementReads[1]?.(
      readResponse("ref_a", {
        status: { type: "active", activeFlags: ["streaming"] },
        turns: [{ id: "turn_newest", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_newest");
    replacementReads[0]?.(
      readResponse("ref_a", {
        status: { type: "idle" },
        turns: [{ id: "turn_superseded", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    await flushMicrotasks(() => threadsStore.getState().threads.get("ref_a")?.turns[0]?.id === "turn_superseded");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns[0]?.id).toBe("turn_newest");
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
  });

  test("a thread resync for an untracked ref does not read it", async () => {
    const fake = connectReadyFakeClient();
    fake.on("thread/read", (params) => echoRead(readResponse("ref_untracked"), params.requestGeneration));

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_untracked", ref: "ref_untracked" },
    });
    await flushMicrotasks(() => fake.calls.some((call) => call.method === "thread/read"));

    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(0);
  });

  test("an initial snapshot supersedes threadId-only notifications buffered after ref routing", async () => {
    const fake = connectReadyFakeClient();
    const box: { resolveRead: ((response: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          box.resolveRead = (response) => resolve(echoRead(response, params.requestGeneration));
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => box.resolveRead !== null);
    expect(box.resolveRead).not.toBeNull();

    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_live", status: "inProgress", itemsView: "" }],
    });
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [
        {
          ...{ type: "agentMessage", id: "item_live", turnId: "turn_live", text: "answer", status: "completed" },
          turnId: "turn_live",
        },
      ],
    });

    box.resolveRead?.(
      readResponse("ref_a", {
        turns: [
          {
            id: "turn_live",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                type: "agentMessage",
                id: "item_live",
                turnId: "turn_live",
                text: "answer",
                status: "completed",
              },
            ],
          },
        ],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_live" },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns).toHaveLength(1);
    expect(model?.turns[0]?.id).toBe("turn_live");
    expect(model?.turns[0]?.items[0]?.text).toBe("answer");
  });

  test("an initial snapshot supersedes v2 notifications buffered after thread identity is established", async () => {
    const fake = connectReadyFakeClient();
    const box: { resolveRead: ((response: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on(
      "thread/read",
      (params) =>
        new Promise<ThreadReadResponse>((resolve) => {
          box.resolveRead = (response) => resolve(echoRead(response, params.requestGeneration));
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => box.resolveRead !== null);
    expect(box.resolveRead).not.toBeNull();

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active", activeFlags: ["streaming"] } },
    });
    emitHistoryUpdated(fake, {
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [
        {
          ...{ type: "commandExecution", id: "item_1", turnId: "turn_1", output: "done", status: "completed" },
          turnId: "turn_1",
        },
      ],
    });

    box.resolveRead?.(
      readResponse("ref_a", {
        status: { type: "active", activeFlags: ["streaming"] },
        turns: [
          {
            id: "turn_1",
            status: "inProgress",
            itemsView: "full",
            items: [
              {
                type: "commandExecution",
                id: "item_1",
                turnId: "turn_1",
                output: "done",
                status: "completed",
              },
            ],
          },
        ],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      }),
    );
    await ensuring;

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
    expect(model?.turns[0]?.items[0]?.output).toBe("done");
  });

  test("a resync that supersedes a pre-clear hydrate routes post-cut frames by its own snapshot's thread id", async () => {
    const fake = connectReadyFakeClient();
    const cut = { reached: false };
    const resyncReads: Array<(response: ThreadReadResponse) => void> = [];
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(readResponse("ref_a"), params.requestGeneration);
      return new Promise<ThreadReadResponse>((resolve) =>
        resyncReads.push((response) => resolve(echoRead(response, params.requestGeneration))),
      );
    });
    fake.on("thread/clear", (params) => clearResponse(params, testThread("ref_a", { id: "thr_cleared", turns: [] })));

    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.threadId).toBe("thr_ref_a");

    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushUntil(() => resyncReads.length === 1);

    await threadsStore.getState().clearThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.threadId).toBe("thr_cleared");

    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_cleared", ref: "ref_a" } });
    await flushUntil(() => resyncReads.length === 2);

    const snapshotTurn = (): boolean =>
      threadsStore
        .getState()
        .threads.get("ref_a")
        ?.turns.some((turn) => turn.id === "turn_9") === true;
    resyncReads[1]?.(
      markResponseCut(
        readResponse("ref_a", {
          id: "thr_cleared",
          turns: [{ id: "turn_9", status: "completed", itemsView: "full", items: [] }],
        }),
        cut,
        fake,
      ),
    );
    await emitAtResponseCut(
      cut,
      "ref_a",
      () =>
        fake.emitNotification({
          method: "thread/status/changed",
          params: { threadId: "thr_cleared", ref: "ref_a", status: { type: "active", activeFlags: ["streaming"] } },
        }),
      snapshotTurn,
    );
    await flushUntil(snapshotTurn);

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_9"]);
    expect(model?.status).toEqual({ type: "active", activeFlags: ["streaming"] });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toHaveLength(1);

    threadsStore.getState().releaseThread("ref_a");
  });

  // The resync branch starts its targeted read and returns, so nothing about
  // the boot-changing notification reaches the models the pane is showing: the
  // repository's own model and the store copy both keep the retired daemon's
  // hold sample until the read settles. The hold orders by holdSequence within
  // one source epoch, and a replacement daemon numbers its samples from its own
  // start, so the retired sample outranks the replacement's first unheld frame
  // — and a read that fails emits no update of its own to clear it.
  test("a boot-changing resync retires the held delegate row before its targeted read settles", async () => {
    const fake = connectReadyFakeClient();
    const held = heldDelegateRow();
    const initial = versionedReadResponse("ref_a", {
      status: { type: "active" },
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
    });
    const replacementRead: { reject: ((error: Error) => void) | null } = { reject: null };
    let readCount = 0;
    let rejectReads = false;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      if (rejectReads) return Promise.reject(new Error("replacement read failed"));
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        replacementRead.reject = reject;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref: "ref_a", delegate: held },
    });
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.holdSequence === 900);

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a", bootGeneration: "2", epoch: 2 },
    });
    await flushUntil(() => replacementRead.reject !== null);

    // Both models the pane can serve from must already be retired: the store
    // copy, and the repository's own model, which a later publication hands
    // back to the store.
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.holdSequence).toBeUndefined();
    expect(
      productionEntryTranscriptForTests("ref_a").repository?.getSnapshot()?.delegates?.[0]?.holdSequence,
    ).toBeUndefined();
    // The durable row survives the retirement: only the hold sample goes.
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]).toMatchObject({
      delegateId: "dlg_held",
      transcriptRef: "local:01dlg_held",
      lifecycle: "stable",
      status: "running",
      projectionRevision: 3,
    });

    // A read that fails leaves the retired hold retired: nothing restores it.
    rejectReads = true;
    replacementRead.reject?.(new Error("replacement read failed"));
    await flushUntil(() => readCount > 2);
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.holdSequence).toBeUndefined();
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.drainHeldByJobs).toBeUndefined();

    threadsStore.getState().releaseThread("ref_a");
  });

  // M1: an empty clear still has to record the new owner. The resync retires
  // nothing here because there is no sample yet, so unless it also records
  // boot 2 as the hold's source, the next boot-2 notification compares against
  // boot 1, reads as another replacement, and clears the live hold boot 2 just
  // published — which emits no update of its own to restore.
  test("a replacement records its ownership so its own later frames keep the hold", async () => {
    const fake = connectReadyFakeClient();
    const initial = versionedReadResponse("ref_a", {
      status: { type: "active" },
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
    });
    const replacementRead: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((resolve) => {
        replacementRead.resolve = (response) => resolve(echoRead(response, params.requestGeneration));
      });
    });
    await threadsStore.getState().ensureThread("ref_a");

    // Boot 2 takes over while no hold sample exists: nothing to retire.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a", bootGeneration: "2", epoch: 2 },
    });
    await flushUntil(() => replacementRead.resolve !== null);

    // Boot 2 publishes a live wait. Nothing has read successfully yet.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref: "ref_a", delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.holdSequence === 900);

    // A second notification from the SAME boot must keep it: boot 2 owns the
    // sequence now, so this is not a replacement.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a", bootGeneration: "2", epoch: 3 },
    });
    await flushUntil(() => readCount >= 3);
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.holdSequence).toBe(900);
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.drainHeldByJobs).toBe(true);

    threadsStore.getState().releaseThread("ref_a");
  });

  test("a same-boot resync preserves the held delegate row and its ordering", async () => {
    const fake = connectReadyFakeClient();
    const held = heldDelegateRow();
    const initial = versionedReadResponse("ref_a", {
      status: { type: "active" },
      evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
    });
    const replacementRead: { reject: ((error: Error) => void) | null } = { reject: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        replacementRead.reject = reject;
      });
    });
    await threadsStore.getState().ensureThread("ref_a");
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref: "ref_a", delegate: held },
    });
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.holdSequence === 900);

    // The same boot generation bumps its own epoch to rebuild the transcript
    // index. Its delegate controller is untouched and still sampling, and an
    // unchanged hold emits no update of its own to restore what we dropped.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref: "ref_a", bootGeneration: "1", epoch: 2 },
    });
    await flushUntil(() => replacementRead.reject !== null);

    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.holdSequence).toBe(900);
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.drainHeldByJobs).toBe(true);
    expect(threadsStore.getState().threads.get("ref_a")?.delegates?.[0]?.runningJobCount).toBe(2);

    threadsStore.getState().releaseThread("ref_a");
  });

  // M2: a structured read failure is not an exemption from publishing what the
  // replacement already said. A boot-changing resync clears the retired daemon's
  // hold and buffers the replacement's delegate updates while its targeted read
  // runs. When that read answers transcriptHistoryFailed, the failure branch used
  // to publish the pre-update base model, and hydration cleanup then dropped the
  // buffered hold - so the watched row omitted the wait for its whole duration,
  // since an unchanged hold emits no update of its own to restore it.
  test("a watched read that fails still publishes the replacement's buffered hold", async () => {
    const fake = connectReadyFakeClient();
    const ref = "local:watched";
    const threadId = `thr_${ref}`;
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    // The refused read stays open until the test rejects it, so the delegate
    // frame below is buffered for its replay rather than folded live.
    const refused: { reject: ((error: Error) => void) | null } = { reject: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.reject = reject;
      });
    });

    // A watched-only ref: the delegate card's live view, not an open pane.
    await threadsStore.getState().watchThread(ref, { includeTurns: true });
    expect(threadsStore.getState().watchedThreads.has(ref)).toBe(true);

    // Boot 1 publishes the retired daemon's hold: 900 outranks anything the
    // replacement numbers from its own start, which is why it must not survive.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    // Boot 2 takes over. The resync retires boot 1's hold before starting its
    // read, and the frames arriving while that read is pending are buffered.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 2 },
    });
    await flushUntil(() => refused.reject !== null);
    expect(
      threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence,
      "the replacement must retire the retired daemon's sample before its read settles",
    ).toBeUndefined();

    // Boot 2 publishes its own live wait while the read is still pending. Its
    // sequence is lower because it numbers from its own start.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });

    // The read fails structurally. The failure must publish the buffered hold,
    // not the pre-update base model that predates it.
    refused.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(1);
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.drainHeldByJobs).toBe(true);
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.runningJobCount).toBe(2);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  // M2: the failure belongs BEFORE the replay, not after it. A buffered
  // `history/updated` from the replacement boot is compared against the OLD boot
  // still in history, so replaying it first invalidates at this read's own
  // generation - and `applyHistoryReadFailure` then reads its own failure as
  // superseded (`requestGeneration <= invalidatedAtGeneration`) and drops the
  // diagnostic. The store publishes invalidated history with no diagnostic, and
  // that publication is not the live invalidation path that schedules a retry.
  // Applying the failure first makes every replay take the failed-history branch,
  // which keeps the diagnostic and still carries the replacement's hold.
  test("a failed watched read keeps its diagnostic when a replacement frame was buffered", async () => {
    const fake = connectReadyFakeClient();
    const ref = "local:watched";
    const threadId = `thr_${ref}`;
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    const refused: { reject: ((error: Error) => void) | null } = { reject: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.reject = reject;
      });
    });

    await threadsStore.getState().watchThread(ref, { includeTurns: true });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 2 },
    });
    await flushUntil(() => refused.reject !== null);

    // The replacement's own history frame arrives while the read is pending. Its
    // boot is not the one still recorded in history, which is what makes the
    // replay order matter.
    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "2",
      epoch: 2,
      snapshot: { incarnation: "inc-2", length: 1 },
      turns: [],
      items: [],
    });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    // Non-vacuous: the pending read withholds both frames from the published
    // model, so neither reached it live. They exist only in its buffer.
    await Promise.resolve();
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBeUndefined();

    refused.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The diagnostic survives the buffered replay, and so does the hold.
    expect(published?.history?.failed).toBeDefined();
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a same-boot resync carries the superseded read's buffered hold into its failure", async () => {
    const fake = connectReadyFakeClient();
    const ref = "local:watched";
    const threadId = `thr_${ref}`;
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    const refused: Array<(error: Error) => void> = [];
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.push(reject);
      });
    });

    await threadsStore.getState().watchThread(ref, { includeTurns: true });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    // Boot 2 takes over: the retired 900 leaves the published model and the
    // replacement's read is pending.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBeUndefined();

    // The replacement publishes its own live hold while that read is pending,
    // so the frame exists only in the pending read's buffer.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    await Promise.resolve();
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBeUndefined();

    // A second resync from the SAME boot supersedes the first watched read.
    // That is not a replacement, so its buffered hold must come along.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 3 },
    });
    await flushUntil(() => refused.length === 2);

    refused[1]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // Nothing restores a hold dropped here: an unchanged hold emits no update
    // of its own, so the wait would stay absent for its whole duration.
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  // The sibling direction of the M2 finding, on the entry-backed path: the
  // repository owns recorded ingestion, so a delegate frame during a pending
  // tracked read is accepted by the repository itself before it reaches this
  // buffer. A structured failure publishes the repository's own snapshot, not a
  // base model captured before the frame, so that hold is not discarded. This is
  // the sweep's third path, asserted rather than assumed.
  test("a named resync after a bootless start keeps the hold the buffer's own history named", async () => {
    // A bootless relay-recovery resync starts the read from boot 1's model.
    // Boot 2 then publishes its history/updated and, behind it, its hold - so
    // both land in the buffer and the base model never learned about boot 2.
    // A named boot-2 resync then supersedes the read. Judging the buffer by the
    // base model's owner compares boot 2 against boot 1 and throws the hold
    // away, though the buffer itself names boot 2 as the owner.
    const fake = connectReadyFakeClient();
    const ref = "local:watched";
    const threadId = `thr_${ref}`;
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    const refused: Array<(error: Error) => void> = [];
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.push(reject);
      });
    });

    await threadsStore.getState().watchThread(ref, { includeTurns: true });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    // BOOTLESS: a relay-recovery resync names no source, so it retires nothing
    // and starts a read from boot 1's model.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    // Boot 2's own frames arrive while that read is pending: its history first,
    // then its hold. Both are buffered, so the base model still says boot 1.
    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "2",
      epoch: 2,
      snapshot: { incarnation: "inc-2", length: 1 },
      turns: [],
      items: [],
    });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the bootless resync retires nothing, so boot 1's 900 is
    // still what the published model shows. Boot 2's 1 exists only in the
    // pending read's buffer.
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(900);

    // A NAMED boot-2 resync supersedes the read. Boot 2 is the source the
    // buffer already names, so its hold comes along rather than being judged
    // against boot 1's base model.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 3 },
    });
    await flushUntil(() => refused.length === 2);

    refused[1]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    expect(published?.history?.failed).toBeDefined();
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a failed read's own boot token retires the hold it identifies", async () => {
    // A daemon exit sends a bootless resync, so the browser correctly keeps the
    // hold while it reads. If that read then FAILS, the hub stamps the failure
    // with the identity it read (appwire.HistoryReadErrorData) - here
    // "daemonless", the hub's transcript-only answer, which names a definite
    // absence of a daemon. Without a successful read there is no other evidence
    // of the replacement, so the retired daemon's 900 would outlive its owner
    // for the whole duration of the wait.
    const fake = connectReadyFakeClient();
    const ref = "local:watched";
    const threadId = `thr_${ref}`;
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    let readCount = 0;
    // The holder defeats control-flow narrowing after the awaits: the callback
    // runs later, so the compiler cannot prove `reject` was never reassigned.
    const refusedHolder: { reject: ((error: Error) => void) | null } = { reject: null };
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refusedHolder.reject = reject;
      });
    });

    await threadsStore.getState().watchThread(ref, { includeTurns: true });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    // The daemon exits: a bootless resync names no source, so it retires
    // nothing, and starts the read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refusedHolder.reject !== null);
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(900);

    refusedHolder.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: DAEMONLESS_BOOT_GENERATION,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The diagnostic stands, and the hold the failed read's own identity
    // retired is gone rather than outliving the daemon that numbered it.
    expect(published?.history?.failed).toBeDefined();
    expect(published?.delegates?.[0]?.holdSequence).toBeUndefined();
    expect(published?.delegates?.[0]?.delegateId).toBe("dlg_held");

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("an ambiguous bare resync hint keeps the buffered hold it cannot interpret", async () => {
    // A descendant is served by its root at "5@root". An OLDER daemon's
    // worktree-change resync spells that same controller "5" - a bare counter
    // that names no root, so it cannot say whether anything was replaced. The
    // replacement's wait arrives while the first read is pending, so that
    // buffer holds the ONLY copy of it. A second such hint supersedes the read;
    // dropping the buffer there publishes a transcript with no wait at all, and
    // an unchanged hold emits no update to restore it.
    const fake = connectReadyFakeClient();
    const ref = "local:watched";
    const threadId = `thr_${ref}`;
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    // Served by its ROOT: that is what makes a bare "5" below ambiguous rather
    // than a different owner.
    const qualified = { ...initial, bootGeneration: "5@root" };
    const refused: Array<(error: Error) => void> = [];
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(qualified, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.push(reject);
      });
    });

    // No hold yet: the base model this read starts from has no delegate row.
    await threadsStore.getState().watchThread(ref, { includeTurns: true });

    // The older daemon's bare "5" hint names no root, so it retires nothing and
    // starts the watched read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "5", epoch: 3 },
    });
    await flushUntil(() => refused.length === 1);

    // The wait arrives while that read is pending: it exists only in the buffer.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    await Promise.resolve();
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBeUndefined();

    // A second bare "5" supersedes the read. It is ambiguous, so the buffer's
    // hold must come along rather than being read as a confirmation.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "5", epoch: 4 },
    });
    await flushUntil(() => refused.length === 2);

    refused[1]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The diagnostic stands, and the wait survives: without the carry this is
    // undefined, so the row would omit a wait that is still live for its whole
    // duration.
    expect(published?.history?.failed).toBeDefined();
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a failed read retires the outgoing source before folding the replacement's own wait", async () => {
    // After a bootless recovery resync, a replacement daemon publishes its
    // delegate hold BEFORE any history frame names its boot, so the buffer holds
    // a live sample of the INCOMING source. If the read then fails with that
    // daemon's boot token, folding the buffer onto the old owner first and
    // handing off second retires the replacement's own wait along with the
    // outgoing source's samples - and an unchanged hold emits no update to
    // restore it.
    const fake = connectReadyFakeClient();
    const ref = "local:watched";
    const threadId = `thr_${ref}`;
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    const refused: Array<(error: Error) => void> = [];
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.push(reject);
      });
    });

    await threadsStore.getState().watchThread(ref, { includeTurns: true });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    // A bootless resync: retires nothing, starts the watched read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    // The replacement's wait arrives with NO identifying history frame, so the
    // old owner is still the only one the model names.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the buffer holds it, and the published model still shows the
    // old wait.
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(900);

    // The read fails, and its own identity names the source that replaced boot 1.
    refused[0]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "2",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The diagnostic stands, the retired 900 is gone, and the replacement's
    // live wait survives because the retire ran before the replay.
    expect(published?.history?.failed).toBeDefined();
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a tracked read that fails keeps the hold the entry repository already accepted", async () => {
    const fake = connectReadyFakeClient();
    const ref = "ref_a";
    const initial = versionedReadResponse(
      ref,
      {
        status: { type: "active" },
        evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
      },
      0,
    );
    const refused: { reject: ((error: Error) => void) | null } = { reject: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.reject = reject;
      });
    });
    await threadsStore.getState().ensureThread(ref);
    expect(existingEntryRoute(ref)?.repository).toBeDefined();

    // Boot 1's sample is retired by the hand-off; boot 2 then publishes its own
    // live wait, which the repository accepts as its own recorded ingestion.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().threads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref, bootGeneration: "2", epoch: 2 },
    });
    await flushUntil(() => refused.reject !== null);
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId: "thr_ref_a",
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    // Non-vacuous: the repository really holds boot 2's live sample right now.
    await flushUntil(() => existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence === 1);

    refused.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get(ref)?.history?.failed !== undefined);
    // The repository owns recorded ingestion, so the failure publishes its own
    // snapshot, not a base model captured before the frame: the hold survives.
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence).toBe(1);
    expect(threadsStore.getState().threads.get(ref)?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseThread(ref);
  });
});
