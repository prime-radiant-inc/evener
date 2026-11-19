// The destination claim lease meets the hydration lifecycle: a claim waits on
// one owned hydration record, and every terminal answer for that record - the
// deletion fence, the hub's typed history failure - has to reach the wait, or
// the resend behind it stays busy holding a destination nothing will decide.
// The same lifecycle owns the registration a reconnect refresh leaves behind
// and the body ownership that decides when a pane's reader is still owed.

import "fake-indexeddb/auto";
import { type ThreadReadResponse, threadModelFromCache, WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { beforeEach, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import {
  type EntryTranscriptRoute,
  entryTranscriptRoute,
  releaseEntryTranscriptReader,
  setProductionEntryCacheForTests,
} from "./entryTranscriptRoutes";
import { deleteMutationDatabase } from "./mutationOutboxTestUtils";
import { entryHistoryKey, setSessionCacheLookupDeadlineForTests } from "./sessionCacheSchema";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { pumpIndexedDBUntil } from "./testing/indexedDBPump";
import { bodyOwners, holdHistory, releaseHistory } from "./threadOwners";
import {
  installHydrationRetrySchedulerForTests,
  markCacheSessionsDeleted,
  putThreadModel,
  resetThreadsStoreForTests,
  threadsStore,
} from "./threads";
import { connectMutationClient, readResponse, resumeOnlySnapshot } from "./threadsSessionActionsTestUtils";
import { acquireUnreadThreadClaim } from "./useThreadModel";

beforeEach(async () => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  await deleteMutationDatabase();
});

async function flushUntil(done: () => boolean, maxTurns = 40): Promise<void> {
  for (let turn = 0; turn < maxTurns && !done(); turn += 1) await nextMacrotask();
  if (!done()) throw new Error("condition not reached");
}

async function pump(turns = 20): Promise<void> {
  for (let turn = 0; turn < turns; turn += 1) await nextMacrotask();
}

function entryRouteOf(ref: string): EntryTranscriptRoute {
  const route = entryTranscriptRoute(ref);
  if (!route) throw new Error("expected an entry route");
  return route;
}

function rebuildingRejection(): WireError {
  return new WireError("thread history is being rebuilt in the background", -32000, {
    evenerErrorInfo: "transcriptIndexRebuilding",
  });
}

function historyFailedRejection(): WireError {
  return new WireError("thread history failed at entry 3", -32000, {
    evenerErrorInfo: "transcriptHistoryFailed",
  });
}

// The claim is parked on an owned hydration a deletion just retired. Bounded,
// not awaited: a claim left hanging is the defect, and a test timeout would
// report it as a slow test rather than as a hang.
async function settleOutcome(promise: Promise<unknown>): Promise<"pending" | "resolved" | "rejected"> {
  let outcome: "pending" | "resolved" | "rejected" = "pending";
  void promise.then(
    () => {
      outcome = "resolved";
    },
    () => {
      outcome = "rejected";
    },
  );
  for (let turn = 0; turn < 60 && outcome === "pending"; turn += 1) await nextMacrotask();
  return outcome;
}

// A deletion that lands in this tab while the claim waits on a scheduled retry
// must wake it. The deletion fence revokes the retry and the aborted read never
// reaches the rejection handler that would settle the wait, so the settlement
// has to happen at the fence itself.
test("a deletion during a claim's retry wait rejects the claim instead of parking it", async () => {
  const ref = "local:deleted-mid-wait";
  const fake = connectMutationClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    if (reads === 1) throw new Error("transport died");
    return resumeOnlySnapshot(ref);
  });
  // The claim's failed read arms one retry; the retry is never fired, because
  // the deletion is what answers the lifecycle.
  const restoreScheduler = installHydrationRetrySchedulerForTests(() => () => {});
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => reads === 1);
  // The retry is armed by now, so the claim is parked on the owned lifecycle.
  await pump();

  markCacheSessionsDeleted([ref]);

  expect(await settleOutcome(claim), "the deletion fence wakes the waiting claim").toBe("rejected");
  expect(reads, "the fence revoked the retry rather than letting it read").toBe(1);
  expect(bodyOwners(ref), "a rejected acquisition leaves no body hold behind").toBe(0);
  restoreScheduler();
}, 20_000);

// The retry's answer is the hub's terminal one for the attempt. Nothing will
// publish and nothing should read again, so the claim waiting on that lifecycle
// learns the failure itself rather than waiting for a settlement that never
// comes.
test("a history-failed retry rejects the waiting claim and arms no further read", async () => {
  const ref = "local:failed-mid-wait";
  const fake = connectMutationClient();
  const claimRead = deferred<ThreadReadResponse>();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    if (reads === 1) return claimRead.promise;
    throw historyFailedRejection();
  });
  let fireRetry: (() => void) | undefined;
  const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, run) => {
    fireRetry = run;
    return () => {};
  });
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => reads === 1);
  // A pane's bounded lookup installs a cached shell while the claim's read is
  // still outstanding: a model whose source read has not verified it, which is
  // the base model the claim's attempt and its retry both capture. A shell
  // announces neither Send nor Queue, so the claim may not resolve on it.
  const route = entryRouteOf(ref);
  route.cachedUnverified = true;
  // A shell advertises neither Send nor Queue: the source is the only thing that
  // can say what the destination allows.
  putThreadModel(
    ref,
    threadModelFromCache(
      {
        ref,
        threadId: `thr_${ref}`,
        name: "",
        modelProvider: "test/provider",
        model: "test/provider",
        imageSessionId: "session-a",
        savedAt: 1,
        history: {
          bootGeneration: "1",
          epoch: 1,
          incarnation: "inc-shell",
          length: 0,
          appliedGeneration: 0,
          issuedGeneration: 0,
          turns: [],
        },
      },
      0,
    ),
  );
  // The claim's read reports the index rebuilding, so its retry is armed.
  claimRead.reject(rebuildingRejection());
  await flushUntil(() => fireRetry !== undefined);
  expect(reads, "the claim's read reported rebuilding and armed its retry").toBe(1);
  // A concurrent source read verifies the destination while the retry waits.
  // A neutral retirement would let the woken claim resolve on that model; the
  // hub's terminal answer for this attempt is the claim's answer, so it must
  // reject instead.
  route.cachedUnverified = false;
  // The retry answers with the hub's terminal answer for that attempt.
  fireRetry?.();

  expect(await settleOutcome(claim), "the terminal answer rejects the claim").toBe("rejected");
  // The claim's rebuilding read, the retry's terminal answer, and nothing after
  // it: the rule against re-issuing a history-failed read stands.
  expect(reads, "the failure is terminal: the retry ran once and no read followed").toBe(2);
  await pump();
  expect(reads, "no read follows the terminal answer").toBe(2);
  restoreScheduler();
}, 20_000);

// A routing claim's hold keeps the model, the subscription and the route alive
// for a resend to dispatch on, and never owns the bodies. When the last pane
// closes during that resend, its reader must still be detached and its display
// pruned: owning the bodies is what keeps a reader, not holding the ref.
test("the last pane closing during a resend detaches its reader and prunes its display", async () => {
  const ref = "local:body-owner";
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse(ref, { name: "Body" }));
  await threadsStore.getState().ensureThread(ref);
  const route = entryRouteOf(ref);
  expect(bodyOwners(ref), "a pane's acquisition owns the bodies").toBe(1);
  expect(route.reader, "a body owner keeps a reader").toBeDefined();
  // The resend's routing lease, taken over the same destination.
  const lease = await acquireUnreadThreadClaim(ref);

  // The pane closes: its body ownership and its hold are gone, the lease stays.
  threadsStore.getState().releaseThread(ref);

  expect(bodyOwners(ref), "the pane's body ownership is gone").toBe(0);
  expect(route.reader, "the routing lease does not keep a reader").toBeUndefined();
  // The route and its repository survive: the resend dispatches on them.
  expect(route.repository, "the routing lease keeps the repository").toBeDefined();
  expect(threadsStore.getState().threads.has(ref), "the routing lease keeps the model").toBe(true);
  lease.release();
  await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
}, 20_000);

// A history hold owns the bodies, so it keeps the reader even though no pane is
// open. The body question, not the keep-alive count, is what separates the two.
test("a history hold keeps the reader a routing lease alone would release", async () => {
  const ref = "local:history-hold";
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  const route = entryRouteOf(ref);
  holdHistory(ref);

  threadsStore.getState().releaseThread(ref);

  expect(route.reader, "a history hold owns the live bodies").toBeDefined();
  expect(bodyOwners(ref), "the pane's body ownership is gone").toBe(0);
  releaseHistory(ref);
  releaseEntryTranscriptReader(ref, true);
  expect(route.reader, "with no body owner left the reader is released").toBeUndefined();
}, 20_000);

// A failed first routing read removes its own pending entry, so the reconnect
// refresh that follows has no predecessor to adopt. It still has to register:
// the awakened claim joins that read, and a read it cannot join is a duplicate
// it starts beside the one already answering.
test("a reconnect refresh for a waiting claim registers the read the claim joins", async () => {
  const ref = "local:reconnect-join";
  const first = connectMutationClient();
  let firstReads = 0;
  first.on("thread/read", () => {
    firstReads += 1;
    throw new Error("transport died");
  });
  const restoreScheduler = installHydrationRetrySchedulerForTests(() => () => {});
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => firstReads === 1);
  // The failed attempt has removed its pending entry by now; what stands is the
  // claim's own wait on the owned lifecycle.
  await pump();

  // The replacement connects. Its answer is held, so the claim must be parked on
  // exactly this read when it arrives.
  const replacementRead = deferred<ThreadReadResponse>();
  let replacementReads = 0;
  const second = new FakeClient("ready");
  second.on("thread/read", () => {
    replacementReads += 1;
    return replacementRead.promise;
  });
  connectionStore.getState().connect(second);
  await flushUntil(() => replacementReads > 0);
  // Exactly the reconnect's one read: the claim is parked on it rather than
  // having started one of its own beside it.
  await pump();
  expect(replacementReads, "the reconnect registered one read, which the claim joined").toBe(1);

  replacementRead.resolve(readResponse(ref));
  const lease = await claim;
  expect(replacementReads, "the claim did not supersede the reconnect read with a duplicate").toBe(1);
  lease.release();
  restoreScheduler();
}, 20_000);

// A pane acquiring across the fence keeps its deleted-session display: the
// same settlement reaches both kinds of wait, and only the claim rejects.
test("a pane acquiring across a deletion still resolves and shows the deleted state", async () => {
  const ref = "local:pane-across-fence";
  const fake = connectMutationClient();
  const read = deferred<ThreadReadResponse>();
  fake.on("thread/read", () => read.promise);
  const pane = threadsStore.getState().ensureThread(ref);
  await flushUntil(() => fake.calls.some((call) => call.method === "thread/read"));

  markCacheSessionsDeleted([ref]);
  read.reject(new Error("transport died"));

  await expect(pane, "a pane keeps its deleted-session display").resolves.toBeUndefined();
  expect(threadsStore.getState().deletedRefs.has(ref), "the fence is recorded for the pane").toBe(true);
}, 20_000);

// The composer's resend gate reads the destination's capabilities. A routing
// claim's read must not resolve on a cached shell a pane installed while that
// read awaited its durable authorities: the shell's incarnation is older, so
// the repository refuses the source response, and a claim that resolved on the
// shell would carry its empty capability set onto the lease - refusing a
// resend the hub's fresh snapshot allows.
test("a cached shell installed during a routing read does not certify the claim", async () => {
  const ref = "local:superseded-shell";
  const cache = createEntryCacheFixture();
  setProductionEntryCacheForTests(cache.cache);
  // The pane's lookup answers at once; only the claim's authority read is
  // held, so the shell lands while the source response awaits it.
  setSessionCacheLookupDeadlineForTests(30_000);
  const clientId = location.origin;
  const older = { clientId, hostId: "local", sessionId: `sess_${ref}`, incarnation: "inc-1" };
  // The lookup only reports a record inside the cache's freshness window.
  const savedAt = Date.now();
  const seed = await cache.cache.commitBatch({
    historyKey: entryHistoryKey(older),
    clearEpoch: 0,
    items: [],
    sessionMetadata: {
      historyKey: entryHistoryKey(older),
      requestedRefs: [{ clientId, ref }],
      resolvedIdentity: older,
      savedAt,
      accessedAt: savedAt,
      formatVersion: 3,
      cachedHeader: {
        ref,
        threadId: `thr_${ref}`,
        name: "",
        modelProvider: "test/provider",
        model: "test/provider",
        imageSessionId: `sess_${ref}`,
        savedAt,
        history: {
          bootGeneration: "1",
          epoch: 1,
          incarnation: "inc-1",
          length: 0,
          appliedGeneration: 0,
          issuedGeneration: 0,
        },
      },
    },
  });
  expect(seed.outcome, "the pane's cached shell is seeded durably").toBe("committed");
  const authorities = deferred<void>();
  const readAuthorities = cache.cache.getClearEpoch.bind(cache.cache);
  let authorityReads = 0;
  vi.spyOn(cache.cache, "getClearEpoch").mockImplementation(async () => {
    // Only the claim's read is held; the pane's own authority read answers, so
    // its shell installs while the claim's response is still waiting.
    authorityReads += 1;
    if (authorityReads === 1) await authorities.promise;
    return readAuthorities();
  });
  const fake = connectMutationClient();
  // The source's answer is the fresh incarnation, whose read grants Send.
  fake.on("thread/read", () => {
    const response = readResponse(ref);
    const snapshot = response.snapshot;
    if (snapshot === undefined) throw new Error(`expected a snapshot for ${ref}`);
    return {
      ...response,
      snapshot: { ...snapshot, length: snapshot.length ?? 0, incarnation: "inc-2" },
    };
  });
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => fake.calls.some((call) => call.method === "thread/read"));
  // The pane opens over the claim's read and installs the older shell while the
  // source response awaits its authorities.
  const pane = threadsStore.getState().ensureThread(ref);
  const shellInstalled = await pumpIndexedDBUntil({
    turns: 40,
    until: () => entryRouteOf(ref).repository?.getSnapshot()?.history?.incarnation === "inc-1",
  });
  expect(shellInstalled, "the pane installed the older incarnation").toBe(true);
  authorities.resolve();
  const lease = await claim;
  const published = threadsStore.getState().threads.get(ref);
  expect(published?.capabilities.send, "the claim's lease carries the source's capabilities").toBe(true);
  expect(published?.history?.incarnation, "the source's incarnation is the one serving the claim").toBe("inc-2");
  expect(entryRouteOf(ref).cachedUnverified, "the source response verified the route").toBe(false);
  await pane;
  lease.release();
  await cache.dispose();
  setSessionCacheLookupDeadlineForTests(250);
}, 20_000);
