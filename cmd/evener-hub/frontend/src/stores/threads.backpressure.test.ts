import "fake-indexeddb/auto";
import { type TurnStartResponse, WireError } from "@evener/appwire-client";
import { cleanup } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import {
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
} from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { settleProjectionWorkForTests } from "./projectionWork";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  installBackpressureRetrySchedulerForTests,
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "./threads";
import {
  connectMutationClient,
  deferred,
  flushIndexedDBUntil,
  mutationReceipt,
  readResponse,
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

// The fixture's refusal message: one constant shared by every fake throw
// and the passthrough assertion, so no assertion matches a duplicated
// string (copy is not a test oracle).
const HUB_REFUSAL_MESSAGE = "the hub's inbound queue is busy and did not accept this message";

// #4342: the hub's high-water backpressure refusal is transient. The row
// stays queued in the outbox FIFO (never recovery), the pending surface
// shows the hub did not accept it yet — the busy marker holds until the row
// settles and a fresh refusal re-marks it at the next pace step — and the store paces
// retries through the injectable backoff scheduler, one arm per refusal,
// doubling while the hub keeps refusing. A retry the hub accepts settles
// the row and leaves nothing marked.
test("a backpressured send stays queued, surfaces busy, and paces retries that redeliver", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  let refuse = true;
  fake.on("turn/start", (params) => {
    if (refuse) {
      throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
        evenerErrorInfo: "connectionBackpressure",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
        retryDisposition: "automatic",
        cause: "queueSaturated",
      });
    }
    return {
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
      receipt: mutationReceipt(params.clientMutationId),
    };
  });
  const scheduled: Array<{ attempt: number; retry: () => void }> = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt, retry) => {
    scheduled.push({ attempt, retry });
    return () => {};
  });

  await threadsStore.getState().send("ref_a", "held by backpressure");
  // The rollback lands one storage read after the wire call (the refusal
  // path's pre-rollback state read), so wait for the marker the
  // assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [refused] = await storage.listOutbox("ref_a");
  if (!refused) throw new Error("expected the refused row to stay durable");
  expect(refused.state).toBe("submitting");
  expect(refused.attempted).toBe(false);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(refused.clientMutationId);
  expect(scheduled.map((entry) => entry.attempt)).toEqual([1]);

  // Firing the retry while the hub still refuses: the marker holds through
  // the attempt in flight, and the refusal that answers it re-marks at a
  // doubled pace.
  const firstRetry = scheduled[0];
  if (!firstRetry) throw new Error("expected the first paced retry to be armed");
  firstRetry.retry();
  await flushIndexedDBUntil(
    () => fake.calls.filter((call) => call.method === "turn/start").length === 2 && scheduled.length === 2,
  );
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(refused.clientMutationId);
  expect(scheduled.map((entry) => entry.attempt)).toEqual([1, 2]);

  // A retry the hub accepts settles the row and clears the marker.
  refuse = false;
  const secondRetry = scheduled[1];
  if (!secondRetry) throw new Error("expected the doubled paced retry to be armed");
  secondRetry.retry();
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 3);
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a")).length === 0);
  expect(await storage.listOutbox("ref_a")).toEqual([]);
  expect(threadsStore.getState().transientRefusalPaces.size).toBe(0);
  restoreScheduler();
});

// #4342 (round-9 review): the refusal's own storage notification refreshes
// the ref's projection, and that refresh's newly-loaded hand-off must not
// wake the dispatcher - the notification fires once per refusal, so an
// unfiltered hand-off redelivers the refused row immediately and each
// refusal repeats the wake in a tight loop, hammering the saturated
// connection and perpetually replacing the pace timer. The paced retry and
// the ordinary discovery drive own the re-drive: with the scheduler withheld
// and nothing else driving, a fully drained runtime leaves exactly one
// request on the wire.
test("a refusal's storage notification does not itself redispatch the refused row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "refused under saturation");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));

  // Drain every storage and projection async the refusal kicked off: the
  // rollback's notification, the commit feed's projection refresh, the
  // marker prune. Each round is bounded so a busy loop cannot hang the
  // drain itself; any quiet gap lets one settle through.
  for (let round = 0; round < 3; round++) {
    await Promise.race([settleProjectionWorkForTests(), new Promise((resolve) => setTimeout(resolve, 100))]);
  }

  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt).toBe(1);
  restoreScheduler();
});

// #4342 (round-12 review): a projection refresh from any source - the
// discovery cadence, a commit, the reopened row's own notification-driven
// refresh - completing while a refused row's paced-retry timer is live
// must not hand readiness to the dispatcher: that timer owns the re-drive,
// and its fire schedules the same dispatch the hand-off would. The gate
// marker, so it cannot outlive what backs it.
test("a projection refresh during a live paced retry does not redispatch the refused row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "refused, paced, and refreshed");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt).toBe(1);

  // A projection read from outside the refusal's own path - the same read
  // the discovery cadence and every commit run - while the pace is live
  // (the withheld scheduler keeps the timer armed, as an in-window read
  // would find it). The read completes, the projection is true, and its
  // readiness hand-off must still hand nothing to the dispatcher.
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect((await storage.listOutbox("ref_a"))[0]?.state).toBe("submitting");
  restoreScheduler();
});

// #4342 (round-11 review): a refusal that rolls back to residual evidence -
// the two-tab case the count exists for: another tab's concurrent attempt
// marked the row while this attempt was in flight - keeps the row submitting
// under its uncertain treatment: no busy marker, no paced timer, the
// ordinary discovery cadence as its only re-drive. That refusal's
// notification must not redispatch the row either: the rollback changed
// only attempt evidence, which no queue projection carries, so a projection
// refresh would read an identical queue and hand readiness straight back
// to the dispatcher - the same tight loop the clean refusal ran before
// round 10.
test("a refusal rolling back to residual evidence does not redispatch the row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  let firstRefused = false;
  const inFlight = deferred<TurnStartResponse>();
  fake.on("turn/start", (params) => {
    if (!firstRefused) return inFlight.promise;
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "refused with a concurrent attempt aboard");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the in-flight attempt's row to stay durable");

  // The other tab's concurrent attempt, committed while this attempt holds
  // the wire: the refusal rolls back only its own, leaving the row with
  // residual evidence, no busy marker, and no timer - the uncertain
  // treatment.
  await expect(storage.markAttempted(row.clientMutationId)).resolves.toBe(true);
  firstRefused = true;
  inFlight.reject(
    new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: row.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    }),
  );
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a"))[0]?.unresolvedAttempts === 1);
  const [residual] = await storage.listOutbox("ref_a");
  if (!residual) throw new Error("expected the refused row to stay durable");
  expect(residual.state).toBe("submitting");
  expect(residual.unresolvedAttempts).toBe(1);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")).toBeUndefined();

  // The same bounded drain as the clean-refusal test: nothing may redispatch
  // the row before the ordinary discovery cadence gets its turn.
  for (let round = 0; round < 3; round++) {
    await Promise.race([settleProjectionWorkForTests(), new Promise((resolve) => setTimeout(resolve, 100))]);
  }

  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  restoreScheduler();
});

// #4342: the dispatcher's attempted-marker rollback is what keeps a
// refused row out of the browser's own reconnect reconcile park — the
// non-authoritative first read marks submitting+attempted rows
// blockedUnknown, and a parked row stops dispatching. A refused row
// (its marker cleared by the refusal itself) survives the scan still
// submitting, and the ordinary ready drive redelivers it.
test("a backpressured send survives the non-authoritative reconcile park and redelivers", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  let snapshot = readResponse("ref_a");
  fake.on("thread/read", () => snapshot);
  let refuse = true;
  fake.on("turn/start", (params) => {
    if (!refuse) {
      return {
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: mutationReceipt(params.clientMutationId),
      };
    }
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const paced: Array<() => void> = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((_attempt, retry) => {
    paced.push(retry);
    return () => {};
  });

  await threadsStore.getState().ensureThread("ref_a");
  await threadsStore.getState().send("ref_a", "held across a reconnect");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [refused] = await storage.listOutbox("ref_a");
  if (!refused) throw new Error("expected the refused row to stay durable");
  expect(refused.state).toBe("submitting");

  // A reconnect whose first read is not mutation-authoritative runs the
  // reconcile's onlyAttempted park over the ref's submitting rows.
  snapshot = readResponse("ref_a");
  Object.assign(snapshot.thread.evener, { mutationStateAuthoritative: false });
  await threadsStore.getState().refreshThread("ref_a");
  const [survived] = await storage.listOutbox("ref_a");
  if (!survived) throw new Error("expected the refused row to survive the reconcile");
  expect(survived.clientMutationId).toBe(refused.clientMutationId);
  expect(survived.state).toBe("submitting");

  // The ordinary ready drive redelivers the row; the hub refusing again
  // proves the redelivery reached the wire while the row never parked.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 2);
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 2);
  const [redelivered] = await storage.listOutbox("ref_a");
  if (!redelivered) throw new Error("expected the refused row to stay durable through redelivery");
  expect(redelivered.state).toBe("submitting");
  expect(redelivered.clientMutationId).toBe(refused.clientMutationId);

  // Once the hub accepts, the refusal's paced retry redelivers it - the
  // redrive the spec gives a refused row. (A direct drive made here would
  // join the second refusal's still-running dispatch and die with its
  // stop; the pace is what recovers it.)
  refuse = false;
  paced.at(-1)?.();
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 3);
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a")).length === 0);
  expect(await storage.listOutbox("ref_a")).toEqual([]);
  restoreScheduler();
});

// #4342: the direct-send fallback (C13) has no durable row, so a
// backpressure refusal there can neither pace nor surface busy: it
// rejects the send with the hub's own message through the existing
// send-failure path, nothing is queued, and no retry is armed. The user
// re-presses Send; the spec claims no paced retry for this path.
test("a backpressure refusal on the direct-send path rejects with the hub's message and pins nothing", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const scheduled: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt) => {
    scheduled.push(attempt);
    return () => {};
  });
  // Force the direct path the same way the quota test does: the durable
  // write refuses at once, so the send goes out as a plain RPC.
  const add = vi.spyOn(IDBObjectStore.prototype, "add").mockImplementationOnce(() => {
    throw new DOMException("storage full", "QuotaExceededError");
  });

  await expect(threadsStore.getState().send("ref_a", "sent from a full origin")).rejects.toThrow(HUB_REFUSAL_MESSAGE);
  expect(add).toHaveBeenCalledTimes(1);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  expect(await storage.listOutbox()).toEqual([]);
  expect(threadsStore.getState().transientRefusalPaces.size).toBe(0);
  expect(scheduled).toEqual([]);
  restoreScheduler();
});

// #4342 (implementation review): a fresh refusal for a ref replaces the
// pending paced retry's timer rather than stacking a second one — the
// outbox's 2s discovery cadence re-drives a refused row on its own, and a
// live timer per refusal would multiply dispatches past the pace the
// backoff promises.
test("a fresh refusal replaces the pending paced retry's timer", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const attempts: number[] = [];
  const cancels: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt) => {
    const index = attempts.length;
    attempts.push(attempt);
    return () => cancels.push(index);
  });

  await threadsStore.getState().send("ref_a", "held one");
  await flushIndexedDBUntil(() => attempts.length === 1);
  // Let the refused dispatch's settle chain drain before re-driving: the
  // dispatcher coalesces a notify onto the still-settling run and its stop
  // outcome discards the requested run, so a notify that races the settle
  // would be lost. IndexedDB turns yield to the microtasks that finish the
  // settle, so two turns bound the drain.
  await flushIndexedDBUntil(() => false, 2);
  // A discovery-driven re-attempt before the first retry fires refuses
  // again; the new arm must replace (cancel) the first.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => attempts.length === 2);
  expect(attempts).toEqual([1, 2]);
  expect(cancels).toEqual([0]);
  restoreScheduler();
});

// #4342 (implementation review): the busy marker's contract is settlement —
// it clears when the refused record settles (a receipt, a reclassification),
// not when the paced attempt fires. An in-flight attempt is still the hub's
// refusal being worked: the row must keep reading as busy until its retry is
// actually accepted.
test("the busy marker clears when the record settles, not when the paced attempt fires", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  let refuse = true;
  const acceptedRetry = deferred<TurnStartResponse>();
  fake.on("turn/start", (params) => {
    if (refuse) {
      throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
        evenerErrorInfo: "connectionBackpressure",
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
        retryDisposition: "automatic",
        cause: "queueSaturated",
      });
    }
    return acceptedRetry.promise;
  });
  const retries: Array<() => void> = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((_attempt, retry) => {
    retries.push(retry);
    return () => {};
  });

  await threadsStore.getState().send("ref_a", "held until accepted");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the refused row to stay durable");
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The paced attempt goes out and parks on the hub's answer: the marker
  // must survive the attempt in flight.
  refuse = false;
  expect(retries.length).toBe(1);
  const firstRetry = retries[0];
  if (!firstRetry) throw new Error("expected the paced retry to be armed");
  firstRetry();
  await flushIndexedDBUntil(() => fake.calls.filter((call) => call.method === "turn/start").length === 2);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The accepted retry settles the row; settlement is what clears the marker.
  acceptedRetry.resolve({
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    receipt: mutationReceipt(row.clientMutationId),
  });
  await flushIndexedDBUntil(
    () => threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId !== row.clientMutationId,
  );
  await flushIndexedDBUntil(async () => (await storage.listOutbox("ref_a")).length === 0);
  expect(await storage.listOutbox("ref_a")).toEqual([]);
  restoreScheduler();
});

// #4342 (implementation review): the store's test reset cancels every armed
// paced retry along with the pace map — clearing the map alone would leave
// each arm's timer live against the next test's fresh runtime.
test("resetting the store cancels pending paced retries", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  let arms = 0;
  const cancels: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => {
    const index = arms;
    arms += 1;
    return () => cancels.push(index);
  });

  await threadsStore.getState().send("ref_a", "held across a reset");
  await flushIndexedDBUntil(() => arms === 1);
  resetThreadsStoreForTests();
  expect(arms).toBe(1);
  expect(cancels).toEqual([0]);
  restoreScheduler();
});

// #4342 (review): the marker's settle prune also runs when the row's
// settlement happened in ANOTHER tab - this tab's dispatcher never sees
// that settle, but every dispatch tail re-reads the shared storage, and the
// prune rides it.
test("a refusal's busy marker leaves when another tab settles the row", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().send("ref_a", "held across tabs");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the refused row to stay durable");
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The other tab settles the row straight into the optimistic store, through
  // its own adapter instance over the shared database: this tab's dispatcher
  // and storage hooks never see the write, and the suite's broadcast channel
  // is a stub, so the dispatch tail's prune is the one path that can clear
  // the marker.
  const otherTab = new MutationOutboxIndexedDB();
  await otherTab.settleReceipt(row.clientMutationId, "reflected");
  otherTab.close();
  // Any dispatch pass's pin refresh re-reads the ref; the prune rides it.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a") === undefined);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")).toBeUndefined();
  restoreScheduler();
});

// #4342 (round-4 review): a Stop whose cancel scan skips the in-flight
// attempted row must not be outrun by the refusal that answers that attempt.
// The hub proved the attempt never landed, but the row holds canceled -
// released only by an explicit Retry - instead of returning dispatchable
// ahead of the interrupt the Stop already queued.
test("a refusal after a mid-attempt Stop holds the row canceled instead of redelivering", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  const inFlight = deferred<TurnStartResponse>();
  fake.on("turn/start", () => inFlight.promise);
  fake.on("turn/interrupt", (params) => ({ receipt: mutationReceipt(params.clientMutationId) }));
  const paced: number[] = [];
  const restoreScheduler = installBackpressureRetrySchedulerForTests((attempt) => {
    paced.push(attempt);
    return () => {};
  });

  await threadsStore.getState().send("ref_a", "held past a stop");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/start"));
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the in-flight row to stay durable");
  expect(row.attempted).toBe(true);

  // The user stops while the attempt is in flight: the cancel scan skips
  // the attempted row, and the interrupt goes out.
  await threadsStore.getState().interrupt("ref_a");
  await flushIndexedDBUntil(() => fake.calls.some((call) => call.method === "turn/interrupt"));

  // The hub's refusal lands after the Stop: the rollback holds the row
  // canceled, arms no paced retry, and leaves no busy marker.
  inFlight.reject(
    new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: row.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    }),
  );
  await flushIndexedDBUntil(async () => (await storage.getOutbox(row.clientMutationId))?.state === "canceled");
  expect((await storage.getOutbox(row.clientMutationId))?.state).toBe("canceled");
  expect(paced).toEqual([]);
  expect(threadsStore.getState().transientRefusalPaces.size).toBe(0);

  // Discovery cannot outrun the Stop either: the canceled row never re-sends.
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => false, 4);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(1);
  restoreScheduler();
});

// #4342 (review): the settle prune is fire-and-forget at every call site,
// so a storage read failing inside it must not escape as an unhandled
// rejection - and a failed read proves nothing, so it clears nothing: the
// marker and its armed retry survive for a later recovery pass. The
// unhandled-rejection claim itself is pinned by this file run's exit, not
// an in-test listener (vitest intercepts process-level unhandledRejection
// dispatch, so a per-test listener cannot discriminate; see the
// transcript-scroll precedent) - before the fix, this suite exits 1 naming
// the stubbed storage failure.
test("a refusal's busy marker survives a prune whose storage read fails", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await threadsStore.getState().ensureThread("ref_a");
  fake.on("turn/start", (params) => {
    throw new WireError(HUB_REFUSAL_MESSAGE, -32014, {
      evenerErrorInfo: "connectionBackpressure",
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "automatic",
      cause: "queueSaturated",
    });
  });
  const restoreScheduler = installBackpressureRetrySchedulerForTests(() => () => {});

  await threadsStore.getState().send("ref_a", "held through a failed prune");
  // The rollback lands one storage read after the wire call, so wait for
  // the marker the assertions below read rather than the call alone.
  await flushIndexedDBUntil(
    () =>
      fake.calls.some((call) => call.method === "turn/start") &&
      threadsStore.getState().transientRefusalPaces.get("ref_a")?.attempt === 1,
  );
  const [row] = await storage.listOutbox("ref_a");
  if (!row) throw new Error("expected the refused row to stay durable");
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // The dispatch-tail prune's shared-storage read fails for the marked row:
  // the dispatcher drain's own read of the same row comes first and its
  // failure is handled by the dispatch chain, so the failure is armed for
  // the second read - the tail prune's.
  let failedReads = 0;
  let markerReads = 0;
  const originalRead = storage.getOutbox.bind(storage);
  const read = vi.spyOn(storage, "getOutbox").mockImplementation((clientMutationId: string) => {
    if (clientMutationId === row.clientMutationId) {
      markerReads += 1;
      if (markerReads > 1) {
        failedReads += 1;
        return Promise.reject(new MutationStorageTimeoutError());
      }
    }
    return originalRead(clientMutationId);
  });
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => failedReads > 0, 30);
  read.mockRestore();
  expect(failedReads).toBeGreaterThan(0);
  // The failed prune proved nothing: the marker and its retry survive.
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")?.clientMutationId).toBe(row.clientMutationId);

  // Once storage answers again, the same dispatch-tail path still prunes a
  // row that settled meanwhile - the recovery pass the marker was kept for.
  const otherTab = new MutationOutboxIndexedDB();
  await otherTab.settleReceipt(row.clientMutationId, "reflected");
  otherTab.close();
  notifyReadyForMutationDispatch(["ref_a"]);
  await flushIndexedDBUntil(() => threadsStore.getState().transientRefusalPaces.get("ref_a") === undefined);
  expect(threadsStore.getState().transientRefusalPaces.get("ref_a")).toBeUndefined();
  restoreScheduler();
});
