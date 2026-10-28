import { ALL_CAPABILITIES as CAPABILITIES, type TestThreadOverrides, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import type { AnyNotification, ThreadReadResponse, ThreadStatus } from "@evener/appwire-client";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { connectionStore } from "./connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { resetThreadsStoreForTests, threadsStore } from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `notification-routing-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});
const connectFakeClient = () => connectReadyFakeClient((fake) => fake.on("thread/unsubscribe", () => ({})));

function readResponse(ref: string, overrides: TestThreadOverrides = {}, requestGeneration = 1): ThreadReadResponse {
  return {
    thread: testThread(ref, overrides),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration,
  };
}

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

async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

function markResponseCut(response: ThreadReadResponse, cut: { reached: boolean }): ThreadReadResponse {
  return {
    ...response,
    get olderCursor(): string | undefined {
      cut.reached = true;
      return response.olderCursor;
    },
  };
}

async function emitAtResponseCut(
  cut: { reached: boolean },
  ref: string,
  emit: () => void,
  published: () => boolean = () => threadsStore.getState().threads.has(ref),
): Promise<void> {
  for (let i = 0; i < 20 && !cut.reached; i += 1) await Promise.resolve();
  expect(cut.reached, "the response cut never ran: hydrateThread no longer reads olderCursor").toBe(true);
  expect(published(), "the snapshot published before the cut window was reached").toBe(false);
  emit();
}

describe("notification routing", () => {
  test("notifications for a never-tracked ref are dropped (same map reference preserved)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_tracked"));
    await threadsStore.getState().ensureThread("ref_tracked");

    const before = threadsStore.getState().threads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_untracked", ref: "ref_untracked", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads).toBe(before);
  });

  test("turn/completed is delivered only to the model whose activeTurnId matches (sibling immunity)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      const ref = (params as { ref: string }).ref;
      if (ref === "ref_a") {
        return versionedReadResponse("ref_a", {
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
          evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
        });
      }
      return versionedReadResponse("ref_b", {
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }],
        evener: { ref: "ref_b", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      });
    });

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");
    expect(threadsStore.getState().threads.get("ref_b")?.activeTurnId).toBe("turn_1");

    const beforeB = threadsStore.getState().threads.get("ref_b");

    // Stream A's own item BEFORE settling — wire-true: the real
    // turn/completed is a bare status/timing stamp with no items (every live
    // settle site in internal/appprojector/appwire_projection.go emits
    // Turn{ID,Status[,Error]} with Items nil, ItemsView "" — see
    // protocol/reducer.ts's "turn/completed" case), so A's item must already
    // be in the model via item/started + item/completed, not smuggled in
    // through the settle payload.
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          { ...{ type: "agentMessage", id: "item_a1", turnId: "turn_1", status: "inProgress" }, turnId: "turn_1" },
        ],
      },
    });
    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" },
            turnId: "turn_1",
          },
        ],
      },
    });

    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
      },
    });

    // The rightful owner (A, whose activeTurnId matched) settles...
    // activeTurnId is the read-only snapshot field (reducer.ts's model
    // comment) - a history/updated settling the turn does not clear it, only
    // a later thread/status/changed would - so the settled turn's own
    // persisted status is what a v6 model tracks "no longer running" through.
    const modelA = threadsStore.getState().threads.get("ref_a");
    expect(modelA?.turns[0]?.status).toBe("completed");
    expect(modelA?.turns[0]?.items[0]?.text).toBe("A's answer");

    // ...while B, simultaneously active on the same numbered turn_1, is a
    // same-reference no-op because v2's ref/thread identity is authoritative.
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
  });

  test("a session's startup announcements reach the tracked model even though their synthetic turn is never active", async () => {
    // The daemon bundles every SESSION_START-time announcement into one
    // synthetic prelude turn and sends it as turn/completed
    // (internal/appprojector/appwire_projection.go's systemAnnouncementItem).
    // That turn is never the model's activeTurnId, so an activeTurnId gate on
    // delivery drops the whole startup burst and the "N system events" group
    // materializes only on the next hydrate. Identity (ref/threadId) is what
    // decides delivery; where the frame lands inside the model is the
    // reducer's call.
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      versionedReadResponse("ref_a", {
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }],
        evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
      }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");

    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_system", status: "completed", itemsView: "full" }],
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
        ],
      },
    } as AnyNotification);

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_system", "turn_1"]);
    expect(model?.turns[0]?.items[0]?.id).toBe("item_plugin_loaded_1");
    // The real turn above it is still in flight.
    expect(model?.activeTurnId).toBe("turn_1");
  });
});

function readResponseWithId(ref: string, threadId: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  const base = readResponse(ref, overrides);
  return { ...base, thread: { ...base.thread, id: threadId } };
}

describe("notification routing index (ref / threadId fast path)", () => {
  test("a ref-routed notification reaches exactly the model with that ref (sibling and watched models untouched)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    await threadsStore.getState().watchThread("ref_w");

    const beforeB = threadsStore.getState().threads.get("ref_b");
    const beforeWatched = threadsStore.getState().watchedThreads.get("ref_w");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
    // Same-reference no-op for the sibling pane and the lean watch: both hold
    // distinct models the scan would not have selected.
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
    expect(threadsStore.getState().watchedThreads.get("ref_w")).toBe(beforeWatched);
  });

  test("a threadId-routed notification (no ref on the frame) reaches every model with that thread id, in both maps", async () => {
    const fake = connectFakeClient();
    // ref_primary and ref_alias hydrate from snapshots carrying the SAME
    // thread id thr_shared (distinct refs, one thread); ref_watched is a lean
    // watch of a third ref whose thread id is also thr_shared; ref_other is
    // an unrelated pane. A frame with only threadId = thr_shared must select
    // every model the old scan would have: primary, alias, and the watch —
    // and nothing else.
    fake.on("thread/read", (params) => {
      const ref = (params as { ref: string }).ref;
      if (ref === "ref_primary" || ref === "ref_alias") return readResponseWithId(ref, "thr_shared");
      if (ref === "ref_watched") return readResponseWithId(ref, "thr_shared");
      return readResponse(ref);
    });
    await threadsStore.getState().ensureThread("ref_primary");
    await threadsStore.getState().ensureThread("ref_alias");
    await threadsStore.getState().ensureThread("ref_other");
    await threadsStore.getState().watchThread("ref_watched");

    const beforeOther = threadsStore.getState().threads.get("ref_other");

    // thread/status/changed with threadId but NO ref: not wire-true for this
    // method (the hub always sends both), so cast like the suite's other
    // wire-shape probes. The routing layer must key on threadId alone.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_shared", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().threads.get("ref_primary")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().threads.get("ref_alias")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().watchedThreads.get("ref_watched")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().threads.get("ref_other")).toBe(beforeOther);
  });

  test("a ref-routed notification wins over a contradictory threadId (ref has precedence, like the scan)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a"); // thr_ref_a
    await threadsStore.getState().ensureThread("ref_b"); // thr_ref_b

    const beforeB = threadsStore.getState().threads.get("ref_b");

    // ref names ref_a but threadId names ref_b's thread: notificationTargetsThread
    // checks ref FIRST, so the scan selected only ref_a — the index must too.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_b", ref: "ref_a", status: { type: "active" } },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
  });

  test("a notification for an unknown ref changes nothing (no model, same map reference)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_w");

    const before = threadsStore.getState().threads;
    const beforeWatched = threadsStore.getState().watchedThreads;

    fake.emitNotification({
      method: "history/updated",
      params: {
        threadId: "thr_nowhere",
        ref: "ref_nowhere",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", text: "x", status: "inProgress" }],
      },
    });
    // threadId-only frame for an unknown id, too.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_nowhere", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().threads).toBe(before);
    expect(threadsStore.getState().watchedThreads).toBe(beforeWatched);
  });

  test("identity-free broadcast-style notifications match no model (same map reference, like the scan)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_w");

    const before = threadsStore.getState().threads;
    const beforeWatched = threadsStore.getState().watchedThreads;

    const broadcasts: AnyNotification[] = [
      { method: "evener/auth/updated", params: { provider: "p" } },
      { method: "evener/launch/updated", params: { cwd: "/tmp", layer: "test" } },
      { method: "evener/navigation/invalidated", params: { generationId: "g", sequence: 1, targets: [] } },
      { method: "evener/marketplace/updated", params: {} },
      { method: "evener/plugin/updated", params: {} },
      {
        method: "evener/settings/transcriptDisplay/changed",
        params: {
          layout: "compact",
          revision: 1,
          config: { version: 1, content: {}, advanced: {} },
        },
      } as AnyNotification,
    ];
    for (const n of broadcasts) fake.emitNotification(n);

    expect(threadsStore.getState().threads).toBe(before);
    expect(threadsStore.getState().watchedThreads).toBe(beforeWatched);
  });

  test("index survives release and re-watch of the same ref (watched map)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().watchThread("ref_w");
    threadsStore.getState().releaseWatchedThread("ref_w");
    expect(threadsStore.getState().watchedThreads.has("ref_w")).toBe(false);

    const before = threadsStore.getState().watchedThreads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_w", ref: "ref_w", status: { type: "active" } },
    });
    expect(threadsStore.getState().watchedThreads).toBe(before);

    await threadsStore.getState().watchThread("ref_w");
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_w", ref: "ref_w", status: { type: "active" } },
    });
    expect(threadsStore.getState().watchedThreads.get("ref_w")?.status).toEqual({ type: "active" });
  });

  test("a notification arriving mid-hydration for a pending ref is withheld from the stale model but buffered for replay", async () => {
    const fake = connectFakeClient();
    const cut = { reached: false };
    let resolveRead: ((response: ThreadReadResponse) => void) | null = null;
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve) => {
          resolveRead = resolve;
        }),
    );

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    await flushUntil(() => resolveRead !== null);
    await vi.waitFor(() => expect(resolveRead).not.toBeNull());
    const finishRead = resolveRead as unknown as (response: ThreadReadResponse) => void;
    finishRead(markResponseCut(readResponse("ref_a"), cut));
    // The pending hydration still owns ref_a: a live frame for it must be
    // buffered, and the (absent) stale model must not be resurrected.
    await emitAtResponseCut(cut, "ref_a", () =>
      fake.emitNotification({
        method: "thread/status/changed",
        params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
      }),
    );
    await ensuring;

    // The buffered frame replayed onto the published snapshot.
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("index follows model replacement on rehydration (resync publishes a fresh model under the same ref)", async () => {
    const fake = connectFakeClient();
    let status: ThreadStatus = { type: "idle" };
    fake.on("thread/read", (params) => readResponse("ref_a", { status }, params.requestGeneration));
    await threadsStore.getState().ensureThread("ref_a");

    // A targeted resync republishes a whole new model; routing must follow it.
    status = { type: "active" };
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    // hydrations counts the initial ensureThread publish too, so the resync
    // lands on 2 (and the second resync below on 3).
    await flushUntil(() => threadsStore.getState().hydrations.get("ref_a") === 2);
    await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("ref_a")).toBe(2));
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    status = { type: "idle" };
    fake.emitNotification({ method: "evener/thread/resync", params: { threadId: "thr_ref_a", ref: "ref_a" } });
    await flushUntil(() => threadsStore.getState().hydrations.get("ref_a") === 3);
    await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("ref_a")).toBe(3));
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "idle" });

    // And live routing still lands on the newest model.
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("index survives release and re-ensure of the same ref (no stale model left behind)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref, {}, params.requestGeneration));
    await threadsStore.getState().ensureThread("ref_a");
    const repository = productionEntryTranscriptForTests("ref_a").repository;
    expect(repository).toBeDefined();
    if (!repository) throw new Error("ordinary acquisition published no repository");
    const retire = vi.spyOn(repository, "retire");
    threadsStore.getState().releaseThread("ref_a");
    // R36: the finite final owner retains the indexed model at this same boundary.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(retire).toHaveBeenCalledTimes(1);

    // R36: routing remains live until retirement settles, not just until pane release.
    const before = threadsStore.getState().threads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads).not.toBe(before);
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    // Actual terminal retirement removes and de-indexes the released model.
    expect(await retire.mock.results[0]?.value).toEqual({ kind: "saved" });
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    const afterRetirement = threadsStore.getState().threads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads).toBe(afterRetirement);

    // Re-ensure republishes and the index must pick the ref up again.
    await threadsStore.getState().ensureThread("ref_a");
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });
});

describe("notification routing for out-of-catalog methods", () => {
  test("an unknown method carrying a matching ref still routes by that ref", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");

    const beforeB = threadsStore.getState().threads.get("ref_b");
    const before = threadsStore.getState().threads;

    // A method this build predates. Routing reads only the frame's own
    // ref/threadId (notificationRoutingKey is method-agnostic), so an
    // out-of-catalog frame routes exactly like a catalog frame: the
    // ref-routed model is selected, and no full-scan fallback exists to
    // behave differently. The reducer's default case returns the same
    // reference, so the pin here is selection-plus-no-side-effect: the
    // map is untouched for the sibling and the map reference is identical,
    // and a follow-up catalog frame still lands on the selected model.
    fake.emitUnknownNotification({ method: "totally/unknown", params: { ref: "ref_a" } });

    expect(threadsStore.getState().threads).toBe(before);
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });
});
