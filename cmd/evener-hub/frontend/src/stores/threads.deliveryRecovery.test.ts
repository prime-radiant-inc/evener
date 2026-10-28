import { ALL_CAPABILITIES as CAPABILITIES } from "../threadTestUtils";
import "fake-indexeddb/auto";
import { RequestTimeoutError, type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { cleanup, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  retryBlockedMutation,
  setMutationStorageForTests,
  threadsStore,
} from "./threads";
import {
  connectFakeClient,
  connectMutationClient,
  deferred,
  ensureActiveMutationTarget,
  flushIndexedDBUntil,
  mutationReceipt,
  nextHandledRequest,
  onTurnStart,
  readResponse,
  settleCallerContinuations,
  timeOutDurableEnqueues,
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

test.each(["active", "idle"])("manual retry stays blocked after reload of a saved %s delegate", async (status) => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "saved-manual-retry" });
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await storage.markUnknown(record.clientMutationId, "blockedUnknown");
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  const saved = readResponse("ref_a", { status: { type: status } });
  saved.thread.evener.mutationStateAuthoritative = false;
  fake.on("thread/read", () => saved);
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await settleCallerContinuations();
  expect(threadsStore.getState().restartBlockingObligations.size).toBe(0);
  expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
  expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
});

test.each(["active", "idle"].flatMap((status) => [true, false].map((accepted) => ({ status, accepted }))))(
  "periodic discovery resumes authority checks for saved $status delegates, accepted=$accepted",
  async ({ status, accepted }) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    // Per-case private IndexedDB factory, the same pattern this file uses for
    // its other directly driven storage cases. A fresh factory is its own
    // in-memory database namespace: this test awaits only the FIRST settle
    // call, so its reconciliation tail can still be running when the case ends,
    // and a shared (even uniquely named) database let that tail linger in the
    // worker's factory. A private factory means the tail cannot write into a
    // sibling case's records, a rerun cannot inherit a stale `delegate-periodic`
    // record (whose duplicate `add` would mask the real failure as a
    // ConstraintError), and no database is left behind in the shared global
    // factory that the suite's deleteMutationDatabase() never touches.
    const indexedDB = new IDBFactory();
    const databaseName = `evener-mutation-outbox-periodic-${status}-${accepted}`;
    try {
      const storage = new MutationOutboxIndexedDB({
        indexedDB,
        databaseName,
        createMutationId: () => "delegate-periodic",
      });
      const record = await storage.enqueueIntent({
        targetRef: "ref_a",
        method: "turn/queue",
        payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
        attachments: [],
        optimisticDisplay: { text: "sentinel" },
      });
      await storage.markAttempted(record.clientMutationId);
      const settled = deferred<void>();
      const settleApplied = storage.settleApplied.bind(storage);
      vi.spyOn(storage, "settleApplied").mockImplementation(async (...args) => {
        const result = await settleApplied(...args);
        if (args[0] === record.clientMutationId && result) settled.resolve();
        return result;
      });
      const settleReceipt = storage.settleReceipt.bind(storage);
      vi.spyOn(storage, "settleReceipt").mockImplementation(async (...args) => {
        const result = await settleReceipt(...args);
        if (args[0] === record.clientMutationId) settled.resolve();
        return result;
      });
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let recovered = false;
      fake.on("thread/read", () => {
        const response = readResponse("ref_a", { status: { type: status } });
        response.thread.evener.mutationStateAuthoritative = recovered;
        if (recovered && accepted) response.thread.evener.queue.clientMutationIds = [record.clientMutationId];
        return response;
      });
      fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      // ensureThread resolves on model publication; refreshThread also waits
      // for durable reconciliation before the single simulated recovery tick.
      await threadsStore.getState().refreshThread("ref_a");
      expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
      recovered = true;
      await vi.advanceTimersByTimeAsync(2000);
      await settled.promise;
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      expect(await storage.getOutbox(record.clientMutationId)).toBeUndefined();
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(accepted ? 0 : 1);
    } finally {
      vi.useRealTimers();
    }
  },
);

test.each(["daemonRestartRequired", "persistenceUnavailable"])(
  "blocked %s mutation invalidates earlier authority on the same connection",
  async (cause) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "authority-lost" });
      const blocked = deferred<void>();
      const markUnknown = storage.markUnknown.bind(storage);
      vi.spyOn(storage, "markUnknown").mockImplementation(async (...args) => {
        const result = await markUnknown(...args);
        blocked.resolve();
        return result;
      });
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      const fresh = deferred<ThreadReadResponse>();
      let reads = 0;
      fake.on("thread/read", () => {
        reads++;
        return reads === 1 ? readResponse("ref_a", { status: { type: "idle" } }) : fresh.promise;
      });
      fake.on("turn/queue", (params) => {
        throw new WireError("owner unavailable", -32014, {
          evenerErrorInfo: "mutationOutcome",
          clientMutationId: params.clientMutationId,
          mutationOutcome: "unknown",
          retryDisposition: "blocked",
          cause,
        });
      });
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      await threadsStore.getState().queue("ref_a", "preserve this message");
      await blocked.promise;
      await settleCallerContinuations();
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(false);
      expect(await retryBlockedMutation("authority-lost")).toBe(false);
      await vi.advanceTimersByTimeAsync(2000);
      await flushIndexedDBUntil(() => reads === 2);
      expect(reads).toBe(2);
      const response = readResponse("ref_a", { status: { type: "restartRequired" } });
      response.thread.evener.mutationStateAuthoritative = false;
      fresh.resolve(response);
      await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "restartRequired");
      expect((await storage.getOutbox("authority-lost"))?.state).toBe("blockedUnknown");
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  },
);

test.each([true, false])(
  "discovery reconciles another tab's blocked send with cached authority, accepted=%s",
  async (accepted) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "other-tab-blocked" });
    const otherTab = new MutationOutboxIndexedDB({ createMutationId: () => "other-tab-blocked" });
    const settled = deferred<void>();
    const settleApplied = storage.settleApplied.bind(storage);
    vi.spyOn(storage, "settleApplied").mockImplementation(async (...args) => {
      const result = await settleApplied(...args);
      settled.resolve();
      return result;
    });
    const settleReceipt = storage.settleReceipt.bind(storage);
    vi.spyOn(storage, "settleReceipt").mockImplementation(async (...args) => {
      const result = await settleReceipt(...args);
      settled.resolve();
      return result;
    });
    try {
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let reads = 0;
      fake.on("thread/read", () => {
        reads++;
        const response = readResponse("ref_a", { status: { type: "idle" } });
        if (reads > 1 && accepted)
          response.thread.evener.queue = { revision: 1, clientMutationIds: ["other-tab-blocked"] };
        return response;
      });
      let sends = 0;
      fake.on("turn/queue", (params) => {
        if (++sends === 1) throw new RequestTimeoutError("response lost");
        return { receipt: mutationReceipt(params.clientMutationId) };
      });
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      await threadsStore.getState().queue("ref_a", "sentinel");
      await flushIndexedDBUntil(() => sends === 1);
      await settleCallerContinuations();
      await otherTab.markUnknown("other-tab-blocked", "blockedUnknown");
      otherTab.close();
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      await vi.advanceTimersByTimeAsync(2000);
      await flushIndexedDBUntil(() => reads > 1);
      expect(reads).toBe(2);
      await flushIndexedDBUntil(() => accepted || sends === 2);
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(accepted ? 1 : 2);
      await settled.promise;
      expect(await storage.getOutbox("other-tab-blocked")).toBeUndefined();
    } finally {
      otherTab.close();
      vi.useRealTimers();
    }
  },
);

test.each(["accepted", "absent", "unavailable"])(
  "manual retry refreshes authority after another tab blocks a send: %s",
  async (outcome) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "manual-other-tab" });
    const otherTab = new MutationOutboxIndexedDB();
    const fresh = deferred<ThreadReadResponse>();
    try {
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let reads = 0;
      fake.on("thread/read", () =>
        ++reads === 1 ? readResponse("ref_a", { status: { type: "idle" } }) : fresh.promise,
      );
      let sends = 0;
      fake.on("turn/queue", (params) => {
        if (++sends === 1) throw new RequestTimeoutError("response lost");
        return { receipt: mutationReceipt(params.clientMutationId) };
      });
      fake.emitReady();
      await threadsStore.getState().ensureThread("ref_a");
      await threadsStore.getState().queue("ref_a", "sentinel");
      await flushIndexedDBUntil(() => sends === 1);
      await settleCallerContinuations();
      await otherTab.markUnknown("manual-other-tab", "blockedUnknown");
      otherTab.close();
      expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(true);
      const retry = retryBlockedMutation("manual-other-tab");
      await flushIndexedDBUntil(() => reads === 2);
      expect(reads).toBe(2);
      expect((await storage.getOutbox("manual-other-tab"))?.state).toBe("blockedUnknown");
      expect(sends).toBe(1);
      expect(await retryBlockedMutation("manual-other-tab")).toBe(false);
      const response = readResponse("ref_a", {
        status: { type: outcome === "unavailable" ? "restartRequired" : "idle" },
      });
      if (outcome === "accepted")
        response.thread.evener.queue = { revision: 1, clientMutationIds: ["manual-other-tab"] };
      if (outcome === "unavailable") response.thread.evener.mutationStateAuthoritative = false;
      fresh.resolve(response);
      expect(await retry).toBe(outcome !== "unavailable");
      if (outcome === "absent") await flushIndexedDBUntil(() => sends === 2);
      expect(sends).toBe(outcome === "absent" ? 2 : 1);
      if (outcome === "unavailable")
        expect((await storage.getOutbox("manual-other-tab"))?.state).toBe("blockedUnknown");
    } finally {
      fresh.resolve(readResponse("ref_a", { status: { type: "restartRequired" } }));
      otherTab.close();
      vi.useRealTimers();
    }
  },
);

for (const status of ["active", "idle"]) {
  test(`saved ${status} delegate snapshots do not release uncertain mutations`, async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "delegate-uncertain" });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let snapshot = readResponse("ref_a", { status: { type: "restartRequired" } });
    fake.on("thread/read", (params) => ({ ...snapshot, requestGeneration: params.requestGeneration }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    await storage.markAttempted(record.clientMutationId);
    snapshot = readResponse("ref_a", { status: { type: status } });
    Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false, kind: "subagent" });
    await threadsStore.getState().refreshThread("ref_a");
    expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    snapshot = readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 1, clientMutationIds: [record.clientMutationId] },
      },
    });
    await threadsStore.getState().refreshThread("ref_a");
    expect(await storage.getOutbox(record.clientMutationId)).toBeUndefined();
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  });
}

for (const state of ["blockedUnknown", "submitting"] as const) {
  test(`incompatible refresh preserves ${state} until a compatible snapshot arrives`, async () => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "upgrade-pending" });
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    await storage.markAttempted(record.clientMutationId);
    if (state === "blockedUnknown") await storage.markUnknown(record.clientMutationId, state);
    storage.close();
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().refreshThread("ref_a");
    const inspector = new MutationOutboxIndexedDB();
    expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
    expect((await inspector.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    status = "notLoaded";
    await threadsStore.getState().refreshThread("ref_a");
    expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
    expect((await inspector.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    status = "idle";
    await threadsStore.getState().refreshThread("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    inspector.close();
  });
}

test("compatible refresh wins over a delayed incompatible receipt write", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "delayed-upgrade" });
  const record = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await storage.markAttempted(record.clientMutationId);
  const writeStarted = deferred<void>();
  const releaseWrite = deferred<void>();
  const markUnknown = storage.markUnknown.bind(storage);
  vi.spyOn(storage, "markUnknown").mockImplementation(async (id, state) => {
    writeStarted.resolve();
    await releaseWrite.promise;
    return markUnknown(id, state);
  });
  const settled = deferred<void>();
  const settleReceipt = storage.settleReceipt.bind(storage);
  vi.spyOn(storage, "settleReceipt").mockImplementation(async (id, projectionState) => {
    const result = await settleReceipt(id, projectionState);
    if (id === record.clientMutationId) settled.resolve();
    return result;
  });
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  let status = "restartRequired";
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
  const receipt = deferred<{ receipt: ReturnType<typeof mutationReceipt> }>();
  fake.on("turn/queue", () => receipt.promise);
  fake.emitReady();
  const firstRead = threadsStore.getState().ensureThread("ref_a");
  await writeStarted.promise;
  status = "idle";
  const refresh = threadsStore.getState().refreshThread("ref_a");
  await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "idle");
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
  releaseWrite.resolve();
  await Promise.all([firstRead, refresh]);
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
  expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("submitting");
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  receipt.resolve({ receipt: mutationReceipt(record.clientMutationId) });
  await settled.promise;
});

test.each([false, true])(
  "stopped refresh retains an overlapping blocking obligation (reconnect=%s)",
  async (reconnect) => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "overlapping-stop" });
    const record = await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    await storage.markAttempted(record.clientMutationId);
    const scanStarted = deferred<void>();
    const releaseScan = deferred<void>();
    const listOutbox = storage.listOutbox.bind(storage);
    let held = false;
    vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
      const records = await listOutbox(ref);
      if (ref && !held && threadsStore.getState().threads.get(ref)?.status.type === "restartRequired") {
        held = true;
        scanStarted.resolve();
        await releaseScan.promise;
      }
      return records;
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    const firstRead = threadsStore.getState().ensureThread("ref_a");
    await scanStarted.promise;
    status = "notLoaded";
    if (reconnect) {
      fake.emitStateChange("reconnecting");
      fake.emitReady();
    }
    const refresh = threadsStore.getState().refreshThread("ref_a");
    await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "notLoaded");
    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("notLoaded");
    releaseScan.resolve();
    await Promise.all([firstRead, refresh]);
    expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    status = "idle";
    await threadsStore.getState().refreshThread("ref_a");
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  },
);

for (const failure of ["listOutbox", "markUnknown"] as const) {
  test.each([false, true])(
    `restart blocking survives ${failure} failure and stopped refresh (overlap=%s)`,
    async (overlap) => {
      const storage = new MutationOutboxIndexedDB({ createMutationId: () => "failed-restart-block" });
      const record = await storage.enqueueIntent({
        targetRef: "ref_a",
        method: "turn/queue",
        payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
        attachments: [],
        optimisticDisplay: { text: "sentinel" },
      });
      await storage.markAttempted(record.clientMutationId);
      const blockingStarted = deferred<void>();
      const releaseFailure = deferred<void>();
      let faultEnabled = true;
      let held = false;
      const failStorage = async () => {
        if (!held) {
          held = true;
          blockingStarted.resolve();
          await releaseFailure.promise;
        }
        throw new DOMException("IndexedDB transaction aborted", "AbortError");
      };
      if (failure === "listOutbox") {
        const listOutbox = storage.listOutbox.bind(storage);
        vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
          if (
            faultEnabled &&
            ref &&
            (held || threadsStore.getState().threads.get(ref)?.status.type === "restartRequired")
          )
            await failStorage();
          return listOutbox(ref);
        });
      } else {
        const markUnknown = storage.markUnknown.bind(storage);
        vi.spyOn(storage, "markUnknown").mockImplementation(async (id, state) => {
          if (faultEnabled && state === "blockedUnknown") await failStorage();
          return markUnknown(id, state);
        });
      }
      setMutationStorageForTests(storage);
      const fake = connectFakeClient("connecting");
      let status = "restartRequired";
      fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
      fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
      fake.emitReady();
      const firstRead = threadsStore
        .getState()
        .ensureThread("ref_a")
        .catch(() => undefined);
      await blockingStarted.promise;
      let stopped: Promise<unknown> | undefined;
      if (overlap) {
        status = "notLoaded";
        stopped = threadsStore
          .getState()
          .refreshThread("ref_a")
          .catch(() => undefined);
        await flushIndexedDBUntil(() => threadsStore.getState().threads.get("ref_a")?.status.type === "notLoaded");
      }
      releaseFailure.resolve();
      await firstRead;
      status = "notLoaded";
      await (stopped ??
        threadsStore
          .getState()
          .refreshThread("ref_a")
          .catch(() => undefined));
      expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("submitting");
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
      faultEnabled = false;
      await threadsStore.getState().refreshThread("ref_a");
      expect((await storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
      status = "idle";
      await threadsStore.getState().refreshThread("ref_a");
      await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
      expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    },
  );
}

test("periodic discovery recovers failed compatible reconciliation after storage returns", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "recovery-after-storage" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    let faultEnabled = false;
    let failures = 0;
    const listOutbox = storage.listOutbox.bind(storage);
    vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
      if (
        faultEnabled &&
        ref &&
        (failures > 0 || threadsStore.getState().threads.get(ref)?.status.type === "restartRequired")
      ) {
        failures += 1;
        throw new DOMException("IndexedDB transaction aborted", "AbortError");
      }
      return listOutbox(ref);
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    faultEnabled = true;
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(failures).toBeGreaterThan(0);
    status = "idle";
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe("idle");
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
    expect(failures).toBeGreaterThan(1);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
    faultEnabled = false;
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
  } finally {
    vi.useRealTimers();
  }
});

test("periodic discovery recovers reconciliation after the final durable record settles", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "last-record" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    let faultEnabled = false;
    const listOutbox = storage.listOutbox.bind(storage);
    vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
      const records = await listOutbox(ref);
      if (faultEnabled && ref && records.length === 0)
        throw new DOMException("IndexedDB transaction aborted", "AbortError");
      return records;
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let compatible = false;
    fake.on("thread/read", () =>
      readResponse("ref_a", {
        status: { type: compatible ? "idle" : "restartRequired" },
        evener: {
          ref: "ref_a",
          capabilities: CAPABILITIES,
          queue: { revision: 1, clientMutationIds: compatible ? ["last-record"] : [] },
        },
      }),
    );
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    compatible = true;
    faultEnabled = true;
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(await storage.listTargetRefs()).toEqual([]);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
    faultEnabled = false;
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => !threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a"));
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);
  } finally {
    vi.useRealTimers();
  }
});

test.each(["active", "idle"])(
  "recovered %s descendant without uncertain messages permits new sends",
  async (status) => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "fresh-descendant-send" });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let snapshot = readResponse("ref_a", { status: { type: "restartRequired" } });
    fake.on("thread/read", (params) => ({ ...snapshot, requestGeneration: params.requestGeneration }));
    const delivered = deferred<void>();
    fake.on("turn/queue", (params) => {
      delivered.resolve();
      return { receipt: mutationReceipt(params.clientMutationId) };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    snapshot = readResponse("ref_a", { status: { type: status } });
    Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false, kind: "subagent" });
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(false);
    await threadsStore.getState().queue("ref_a", "new message after restart");
    await delivered.promise;
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  },
);

test.each(["active", "idle"])(
  "recovered %s descendant with a never-attempted intent permits delivery",
  async (status) => {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "unsent-descendant-send" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "unsent" }] },
      attachments: [],
      optimisticDisplay: { text: "unsent" },
    });
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let snapshot = readResponse("ref_a", { status: { type: "restartRequired" } });
    fake.on("thread/read", (params) => ({ ...snapshot, requestGeneration: params.requestGeneration }));
    const delivered = deferred<void>();
    fake.on("turn/queue", (params) => {
      delivered.resolve();
      return { receipt: mutationReceipt(params.clientMutationId) };
    });
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(true);
    snapshot = readResponse("ref_a", { status: { type: status } });
    Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false, kind: "subagent" });
    await threadsStore.getState().refreshThread("ref_a");
    expect(threadsStore.getState().restartBlockingObligations.has("ref_a")).toBe(false);
    expect(threadsStore.getState().mutationAuthorityRefs.has("ref_a")).toBe(false);

    await delivered.promise;
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
  },
);

// A storage failure on the outbox's read path: while armed, every per-ref
// listOutbox read rejects - by default with the adapter's watchdog error, the
// same shape a never-answering IndexedDB open produces. The bound real read
// is returned so a test can still inspect rows while the fault is armed.
function wedgeOutboxReads(
  storage: MutationOutboxIndexedDB,
  storageError: () => unknown = () => new MutationStorageTimeoutError(),
) {
  let armed = false;
  const readOutbox = storage.listOutbox.bind(storage);
  vi.spyOn(storage, "listOutbox").mockImplementation(async (ref) => {
    if (armed && ref) throw storageError();
    return readOutbox(ref);
  });
  return {
    arm: () => {
      armed = true;
    },
    lift: () => {
      armed = false;
    },
    readOutbox,
  };
}

// A full origin is not a wedge: IndexedDB refuses the write at once with
// QuotaExceededError. The outbox is local persistence only, so the send still
// goes out as a plain RPC, exactly as it does through a wedge; the refusal is
// final, so the ladder does not retry it. Nothing durable pins the ref.
test("a send whose durable write the browser refuses for quota goes out directly and pins nothing", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  onTurnStart(fake);
  const unsubscribed = nextHandledRequest(fake, "thread/unsubscribe", () => ({}));
  const add = vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
    throw new DOMException("storage full", "QuotaExceededError");
  });
  await threadsStore.getState().send("ref_a", "sent from a full origin");
  expect(add).toHaveBeenCalledTimes(1);
  const sent = fake.calls.filter((call) => call.method === "turn/start");
  expect(sent, "the send from a full origin never reached the daemon").toHaveLength(1);
  expect((sent[0]?.params as { clientMutationId?: string } | undefined)?.clientMutationId).toBeTruthy();
  expect(await storage.listOutbox()).toEqual([]);
  threadsStore.getState().releaseThread("ref_a");
  await waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  await unsubscribed;
});

// Behind an undelivered durable send the fallback refuses rather than jump
// it, and only that earlier durable row keeps the ref pinned.
test("a refused write behind an undelivered durable send fails and preserves only the durable pin", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));
  await threadsStore.getState().send("ref_a", "already saved");
  vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
    throw new DOMException("storage full", "QuotaExceededError");
  });
  await expect(threadsStore.getState().send("ref_a", "not saved")).rejects.toThrow("storage full");
  expect(await storage.listOutbox()).toHaveLength(1);
  threadsStore.getState().releaseThread("ref_a");
  expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
});

// A reconciliation that storage unavailability fails is not a mutation-state
// fact: the same wedge that fences the durable write also fails the reconcile
// read. Recording it as a genuine failure would fence the direct-dispatch
// fallback and strand every send behind a page reload, so a storage-unavailable
// reconcile records the ref as storage-blocked - the slice the send fallback
// may admit - while every other failure keeps its old, fencing meaning. A
// retired connection fails the read at once instead of timing out, and is
// just as much a storage failure.
test.each([
  { failure: "a storage timeout", storageError: () => new MutationStorageTimeoutError() },
  { failure: "a retired connection", storageError: () => new DOMException("closing", "InvalidStateError") },
  { failure: "an aborted transaction", storageError: () => new DOMException("aborted", "AbortError") },
])("a reconciliation that $failure fails records the ref as storage-blocked, not failed", async ({ storageError }) => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "storage-blocked-reconcile" });
  await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  const wedge = wedgeOutboxReads(storage, storageError);
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await settleCallerContinuations();
  wedge.arm();
  await threadsStore
    .getState()
    .refreshThread("ref_a")
    .catch(() => undefined);
  expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
  expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(false);
});

// A genuine reconcile failure must survive a later storage-timeout reconcile:
// the timeout adds the storage-blocked record but does not supersede the real
// failure, so the failures fence - and the fallback refusal it carries - hold
// through the wedge until a successful reconcile clears both.
test("a storage-timeout reconcile does not erase a genuine reconciliation failure", async () => {
  const storage = new MutationOutboxIndexedDB();
  const wedge = wedgeOutboxReads(storage);
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await ensureActiveMutationTarget(fake, "ref_a");
  onTurnStart(fake);

  // The genuine failure: a blocked shared record or a real reconcile conflict.
  threadsStore.setState({ mutationReconciliationFailures: new Set(["ref_a"]) });
  wedge.arm();
  await threadsStore
    .getState()
    .refreshThread("ref_a")
    .catch(() => undefined);
  expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
  expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(true);

  timeOutDurableEnqueues(storage);
  await expect(threadsStore.getState().send("ref_a", "sent while both fences hold")).rejects.toBeInstanceOf(
    MutationStorageTimeoutError,
  );
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

// The storage-blocked fence must self-heal, not stick: discovery retries the
// ref on every pass, and once storage answers the reconcile succeeds, the
// slice clears, and the durable dispatcher resumes the ref's queued row - so a
// transient wedge costs a wedge-long dispatch pause, not a reload. The
// sentinel row parks behind the restart fence while the wedge is armed, so
// the healthy hydrate can never race its dispatch out first.
test("periodic discovery recovers a storage-blocked reconciliation after storage returns", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "storage-blocked-recovery" });
    await storage.enqueueIntent({
      targetRef: "ref_a",
      method: "turn/queue",
      payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
      attachments: [],
      optimisticDisplay: { text: "sentinel" },
    });
    const wedge = wedgeOutboxReads(storage);
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    let status = "restartRequired";
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: status } }));
    fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
    fake.emitReady();
    // Hydrate behind the restart fence: the healthy reconcile runs, but the
    // sentinel row stays parked, so no healthy window can dispatch it before
    // the wedge lands.
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    wedge.arm();
    status = "idle";
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);
    // The sentinel row is still parked: nothing left while the reconcile was
    // wedged.
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(0);

    wedge.lift();
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/queue"));
    expect(fake.calls.filter((call) => call.method === "turn/queue")).toHaveLength(1);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
    expect(threadsStore.getState().mutationReconciliationFailures.has("ref_a")).toBe(false);
  } finally {
    vi.useRealTimers();
  }
});

// The union clause: a storage-blocked ref with NO durable row is invisible to
// every row-driven discovery trigger, so only the storage-blocked slice's own
// membership in the discovery pass can retry its reconcile. Without that
// clause the slice would stick until a manual refresh or a reload.
test("periodic discovery retries a storage-blocked ref that has no durable row", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const storage = new MutationOutboxIndexedDB();
    const wedge = wedgeOutboxReads(storage);
    setMutationStorageForTests(storage);
    const fake = connectFakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
    fake.emitReady();
    await threadsStore.getState().ensureThread("ref_a");
    await settleCallerContinuations();
    wedge.arm();
    await threadsStore
      .getState()
      .refreshThread("ref_a")
      .catch(() => undefined);
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);

    wedge.lift();
    await vi.advanceTimersByTimeAsync(2000);
    await flushIndexedDBUntil(() => !threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a"));
    expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(false);
  } finally {
    vi.useRealTimers();
  }
});

// The record-level gate: a storage-blocked ref cannot answer the ordering
// questions a Retry press asks, so the press refuses and the canceled row
// stays put until discovery's retry reconciles. The press is otherwise
// admissible here - idle, authoritative, nothing pending - so the refusal
// isolates the storage-blocked clause.
test("a storage-blocked ref refuses a queued record's Retry press", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "evener-mutation-outbox-storage-blocked-retry";
  const seeder = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const record = await seeder.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text: "sentinel" }] },
    attachments: [],
    optimisticDisplay: { text: "sentinel" },
  });
  await seeder.cancelUnattempted("ref_a");
  seeder.close();

  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  const wedge = wedgeOutboxReads(storage);
  setMutationStorageForTests(storage);
  const fake = connectFakeClient("connecting");
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "idle" } }));
  fake.on("turn/queue", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  fake.emitReady();
  await threadsStore.getState().ensureThread("ref_a");
  await settleCallerContinuations();

  wedge.arm();
  await threadsStore
    .getState()
    .refreshThread("ref_a")
    .catch(() => undefined);
  expect(threadsStore.getState().mutationReconciliationStorageBlocked.has("ref_a")).toBe(true);

  expect(await retryBlockedMutation(record.clientMutationId)).toBe(false);
  // The row is untouched: still canceled, and nothing reached the daemon.
  expect((await wedge.readOutbox("ref_a")).map((row) => row.state)).toEqual(["canceled"]);
  expect(fake.calls.filter((call) => call.method === "turn/queue")).toEqual([]);
});
