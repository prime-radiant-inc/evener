// @vitest-environment node

import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import { draftStorageKey } from "../panes/session/composer/draft";
import { MemoryStorage } from "../storageTestUtils";
import { browserDraftKey, createBrowserDraftRepository } from "./browserDraftRepository";
import { IndexedDBTransactionTimeoutError } from "./idbConnection";
import {
  ANNOTATION_HEADS_STORE,
  ANNOTATION_NAMESPACES_STORE,
  ANNOTATION_OPERATIONS_STORE,
  COMPOSER_DRAFTS_STORE,
  DRAFT_TRANSFERS_STORE,
} from "./localCompositionSchema";
import { openAnnotationStore } from "./testing/annotationFixture";
import {
  holdIndexedDBEvent,
  holdNextTransactionEvent,
  holdNextWriteTransaction,
  settleRealTasks,
} from "./testing/stalledIndexedDB";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// Just the platform: these cases prime their own rows and hold their own
// transaction events, so they need no seeded annotation.
function openStallFixture(databaseName: string) {
  return openAnnotationStore({ databaseName, origin: "https://stall.example", targetFor: () => null });
}

// Annotation transactions gained the same watchdog the drafts repository and
// the outbox already have: a stalled request or completion event stops
// waiting and REJECTS, because the open deadline alone never covered the
// transaction behind it - without one, beginRead would hang a reader forever
// and a save/retry pass would never settle. Annotation writes reconcile on
// retry through their operation rows (the duplicate-operation identity
// check) and reads re-read durable state, so the unobserved outcome is
// honestly reported as a fault for the caller's own retry.
test("a stalled annotation transaction stops waiting and the same adapter can reopen and read", async () => {
  const { drafts, repository, document } = await openStallFixture("annotation-stall");
  // Prime the namespace and head rows, then hold the next readwrite
  // transaction over the read stores: fake-indexeddb commits it, but its
  // "complete" event - and beginRead waiting on that event - stays held.
  await repository.beginRead(document);
  const hold = holdNextWriteTransaction([ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE]);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  repository.beginRead(document).catch((error: unknown) => {
    failure = error;
  });
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(failure).toBeInstanceOf(IndexedDBTransactionTimeoutError);
  hold.release();
  // The watchdog retired the stalled connection; a fresh read still works.
  expect((await repository.beginRead(document)).key).toEqual(document);
  repository.close();
  drafts.close();
});

// Ticket publication must follow the durable read generation, not the order
// two beginRead continuations happen to run in. Read A's transaction commits,
// but its completion event is held; read B is issued meanwhile and advances
// the durable generation. When A's continuation finally runs it must not
// prune B's registration or replace B's remembered ticket: B owns the newest
// generation and is still acceptable, while A alone is stale.
test("a held earlier read does not displace the later read's ticket", async () => {
  const { drafts, repository, document } = await openStallFixture("annotation-read-order");
  const content = { text: "ordered read", binary: false, mediaType: "text/markdown", truncated: false, sizeBytes: 12 };
  try {
    // Prime the namespace and head rows so the held read is not the schema writer.
    await repository.beginRead(document);
    const hold = holdNextWriteTransaction([ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE]);
    const earlier = repository.beginRead(document);
    await hold.reached;
    // B is issued while A's completion is still held, and completes first.
    const later = await repository.beginRead(document);
    hold.release();
    const earlierTicket = await earlier;
    expect(earlierTicket.requestId).not.toBe(later.requestId);

    // B owns the latest durable generation: accepting it must succeed.
    expect(await repository.acceptRead(later, content)).toMatchObject({ state: "accepted" });
    // A was superseded durably, so only its own acceptance is stale.
    expect(await repository.acceptRead(earlierTicket, content)).toEqual({ state: "stale-ticket" });
  } finally {
    repository.close();
    drafts.close();
  }
});

// The repository's watchdog bounds the happy path; the rollback the error
// path awaits must share that bound. A body that rejects before the deadline
// enters the catch, whose completed-wait used to sit OUTSIDE the deadline
// race - a stalled abort event kept the call pending forever, past the
// watchdog, blocking the shared annotation retry pass for good.
test("a body rejection with a stalled abort event still settles within the watchdog", async () => {
  const { drafts, repository, document } = await openStallFixture("annotation-abort-stall");
  await repository.beginRead(document);
  // The next readwrite transaction over the read stores has its ABORT event
  // held: the rollback the catch awaits after the body rejects can never
  // settle on its own.
  const hold = holdNextTransactionEvent([ANNOTATION_NAMESPACES_STORE, ANNOTATION_HEADS_STORE], "abort");
  // The beginRead body rejects through a one-shot request-creation fault.
  const getSpy = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementationOnce(() => {
    throw new Error("request creation fault");
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  let settled = false;
  repository
    .beginRead(document)
    .catch((error: unknown) => {
      failure = error;
    })
    .finally(() => {
      settled = true;
    });
  await vi.advanceTimersByTimeAsync(10_001);
  // The call must settle by the watchdog deadline even though its rollback
  // event never arrived.
  expect(settled).toBe(true);
  expect(failure).toBeDefined();
  hold.release();
  getSpy.mockRestore();
  // Subsequent recovery: the watchdog retired the stalled connection, and a
  // fresh read still works.
  expect((await repository.beginRead(document)).key).toEqual(document);
  repository.close();
  drafts.close();
});

// The runtime's shared retry pass ends in this scan, and its transaction sat
// outside the repository's deadline wrapper: a stalled request left every
// later retry awaiting the same never-settling promise.
test("a stalled retry outcome scan stops waiting and a later retry proceeds", async () => {
  const { drafts, repository, document } = await openStallFixture("outcome-stall");
  await repository.beginRead(document);
  const getAll = IDBObjectStore.prototype.getAll;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (this: IDBObjectStore, ...args) {
    const request = getAll.apply(this, args);
    // The outcome scan's transaction covers exactly the operations store;
    // every retry-pass read runs over wider transactions or other stores.
    if (
      this.transaction.objectStoreNames.length === 1 &&
      this.transaction.objectStoreNames.contains(ANNOTATION_OPERATIONS_STORE) &&
      !hold
    ) {
      hold = holdIndexedDBEvent(request, "success");
    }
    return request;
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  repository.retryPendingWithOutcome("storage-recovered").catch((error: unknown) => {
    failure = error;
  });
  await settleRealTasks(() => hold !== undefined, 25);
  if (!hold) throw new Error("outcome scan did not reach IndexedDB");
  await hold.reached;
  await vi.advanceTimersByTimeAsync(10_001);
  expect(failure).toBeInstanceOf(IndexedDBTransactionTimeoutError);
  hold.release();
  // The retired connection does not poison the shared retry pass: a later
  // retry completes over a fresh connection.
  expect(await repository.retryPendingWithOutcome("storage-recovered")).toEqual({ pending: false });
  repository.close();
  drafts.close();
});

// The scan's rollback wait shares the wrapper's defect: a body that rejects
// enters the catch, and its completed-wait used to sit OUTSIDE the deadline
// race - a stalled abort event kept the shared retry pass pending forever,
// even after the watchdog fired.
test("an outcome scan body rejection with a stalled abort event still settles within the watchdog", async () => {
  const { drafts, repository, document } = await openStallFixture("outcome-abort-stall");
  await repository.beginRead(document);
  // The scan's transaction covers exactly the operations store in readonly
  // mode; its ABORT event stays held, so the rollback the catch awaits can
  // never settle on its own.
  const hold = holdNextTransactionEvent([ANNOTATION_OPERATIONS_STORE], "abort", "readonly");
  // The scan body rejects through a request-creation fault scoped to the
  // single-store transaction - the retry pass before it reads over wider
  // transactions and must complete so the scan actually runs.
  const getAll = IDBObjectStore.prototype.getAll;
  let faultArmed = true;
  const getAllSpy = vi.spyOn(IDBObjectStore.prototype, "getAll").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["getAll"]>
  ) {
    const scope = [...this.transaction.objectStoreNames];
    if (faultArmed && scope.length === 1 && scope[0] === ANNOTATION_OPERATIONS_STORE) {
      faultArmed = false;
      throw new Error("request creation fault");
    }
    return getAll.apply(this, args);
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let failure: unknown;
  let settled = false;
  repository
    .retryPendingWithOutcome("storage-recovered")
    .catch((error: unknown) => {
      failure = error;
    })
    .finally(() => {
      settled = true;
    });
  // Real task hops drive the retry pass (no pending operations) and the
  // scan's transaction to the point of its held abort - fake timers do not
  // reach fake-indexeddb's event delivery. The hop count is fixed because the
  // abort is held, so no condition can observe it.
  await settleRealTasks(() => false, 25);
  await vi.advanceTimersByTimeAsync(10_001);
  // The shared retry pass must settle by the watchdog deadline even though
  // its rollback event never arrived.
  expect(settled).toBe(true);
  expect(failure).toBeDefined();
  hold.release();
  getAllSpy.mockRestore();
  // Subsequent recovery: a later retry completes over a fresh connection.
  expect(await repository.retryPendingWithOutcome("storage-recovered")).toEqual({ pending: false });
  repository.close();
  drafts.close();
});

// A transfer whose commit outlives the watchdog reports the recoverable
// pending outcome and skips its legacy cleanup. The commit is nevertheless
// real: the completion's own arrival proves it, and by then the imported row
// exists, so a later scan imports nothing and cleans nothing - the legacy
// localStorage bytes outlive every scan. The late completion is the proof
// moment, so it must finish the cleanup the rejected call never reached.
test("a transfer whose commit lands after the watchdog still removes the legacy bytes it imported", async () => {
  const factory = new IDBFactory();
  const storage = new MemoryStorage();
  const repository = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "draft-transfer-stall",
    storage,
  });
  const key = browserDraftKey("root", "https://stall.example");
  try {
    // The schema and namespace row exist before the held scan opens.
    expect(await repository.transferSupportedDrafts(key.namespace)).toEqual({ state: "committed" });
    const legacyKey = draftStorageKey("ref_a");
    storage.setItem(legacyKey, "legacy draft");
    // The write commits, but its completion event stays held: the watchdog
    // rejects the call while the commit is durable and unobserved.
    const hold = holdNextWriteTransaction([COMPOSER_DRAFTS_STORE, DRAFT_TRANSFERS_STORE, ANNOTATION_NAMESPACES_STORE]);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let reached = false;
    void hold.reached.then(() => {
      reached = true;
    });
    const transfer = repository.transferSupportedDrafts(key.namespace);
    await settleRealTasks(() => reached, 50);
    expect(reached).toBe(true);
    await vi.advanceTimersByTimeAsync(10_001);
    expect(await transfer).toEqual({ state: "pending" });
    // The completion lands: the transaction really committed, and the late arm
    // is the proof that must run the cleanup.
    hold.release();
    vi.useRealTimers();
    await settleRealTasks(() => storage.getItem(legacyKey) === null, 50);
    expect(storage.getItem(legacyKey)).toBeNull();
    // The import committed too, so the durable row carries the legacy text.
    expect((await repository.load({ ...key, sessionRef: "ref_a" }))?.text).toBe("legacy draft");
  } finally {
    repository.close();
  }
});
