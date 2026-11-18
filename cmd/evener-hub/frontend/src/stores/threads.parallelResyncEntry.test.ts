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

  test("a replacement's history frame strips the buffered hold but keeps the terminal outcome", async () => {
    // The carry path walks the superseded read's buffer and, on meeting a
    // history frame from a CONFIRMED replacement, retires the delegate samples
    // that preceded it. Their HOLD belongs to the replaced source's numbering
    // and must go. Their durable fields do not: a terminal outcome buffered
    // before that frame is the only copy of the completed row, and dropping
    // the whole update left the model showing the earlier "running" state for
    // the rest of the wait.
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
      if (readCount === 1) return echoRead({ ...initial, bootGeneration: "1" }, params.requestGeneration);
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

    // Bootless: retires nothing, starts the read from boot 1's model.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    // A TERMINAL outcome arrives while the read is pending, so the buffer holds
    // the only copy of the completed row.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: {
          ...heldDelegateRow(),
          holdSequence: 903,
          projectionRevision: 9,
          phase: "done",
          status: "completed",
          outcome: "success",
          terminal: true,
        },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the published model still shows the running row, so the
    // completed outcome exists only in the buffer.
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.terminal).toBe(false);

    // Boot 2's history frame confirms a replacement, and a named boot-2 resync
    // carries the buffer into a new read.
    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "2",
      epoch: 3,
      snapshot: { incarnation: "inc-2", length: 1 },
      turns: [],
      items: [],
    });
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 4 },
    });
    await flushUntil(() => refused.length === 2);

    // The successor read fails, so the buffer replay is the only publication.
    refused[1]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "2",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0];
    // The completed outcome survived the carry...
    expect(published?.terminal).toBe(true);
    expect(published?.outcome).toBe("success");
    expect(published?.projectionRevision).toBe(9);
    // ...and the retired source's hold did not.
    expect(published?.holdSequence).toBeUndefined();

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a named resync that confirms a replacement strips the buffered hold and keeps the terminal outcome", async () => {
    // The sibling of the case above, one step earlier in the buffer's life: here
    // the SUPERSEDING RESYNC is the first frame to confirm the replacement, so
    // no history frame inside the buffer marks it. `carriedHydrationUpdates`
    // returned an empty buffer for that case, which threw away every buffered
    // update rather than the samples' hold: a terminal outcome that arrived
    // while the read was pending then vanished, and when the successor read
    // failed the delegate showed "running" for the rest of the wait.
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
      if (readCount === 1) return echoRead({ ...initial, bootGeneration: "1" }, params.requestGeneration);
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

    // Bootless: retires nothing, starts the read from boot 1's model.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    // A TERMINAL outcome arrives while that read is pending, so the buffer
    // holds the only copy of the completed row.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: {
          ...heldDelegateRow(),
          holdSequence: 903,
          projectionRevision: 9,
          phase: "done",
          status: "completed",
          outcome: "success",
          terminal: true,
        },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the published model still shows the running row, so the
    // completed outcome exists only in the buffer.
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.terminal).toBe(false);

    // A NAMED boot-2 resync supersedes the read and is itself the first frame
    // to confirm the replacement.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 3 },
    });
    await flushUntil(() => refused.length === 2);

    // The successor read fails, so the buffer replay is the only publication.
    refused[1]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "2",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0];
    // The completed outcome survived the carry...
    expect(published?.terminal).toBe(true);
    expect(published?.outcome).toBe("success");
    expect(published?.projectionRevision).toBe(9);
    // ...and the replaced source's hold did not.
    expect(published?.holdSequence).toBeUndefined();

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

  test("a buffered frame from the retired source cannot reclaim the hold owner during failed-read replay", async () => {
    // A descendant is served by its root at "5@root"; the root then hands it to
    // the descendant's own daemon at boot "1". The bootless resync retires
    // nothing, so the buffer can already hold a history/updated from "5@root"
    // when the replacement publishes its live hold. The failure then names
    // boot "1". Replaying the buffer verbatim would fold "5@root" back as the
    // owner - the source the failure just retired - and boot 1's own next frame
    // would compare against it, read as a fresh replacement, and clear the hold
    // it published. An unchanged hold emits no update, so nothing restores it.
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
    initial.bootGeneration = "5@root";
    let readCount = 0;
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

    // The hand-off: bootless, so it starts the read and retires nothing.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refusedHolder.reject !== null);

    // Buffered while that read is pending: the OLD source's recorded frame,
    // then the replacement's live hold. Only the buffer holds the replacement's
    // sample, so the failure path is what must carry it.
    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "5@root",
      epoch: 3,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [],
      items: [],
    });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId,
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 7, projectionRevision: 4 },
      },
    });
    await Promise.resolve();
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(900);

    refusedHolder.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "1",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    // The replacement's live hold survives the failure itself.
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(7);
    // Now boot 1 speaks for itself. If the replayed "5@root" frame reclaimed
    // the owner, this frame from the source that now owns the sequence reads
    // as ANOTHER replacement and erases the live hold - the harm the finding
    // names, with the diagnostic standing over a vanished wait.
    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-2", length: 1 },
      turns: [],
      items: [],
    });
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.holdOwner === "1");
    const published = threadsStore.getState().watchedThreads.get(ref);
    expect(published?.delegates?.[0]?.holdSequence).toBe(7);
    expect(published?.delegates?.[0]?.delegateId).toBe("dlg_held");

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a buffered frame from a newer source survives a failure under the owner it replaced", async () => {
    // The failure names the source that SERVED the read - `5@root`, the owner
    // this read started under - so the hand-off retires nothing. The buffer
    // meanwhile holds `6@child`'s history frame and `6@child`'s live hold: a
    // cross-owner hand-off the failure knows nothing about. Two QUALIFIED
    // tokens from different roots compare as replacements in BOTH directions,
    // so testing the buffered frame against the failure's own token blanks
    // `6@child`, that frame folds bootless and records no owner, and `5@root`'s
    // sequence 900 survives replay to beat `6@child`'s first sample of 1.
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
    initial.bootGeneration = "5@root";
    const refusedHolder: { reject: ((error: Error) => void) | null } = { reject: null };
    let readCount = 0;
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

    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refusedHolder.reject !== null);

    // The child's own daemon takes over only through frames that arrive while
    // this read is pending; the failure itself still names the root.
    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "6@child",
      epoch: 1,
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
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(900);

    refusedHolder.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "5@root",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // `6@child` owns the sequence the buffered sample was numbered in, and the
    // failure retired nothing, so its frame keeps its identity and its first
    // sample replaces the retired daemon's 900 rather than losing to it.
    // The HARM first: the retired daemon's 900 is still on screen, so the
    // replacement's first sample of 1 loses to a sequence its owner never
    // numbered. The mechanism (no owner recorded) is the assertion below.
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);
    expect(published?.history?.holdOwner).toBe("6@child");

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

  test("the entry repository retires the outgoing source before folding the replacement's own wait", async () => {
    // The repository half of the ordering finding, with a dedicated case rather
    // than an assumed symmetry. After a BOOTLESS recovery resync the old sample
    // is still the only one the model names, and the repository accepts the
    // replacement's delegate hold LIVE before any history frame identifies its
    // boot. The failed read's own identity then names that replacement. If the
    // hand-off folds the live sample against the old owner first, it clears the
    // replacement's wait together with the samples it actually replaced.
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

    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref, delegate: heldDelegateRow() },
    });
    await flushUntil(() => threadsStore.getState().threads.get(ref)?.delegates?.[0]?.holdSequence === 900);

    // BOOTLESS: retires nothing, so boot 1's 900 stands, and starts the read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref, epoch: 2 },
    });
    await flushUntil(() => refused.reject !== null);
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence).toBe(900);

    // The replacement's wait arrives with NO identifying history frame. While
    // the outgoing 900 still stands the live frame cannot win the hold order
    // (mergeStableDelegate ranks samples by holdSequence within the source
    // epoch), so it survives only in the pending read's buffer.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId: "thr_ref_a",
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the repository still shows the outgoing sample, so the
    // replacement's own wait has no copy outside the buffer.
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence).toBe(900);

    refused.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "2",
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get(ref)?.history?.failed !== undefined);
    // The diagnostic stands, the retired 900 is gone, and the replacement's own
    // wait survives because the retire ran before its frames folded in.
    const diagnostic = threadsStore.getState().threads.get(ref)?.history?.failed;
    expect(diagnostic).toBeDefined();
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence).toBe(1);
    expect(threadsStore.getState().threads.get(ref)?.delegates?.[0]?.holdSequence).toBe(1);
    // Supplying the replacement's hold must not have cost the diagnostic: the
    // same value captured above still stands once the hold has changed.
    expect(threadsStore.getState().threads.get(ref)?.history?.failed).toBe(diagnostic);
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.history?.failed).toBe(diagnostic);

    threadsStore.getState().releaseThread(ref);
  });

  test("a failed read that retired nothing does not refold a departed sample over the live one", async () => {
    // The mirror of the ordering case above, and the one where the buffer is
    // the LOSER. Boot 1's 901 is in the buffer when the repository accepts a
    // boot 2 history update live; that hand-off retires 901 and records boot 2
    // as the owner. Boot 2's own hold at sequence 1 then arrives and the
    // repository holds it as its current sample. When the pending read finally
    // fails, the failure's own boot 2 token confirms a replacement that has
    // ALREADY happened and retires nothing - so re-offering the stale buffered
    // 901 unconditionally restores the departed daemon's larger sequence over
    // boot 2's live sample, and boot 2's later updates lose to it.
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
      if (readCount === 1) return echoRead({ ...initial, bootGeneration: "1" }, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refused.reject = reject;
      });
    });
    await threadsStore.getState().ensureThread(ref);
    expect(existingEntryRoute(ref)?.repository).toBeDefined();

    // Boot 1's wait, published live: 901.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref, delegate: { ...heldDelegateRow(), holdSequence: 901 } },
    });
    await flushUntil(() => existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence === 901);

    // A bootless resync retires nothing and starts the tracked read, so the
    // buffer below belongs to this read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref, epoch: 2 },
    });
    await flushUntil(() => refused.reject !== null);

    // A buffered boot 1 frame that the successor read is carrying.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId: "thr_ref_a",
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 901, projectionRevision: 2 },
      },
    });
    await Promise.resolve();

    // Boot 2's history update is accepted LIVE by the repository. It confirms
    // the replacement, retires 901, and records boot 2 as the hold owner.
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref,
        bootGeneration: "2",
        epoch: 3,
        snapshot: { incarnation: "inc-1", length: 0 },
        turns: [],
        items: [],
      },
    });
    await flushUntil(() => existingEntryRoute(ref)?.repository?.getSnapshot()?.history?.holdOwner === "2");
    // Non-vacuous: the retirement really removed 901.
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence).toBeUndefined();

    // Boot 2 publishes its own wait at sequence 1: a LIVE sample the repository
    // holds, not a buffered one.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId: "thr_ref_a",
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    await flushUntil(() => existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence === 1);

    // The read fails, naming the boot that has already taken over.
    refused.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "2",
      }),
    );
    await flushUntil(() => threadsStore.getState().threads.get(ref)?.history?.failed !== undefined);

    // The replacement's own live sample stands. Without the gate this is 901:
    // the departed daemon's sequence restored over a wait that is still live.
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence).toBe(1);
    expect(threadsStore.getState().threads.get(ref)?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseThread(ref);
  });

  // The entry-backed half of the retired-source rule: the replay that follows a
  // structured failure must run the SAME neutralizer the watched path runs.
  test("entry-backed replay cannot let the retired source reclaim the hold owner", async () => {
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
    initial.bootGeneration = "5@root";
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

    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref, delegate: { ...heldDelegateRow(), holdSequence: 901 } },
    });
    await flushUntil(() => existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence === 901);

    // Bootless: retires nothing, starts the tracked read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId: "thr_ref_a", ref, epoch: 2 },
    });
    await flushUntil(() => refused.reject !== null);

    // Buffered while that read is pending: the OUTGOING root's history frame,
    // and then the replacement's live hold. The repository never accepted the
    // root's frame as recorded content.
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref,
        bootGeneration: "5@root",
        epoch: 3,
        snapshot: { incarnation: "inc-1", length: 0 },
        turns: [],
        items: [],
      },
    });
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: {
        threadId: "thr_ref_a",
        ref,
        delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 },
      },
    });
    await Promise.resolve();

    // The read fails naming the descendant's standalone daemon.
    refused.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "1",
      }),
    );
    await flushUntil(() => existingEntryRoute(ref)?.repository?.getSnapshot()?.history?.failed !== undefined);

    // Boot 1 speaks for itself. If the replayed "5@root" frame reclaimed the
    // owner, this frame reads as another replacement and erases the live hold.
    emitHistoryUpdatedFor(fake, ref, "thr_ref_a", {
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-2", length: 0 },
      turns: [],
      items: [],
    });
    await flushUntil(() => existingEntryRoute(ref)?.repository?.getSnapshot()?.history?.holdOwner === "1");
    expect(existingEntryRoute(ref)?.repository?.getSnapshot()?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseThread(ref);
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

  test("an older boot's failure does not strip the newer boot's buffered frame", async () => {
    // RoboRev 53429 (Medium 3): the buffer entry's own identity decides its
    // fate, not the failing read's. A watched read starts on boot 1 and buffers
    // boot 2's history frame and then boot 2's delegate sample; the read then
    // fails naming BOOT 1. Asking `confirmedBootReplacement(retiredBy, boot)`
    // answers "does the buffered frame supersede the failure's boot" - true
    // here - and blanked boot 2's identity, leaving boot 1's stale 900 in place
    // to outrank boot 2's sample even though the buffer identified the new
    // source. The comparison now runs the other way: only a frame whose own
    // boot the failure actually replaced folds as bootless.
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
      if (readCount === 1) return echoRead({ ...initial, bootGeneration: "1" }, params.requestGeneration);
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

    // Boot 1's own resync starts the read that will buffer boot 2's frames.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "1", epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    // Boot 2's history frame and its own delegate sample land in the buffer.
    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "2",
      epoch: 1,
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
    // Non-vacuous: boot 1's 900 is still what the published model shows, so
    // boot 2's sample exists only in the buffer.
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBe(900);

    // The pending read fails naming BOOT 1 — the source boot 2 replaced.
    refused[0]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "1",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The buffered boot-2 sample is boot 1's replacement, not boot 1's
    // property: it survives the replay...
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);
    // ...and boot 2 is recorded as the owner the buffer's frame named.
    expect(published?.history?.holdOwner).toBe("2");
    // The diagnostic still publishes.
    expect(published?.history?.failed).toBeDefined();

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a hold buffered before a confirmed-absence failure is not replayed back", async () => {
    // RoboRev 53429 (Medium 1): the sibling of the test above, one frame later
    // in the buffer's life. There the hold was already PUBLISHED when the
    // failure retired it. Here the delegate frame is still in the buffer -
    // it arrived after the bootless recovery resync started the read - so the
    // hand-off clears nothing on the base model and the replay folds the
    // buffered 900 straight back in. The wait stays visible after the daemon
    // that owned it exited.
    //
    // `daemonless` is trustworthy for this now: the hub distinguishes a
    // confirmed absence from `unknown-owner` (#6828), and a live enrichment
    // failure is stamped with the answering daemon's own token rather than
    // `daemonless`. So a failure naming an absence is evidence that no
    // delegate controller is sampling, and its buffer's hold samples are dead.
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
    const refusedHolder: { reject: ((error: Error) => void) | null } = { reject: null };
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return echoRead(initial, params.requestGeneration);
      return new Promise<ThreadReadResponse>((_resolve, reject) => {
        refusedHolder.reject = reject;
      });
    });

    await threadsStore.getState().watchThread(ref, { includeTurns: true });

    // The daemon exits: a bootless resync retires nothing and starts the read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refusedHolder.reject !== null);

    // The delegate's hold arrives while that read is pending, so it is the
    // buffer - not the published model - that carries the only copy of it.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: heldDelegateRow() },
    });
    await Promise.resolve();
    // Non-vacuous: the published model does not hold this sample.
    expect(threadsStore.getState().watchedThreads.get(ref)?.delegates?.[0]?.holdSequence).toBeUndefined();

    refusedHolder.reject?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: DAEMONLESS_BOOT_GENERATION,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The diagnostic publishes, and the buffered hold does not come back.
    expect(published?.history?.failed).toBeDefined();
    expect(published?.delegates?.[0]?.holdSequence).toBeUndefined();
    // The row itself survives: only the hold facet belonged to the dead source.
    expect(published?.delegates?.[0]?.delegateId).toBe("dlg_held");
    expect(published?.delegates?.[0]?.projectionRevision).toBe(3);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a confirmed replacement drops the outgoing source's buffered overlay, keeping the delegate row", async () => {
    // RoboRev 53429 (Medium 4): the OTHER half of what a supersession carries.
    // A boot-changing resync keeps every non-delegate frame from the superseded
    // read, so an overlay upsert the outgoing daemon buffered before its
    // replacement is replayed onto the replacement's model. If the successor
    // read then fails, that stream text - an unfinished message from the
    // departed daemon, never published before the hand-off - appears in the
    // replacement's transcript.
    //
    // An overlay frame is SOURCE-LOCAL: it describes a stream its producer was
    // writing and nothing about it outlives that producer. A delegate update is
    // DURABLE: only its HOLD is numbered by the departed source, so the row and
    // its terminal outcome stay. That is the split the carry rule makes.
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

    // Boot 1's resync starts the read that will buffer boot 1's overlay.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "1", epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    fake.emitNotification({
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "stream:outgoing",
          kind: "stream",
          streamId: "outgoing-stream",
          roundId: "round-outgoing",
          turnId: "turn_outgoing",
          item: { id: "outgoing-item", turnId: "turn_outgoing", type: "agentMessage", text: "never published" },
        },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the outgoing overlay is not on the published model, so the
    // buffer is the only place it exists.
    expect(threadsStore.getState().watchedThreads.get(ref)?.overlay?.["stream:outgoing"]).toBeUndefined();

    // Boot 2's resync CONFIRMS a replacement and supersedes the read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "2", epoch: 1 },
    });
    await flushUntil(() => refused.length === 2);
    const carried = threadsStore.getState().watchedThreads.get(ref);
    // Retiring the superseded read must not have published it either.
    expect(carried?.overlay?.["stream:outgoing"]).toBeUndefined();

    // The successor read fails structurally; replay must not resurrect it.
    refused[1]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "2",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    expect(published?.history?.failed).toBeDefined();
    // The departed source's stream text is gone with its producer...
    expect(published?.overlay?.["stream:outgoing"]).toBeUndefined();
    expect(published?.turns.flatMap((turn) => turn.items).some((item) => item.id === "outgoing-item")).toBe(false);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a failure under a later boot of the same root still drops the outgoing boot's overlay", async () => {
    // RoboRev 53685 (Medium 1): the replacement is a SUCCESSIVE BOOT OF THE
    // SAME ROOT. `5@root` and `6@root` share an owner id, so reducing the
    // tokens to their root made the replacement comparison answer false - the
    // hand-off accepted boot 6 while this filter still believed nothing had
    // been replaced, and replayed boot 5's unfinished output onto it.
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
    initial.bootGeneration = "5@root";
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
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    fake.emitNotification({
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "stream:outgoing",
          kind: "stream",
          streamId: "outgoing-stream",
          roundId: "round-outgoing",
          turnId: "turn_outgoing",
          item: { id: "outgoing-item", turnId: "turn_outgoing", type: "agentMessage", text: "never published" },
        },
      },
    });
    await Promise.resolve();
    expect(threadsStore.getState().watchedThreads.get(ref)?.overlay?.["stream:outgoing"]).toBeUndefined();

    refused[0]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "6@root",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    expect(published?.history?.failed).toBeDefined();
    expect(published?.overlay?.["stream:outgoing"]).toBeUndefined();
    expect(published?.turns.flatMap((turn) => turn.items).some((item) => item.id === "outgoing-item")).toBe(false);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("a supersession's confirmed hand-off keeps a carried frame from reclaiming ownership", async () => {
    // RoboRev 53688 (Medium 5). A watched read buffers `5@root`'s history
    // update; a NAMED resync from `1@other` supersedes the read and confirms a
    // replacement. The carry kept the outgoing frame's token whole, so when the
    // successor read then failed under `1@other` the replay saw a same-boot
    // failure (the resync already installed that owner) and the carried frame
    // restored `5@root` as owner. The replacement's own next frame then read as
    // another replacement and cleared the live hold it had published.
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
    initial.bootGeneration = "5@root";
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

    // Bootless: starts the read that will buffer the outgoing frame.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    emitHistoryUpdatedFor(fake, ref, threadId, {
      bootGeneration: "5@root",
      epoch: 3,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [],
      items: [],
    });
    await Promise.resolve();

    // A NAMED resync from a different root CONFIRMS the replacement and
    // supersedes the read. This is where the carry's hand-off happens.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, bootGeneration: "1@other", epoch: 1 },
    });
    await flushUntil(() => refused.length === 2);

    // The replacement publishes its live hold while the successor read pends.
    fake.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId, ref, delegate: { ...heldDelegateRow(), holdSequence: 1, projectionRevision: 4 } },
    });
    await Promise.resolve();

    // The successor fails under the SAME boot the resync installed.
    refused[1]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "1@other",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The successor owns the sequence. A carried `5@root` frame that reclaimed
    // it would make the next `1@other` frame read as another replacement.
    expect(published?.history?.holdOwner).toBe("1@other");
    // Non-vacuous: the replacement's own live sample survived.
    expect(published?.delegates?.[0]?.holdSequence).toBe(1);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("an overlay buffered under the outgoing boot is not replayed onto a live replacement", async () => {
    // RoboRev 53656 (Medium 3): the LIVE-REPLACEMENT sibling. A bootless
    // resync carries the buffer across, so boot 1's overlay upsert is still
    // there when the successor read fails naming boot 2. The base model hands
    // off to boot 2, but replay filtered overlays only for `daemonless`, so the
    // failed transcript displayed boot 1's unfinished output - work that was
    // never published before the hand-off and that boot 2 will not confirm.
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
    // Boot 1 served by its ROOT: qualified, so the failure below can name a
    // genuinely DIFFERENT owner rather than a second counter of the same one.
    initial.bootGeneration = "1@root";
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

    // BOOTLESS: starts the read, retires nothing, and keeps the buffer.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    fake.emitNotification({
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "stream:outgoing",
          kind: "stream",
          streamId: "outgoing-stream",
          roundId: "round-outgoing",
          turnId: "turn_outgoing",
          item: { id: "outgoing-item", turnId: "turn_outgoing", type: "agentMessage", text: "never published" },
        },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the buffer is the only place this overlay exists.
    expect(threadsStore.getState().watchedThreads.get(ref)?.overlay?.["stream:outgoing"]).toBeUndefined();

    // The successor read fails naming boot 2 - a CONFIRMED live replacement,
    // not an absence.
    refused[0]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "2@child",
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The diagnostic stands...
    expect(published?.history?.failed).toBeDefined();
    // ...and the departed source's stream text is gone with its producer.
    expect(published?.overlay?.["stream:outgoing"]).toBeUndefined();
    expect(published?.turns.flatMap((turn) => turn.items).some((item) => item.id === "outgoing-item")).toBe(false);

    threadsStore.getState().releaseWatchedThread(ref);
  });

  test("an overlay buffered before a confirmed-absence failure is not replayed back", async () => {
    // RoboRev 53574 (Medium 4): the CONFIRMED-ABSENCE sibling of the test
    // above. A named replacement already drops the outgoing overlay. A
    // `daemonless` failure is the other way a source stops existing: the hub
    // established that no daemon runs the thread, so nothing is streaming.
    // Replay stripped the buffered hold but still folded the buffered overlay,
    // so the transcript displayed unfinished output from a daemon the hub had
    // confirmed was gone.
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

    // The daemon exits: a BOOTLESS resync retires nothing and starts the read.
    fake.emitNotification({
      method: "evener/thread/resync",
      params: { threadId, ref, epoch: 2 },
    });
    await flushUntil(() => refused.length === 1);

    fake.emitNotification({
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "stream:outgoing",
          kind: "stream",
          streamId: "outgoing-stream",
          roundId: "round-outgoing",
          turnId: "turn_outgoing",
          item: { id: "outgoing-item", turnId: "turn_outgoing", type: "agentMessage", text: "never published" },
        },
      },
    });
    await Promise.resolve();
    // Non-vacuous: the outgoing overlay is not on the published model, so the
    // buffer is the only copy that replay could resurrect.
    expect(threadsStore.getState().watchedThreads.get(ref)?.overlay?.["stream:outgoing"]).toBeUndefined();

    refused[0]?.(
      new WireError("thread history failed at entry 7", -32603, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: DAEMONLESS_BOOT_GENERATION,
      }),
    );
    await flushUntil(() => threadsStore.getState().watchedThreads.get(ref)?.history?.failed !== undefined);
    const published = threadsStore.getState().watchedThreads.get(ref);
    // The diagnostic still publishes: only the departed source's frames go.
    expect(published?.history?.failed).toBeDefined();
    // ...and its stream text is gone with its producer.
    expect(published?.overlay?.["stream:outgoing"]).toBeUndefined();
    expect(published?.turns.flatMap((turn) => turn.items).some((item) => item.id === "outgoing-item")).toBe(false);

    threadsStore.getState().releaseWatchedThread(ref);
  });
});
