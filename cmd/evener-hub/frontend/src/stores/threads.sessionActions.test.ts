import { appliedReceipt } from "../mutationReceiptTestUtils";
import { ALL_CAPABILITIES as CAPABILITIES, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import {
  type AnyNotification,
  hydrateThread,
  type NotesHumanSetResponse,
  RequestTimeoutError,
  type ThreadReadResponse,
  WireError,
} from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { editHumanNote, syncHumanNote, useHumanNoteDraft } from "./humanNoteDrafts";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  installHydrationRetrySchedulerForTests,
  readMutationPersistence,
  resetThreadsStoreForTests,
  retryBlockedMutation,
  setMutationStorageForTests,
  threadsStore,
} from "./threads";
import { ConflictError } from "./threadsErrors";
import {
  clearResponse,
  connectFakeClient,
  connectMutationClient,
  deferred,
  ensureActiveMutationTarget,
  flushIndexedDBUntil,
  flushUntil,
  mutationReceipt,
  nextHandledRequest,
  readResponse,
  settleCallerContinuations,
} from "./threadsSessionActionsTestUtils";

interface ScheduledHydrationRetry {
  attempt: number;
  retry: () => void;
  cancelled: boolean;
}

let scheduledHydrationRetries: ScheduledHydrationRetry[] = [];

let restoreHydrationRetryScheduler: (() => void) | null = null;

let cache: SessionEntryCache;

beforeEach(async () => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  resetWorkspaceStoreForTests();
  resetSubagentModuleStoreForTests();
  scheduledHydrationRetries = [];
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

async function deleteMutationDatabase(): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase("evener-mutation-outbox");
    request.addEventListener("success", () => resolve(), { once: true });
    request.addEventListener("error", () => reject(request.error), { once: true });
    request.addEventListener("blocked", () => reject(new Error("mutation database deletion blocked")), {
      once: true,
    });
  });
}

function runScheduledHydrationRetry(index = 0): void {
  const scheduled = scheduledHydrationRetries[index];
  expect(scheduled, `no hydration retry scheduled at index ${index}`).toBeDefined();
  expect(scheduled?.cancelled).toBe(false);
  scheduled?.retry();
}

describe("useThreadsStore session actions (setModel/setReasoningEffort/setGoal/rename/compact/shutdown/clearThread/forkFromTurn)", () => {
  test("setModel sends thread/model/set with {ref, modelProvider, model}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/model/set", () => ({}));

    await threadsStore.getState().setModel("ref_a", "anthropic", "claude-opus-4-1");

    const call = fake.calls.find((c) => c.method === "thread/model/set");
    expect(call?.params).toEqual({ ref: "ref_a", modelProvider: "anthropic", model: "claude-opus-4-1" });
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

  // One representative Conflict-mapping test standing in for every
  // thread-level action above - each wraps its client.request in the exact
  // same mapConflict try/catch as send/steer/queue/interrupt (proven
  // exhaustively above); repeating it per method would test the identical
  // wrapper code path over and over rather than add real coverage.
  test("session actions also map a Conflict rejection to ConflictError (setModel as the representative case)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/model/set", () => {
      throw new WireError("model unavailable", -32013, { evenerErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toBeInstanceOf(ConflictError);
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
  test("setModel rejects synchronously while reconnecting, unlike the read-only actions (representative direct-request mutation)", async () => {
    const fake = connectFakeClient();
    fake.emitStateChange("reconnecting");

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toThrow(
      /cannot call "thread\/model\/set" while state is "reconnecting"/,
    );
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
  // consumed (issue #1704), so the client settles those optimistic turn/queue
  // records by positive evidence -- never by inferring consumption from
  // sequence order.
  test("a queueChanged naming consumed ids settles the matching optimistic queue record", async () => {
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

    let settled = false;
    for (let attempt = 0; attempt < 20 && !settled; attempt += 1) {
      settled = (await inspector.getOptimistic(record.clientMutationId)) === undefined;
    }
    expect(settled).toBe(true);
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
