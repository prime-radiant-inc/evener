import {
  ALL_CAPABILITIES as CAPABILITIES,
  emitHistoryUpdated as emitHistoryUpdatedFor,
  type TestThreadOverrides,
  testThread,
} from "../threadTestUtils";
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  HistoryUpdatedParams,
  Thread,
  ThreadClearResponse,
  ThreadReadResponse,
} from "@evener/appwire-client";
import { acquireThreadSubscription, type ThreadSubscriptionLease } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { resetConnectionStoreForTests } from "./connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
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

function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
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
});
