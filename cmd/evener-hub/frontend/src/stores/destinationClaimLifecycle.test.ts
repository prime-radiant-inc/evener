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
import type { SessionEntryCache } from "./sessionEntryCache";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { pumpIndexedDBUntil } from "./testing/indexedDBPump";
import { bodyOwners, foregroundHolds, holdHistory, releaseHistory } from "./threadOwners";
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
  setSessionCacheLookupDeadlineForTests(250);
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

// The source's answer: the fresh incarnation is what displaces the older cached
// shell a pane restored, so this response is the claim's verification.
function freshIncarnationAnswer(ref: string): ThreadReadResponse {
  const response = readResponse(ref);
  const snapshot = response.snapshot;
  if (snapshot === undefined) throw new Error(`expected a snapshot for ${ref}`);
  return { ...response, snapshot: { ...snapshot, incarnation: "inc-2" } };
}

// Capture the retry a failed read arms, so a test can fire it on demand.
// `retry.fire` stays undefined until a read answers and arms one.
function captureRetryScheduler(): { retry: { fire: (() => void) | undefined }; restore: () => void } {
  const retry: { fire: (() => void) | undefined } = { fire: undefined };
  const restore = installHydrationRetrySchedulerForTests((_attempt, run) => {
    retry.fire = run;
    return () => {};
  });
  return { retry, restore };
}

// Seed the durable shell a pane's bounded lookup installs: a cached header the
// source read then verifies. `issuedGeneration` is the counter the saving
// session had issued, restored with the shell. `savedAt` is "now" because the
// lookup only reports a record inside the cache's freshness window.
async function seedCachedShell(cache: SessionEntryCache, ref: string, issuedGeneration: number) {
  const clientId = location.origin;
  const identity = { clientId, hostId: "local", sessionId: `sess_${ref}`, incarnation: "inc-1" };
  const savedAt = Date.now();
  return cache.commitBatch({
    historyKey: entryHistoryKey(identity),
    clearEpoch: 0,
    items: [],
    sessionMetadata: {
      historyKey: entryHistoryKey(identity),
      requestedRefs: [{ clientId, ref }],
      resolvedIdentity: identity,
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
          issuedGeneration,
        },
      },
    },
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
  const { retry, restore: restoreScheduler } = captureRetryScheduler();
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
  await flushUntil(() => retry.fire !== undefined);
  expect(reads, "the claim's read reported rebuilding and armed its retry").toBe(1);
  // A concurrent source read verifies the destination while the retry waits.
  // A neutral retirement would let the woken claim resolve on that model; the
  // hub's terminal answer for this attempt is the claim's answer, so it must
  // reject instead.
  route.cachedUnverified = false;
  // The retry answers with the hub's terminal answer for that attempt.
  retry.fire?.();

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

// A shell restored from disk carries the request generation the session that
// saved it had issued. That number is no fence this tab's read has to clear: it
// names a read another session made, and the claim's read carries none at all
// (there was no held model to issue one). Fencing on it discards the source
// response while the route keeps serving the shell, whose empty capability set
// disables Send and Queue for a destination the hub's fresh snapshot allows.
// The shell's incarnation is the source's own - only then is its repository the
// one the response is applied to, and only then does the restored generation
// decide whether the response lands.
test("a cached shell carrying a saved request generation does not discard the routing read that verifies it", async () => {
  const ref = "local:shell-saved-generation";
  const cache = createEntryCacheFixture();
  setProductionEntryCacheForTests(cache.cache);
  // The pane's lookup answers at once; only the claim's authority read is held,
  // so the shell lands while the claim's response awaits it.
  setSessionCacheLookupDeadlineForTests(30_000);
  // The saving session had issued a read: its counter is restored with the
  // shell, and nothing in this tab ever issued that number.
  const seed = await seedCachedShell(cache.cache, ref, 4);
  expect(seed.outcome, "the shell is seeded durably with its saved generation").toBe("committed");
  const authorities = deferred<void>();
  const readAuthorities = cache.cache.getClearEpoch.bind(cache.cache);
  let authorityReads = 0;
  vi.spyOn(cache.cache, "getClearEpoch").mockImplementation(async () => {
    authorityReads += 1;
    if (authorityReads === 1) await authorities.promise;
    return readAuthorities();
  });
  const fake = connectMutationClient();
  // The source answers with the incarnation the shell bound, so nothing in the
  // identity comparison displaces the shell's repository: the saved generation
  // is the only fence between this response and the display.
  fake.on("thread/read", () => readResponse(ref));
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => fake.calls.some((call) => call.method === "thread/read"));
  // The pane opens over the claim's read and installs the shell while the
  // source response awaits its authorities.
  const pane = threadsStore.getState().ensureThread(ref);
  const shellInstalled = await pumpIndexedDBUntil({
    turns: 40,
    until: () => entryRouteOf(ref).repository?.getSnapshot()?.history?.incarnation === "inc-1",
  });
  expect(shellInstalled, "the pane installed the shell from disk").toBe(true);
  authorities.resolve();
  const lease = await claim;
  const published = threadsStore.getState().threads.get(ref);
  expect(published?.capabilities.send, "the claim's lease carries the source's capabilities").toBe(true);
  expect(published?.capabilities.queue, "a resend may queue at the destination the source verified").toBe(true);
  expect(entryRouteOf(ref).cachedUnverified, "the source response verified the route").toBe(false);
  await pane;
  lease.release();
  await cache.dispose();
}, 20_000);

// A resend sharing a pane's pending read takes a routing lease, not the
// bodies. When the pane closes mid-read the last body owner is gone and the
// reader is detached - but the pane's own acquisition is still parked on that
// read, and its post-read certification open reattaches a reader for a pane
// nobody has open. The hold the acquisition took is what says whether that
// pane work is still owed.
test("a pane that closes during a shared read does not reattach its reader after the read lands", async () => {
  const ref = "local:closed-during-shared-read";
  const fake = connectMutationClient();
  const read = deferred<ThreadReadResponse>();
  fake.on("thread/read", () => read.promise);
  const pane = threadsStore.getState().ensureThread(ref);
  await flushUntil(() => fake.calls.some((call) => call.method === "thread/read"));
  // A resend claims the same destination and joins the pane's read.
  const claim = acquireUnreadThreadClaim(ref);
  await pump();

  // The pane closes mid-read: its body ownership and its reader are gone, and
  // only the resend's routing lease keeps the destination alive.
  threadsStore.getState().releaseThread(ref);
  expect(bodyOwners(ref), "the pane's body ownership is gone").toBe(0);
  expect(entryRouteOf(ref).reader, "the released pane's reader is detached").toBeUndefined();

  // The shared read lands, resolving both the closed pane's acquisition and the
  // resend's claim.
  read.resolve(readResponse(ref));
  await pane;
  const lease = await claim;
  await pump();

  expect(bodyOwners(ref), "the closed pane never takes body ownership back").toBe(0);
  expect(entryRouteOf(ref).reader, "the closed pane's post-read open does not reattach the reader").toBeUndefined();
  lease.release();
  await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
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
  const seed = await seedCachedShell(cache.cache, ref, 0);
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
  fake.on("thread/read", () => freshIncarnationAnswer(ref));
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
}, 20_000);

// A mounted pane whose FIRST read the hub refuses has a loading surface and no
// model to carry the diagnostic. Rolling its only hold back would leave the
// view on that surface for good, so the pane keeps the paced first-read
// recovery it has without a model, and the read that finally succeeds
// hydrates the view that never unmounted.
test("a mounted pane without a model retries a history-failed first read and hydrates on the later success", async () => {
  const ref = "local:pane-first-read-failed";
  const fake = connectMutationClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    if (reads === 1) throw historyFailedRejection();
    return readResponse(ref, { name: "Recovered" });
  });
  const { retry, restore: restoreScheduler } = captureRetryScheduler();
  const pane = threadsStore.getState().ensureThread(ref);
  await flushUntil(() => retry.fire !== undefined);
  expect(reads, "the pane's first read answered with the hub's refusal").toBe(1);
  expect(threadsStore.getState().threads.has(ref), "no model exists to carry the diagnostic").toBe(false);
  expect(foregroundHolds(ref), "the mounted pane keeps the only hold it took").toBe(1);
  expect(bodyOwners(ref), "the mounted pane keeps owning its bodies").toBe(1);

  retry.fire?.();
  await pane;

  expect(threadsStore.getState().threads.get(ref)?.name, "the later read hydrated the same mounted view").toBe(
    "Recovered",
  );
  expect(reads, "the recovery's read is the only one after the refusal").toBe(2);
  restoreScheduler();
}, 20_000);

// The delegate card's watched lifecycle is the same shape without a pane: a
// lean watch whose first read is refused keeps its refcounted claim and its
// retry, and a later read's snapshot is what the card finally renders.
test("an uncached delegate watch retries a history-failed first read and hydrates on the later success", async () => {
  const ref = "local:watched-first-read-failed";
  const fake = connectMutationClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    if (reads === 1) throw historyFailedRejection();
    return readResponse(ref, { name: "Card" });
  });
  const { retry, restore: restoreScheduler } = captureRetryScheduler();
  const watching = threadsStore.getState().watchThread(ref);
  await flushUntil(() => retry.fire !== undefined);
  expect(reads, "the watch's first read answered with the hub's refusal").toBe(1);
  expect(threadsStore.getState().watchedThreads.has(ref), "the card has no model yet").toBe(false);

  retry.fire?.();
  await watching;

  expect(threadsStore.getState().watchedThreads.get(ref)?.name, "the later read hydrated the same card").toBe("Card");
  expect(reads, "the recovery's read is the only one after the refusal").toBe(2);
  threadsStore.getState().releaseWatchedThread(ref);
  restoreScheduler();
}, 20_000);

// The typed history failure is the hub's terminal answer for the attempt, and a
// destination nobody has opened has no held model to answer it against. A
// resend's claim has no surface at all, so it must learn the answer and stop:
// pacing a retry over a terminal response repeats it forever while the composer
// behind the claim stays busy waiting for a decision that never comes. No read
// may follow a refusal the claim has been told about.
test("a routing-only claim on an unopened destination rejects at once, arms no retry, and leaves no read behind", async () => {
  const ref = "local:routing-first-read-failed";
  const fake = connectMutationClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    throw historyFailedRejection();
  });
  let scheduled = 0;
  const restoreScheduler = installHydrationRetrySchedulerForTests(() => {
    scheduled += 1;
    return () => {};
  });
  const claim = acquireUnreadThreadClaim(ref);

  expect(await settleOutcome(claim), "the hub's answer rejects the unopened claim").toBe("rejected");
  expect(reads, "the refusal is the only read").toBe(1);
  expect(scheduled, "no pane-shaped recovery is armed over a refused destination").toBe(0);
  await pump();
  expect(reads, "the rejected claim leaves no read behind it").toBe(1);
  expect(bodyOwners(ref), "a rejected routing acquisition leaves no body hold behind").toBe(0);
  restoreScheduler();
}, 20_000);

// The two owners of one refused first read want opposite things: the pane has
// a loading surface and needs the recovery, the resend has none and must not
// wait for it. Both hold the ref at once, and each gets its own answer - the
// pane hydrates from the retry that follows, and the claim rejects without
// being the reason the retry stopped.
test("a claim sharing a pane's refused first read rejects while the pane keeps its recovery", async () => {
  const ref = "local:pane-and-claim-first-read-failed";
  const fake = connectMutationClient();
  // The first read is held open until both callers have joined it, then answers
  // the hub's refusal by throwing at the handler - no promise of the test's own
  // is left rejected with nobody awaiting it.
  const bothJoined = deferred<void>();
  let reads = 0;
  fake.on("thread/read", async () => {
    reads += 1;
    if (reads > 1) return readResponse(ref, { name: "After retry" });
    await bothJoined.promise;
    throw historyFailedRejection();
  });
  const { retry, restore: restoreScheduler } = captureRetryScheduler();
  const pane = threadsStore.getState().ensureThread(ref);
  await flushUntil(() => reads === 1);
  // The resend claims the same destination while the pane's read is outstanding.
  const claim = acquireUnreadThreadClaim(ref);
  await pump();
  // Its outcome is observed from here on: the claim rejects the moment the
  // refusal lands, and attaching a handler only after the retry is armed would
  // leave that rejection unowned for the turns in between.
  const claimOutcome = settleOutcome(claim);

  // Both callers are parked on that one read; its refusal is the only answer
  // either of them gets until the recovery reads again.
  bothJoined.resolve();
  await flushUntil(() => retry.fire !== undefined);

  expect(await claimOutcome, "the routing claim learns the hub's answer without waiting out the pane's recovery").toBe(
    "rejected",
  );
  expect(reads, "the refusal is still the only read").toBe(1);
  expect(foregroundHolds(ref), "the claim's rollback leaves only the mounted pane's hold").toBe(1);
  expect(bodyOwners(ref), "the mounted pane still owns its bodies").toBe(1);

  retry.fire?.();
  await pane;

  expect(threadsStore.getState().threads.get(ref)?.name, "the pane's recovery hydrated its own view").toBe(
    "After retry",
  );
  expect(reads, "the recovery read once").toBe(2);
  restoreScheduler();
}, 20_000);

// A claim's retry record belongs to ONE attempt. A read a newer attempt
// superseded is not that attempt, so its late terminal answer must not settle
// the record the retry opened: the claim would reject over a refusal the hub
// gave a read nobody is waiting on any more, while the retry it is parked on
// was still going to answer.
test("a superseded read's late history failure does not settle the retry record a claim waits on", async () => {
  const ref = "local:superseded-late-refusal";
  const fake = connectMutationClient();
  // The delegate card's own read establishes the shared subscription first, so
  // the reads below are plain thread/reads that can overlap on the wire.
  fake.on("thread/read", () => readResponse(ref, { name: "Card" }));
  await threadsStore.getState().watchThread(ref);
  const supersededRead = deferred<ThreadReadResponse>();
  const retryRead = deferred<ThreadReadResponse>();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    // The claim's first read fails in transport and arms the retry record.
    if (reads === 1) throw new Error("transport died");
    // The pane's own read stays on the wire until the test answers it late.
    if (reads === 2) return supersededRead.promise;
    // The refresh that supersedes it fails in transport too, so the retry the
    // claim is parked on is still the one that will answer it.
    if (reads === 3) throw new Error("transport died");
    // The retry's own read is what finally answers the claim.
    return retryRead.promise;
  });
  const { retry, restore: restoreScheduler } = captureRetryScheduler();
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => retry.fire !== undefined);
  expect(reads, "the claim's read armed the retry record").toBe(1);
  // The claim is parked on that record's first model.
  await pump();

  // A pane opens and issues its own read, and a targeted refresh supersedes it
  // while that read is still on the wire.
  const pane = threadsStore.getState().ensureThread(ref);
  await flushUntil(() => reads === 2);
  void threadsStore
    .getState()
    .refreshThread(ref)
    .catch(() => undefined);
  await pump();
  expect(reads, "the refresh superseded the pane's read").toBe(3);
  // The pane closes, so its superseded read answers with no pane behind it and
  // the claim's routing hold is all that is left.
  threadsStore.getState().releaseThread(ref);
  expect(bodyOwners(ref), "the closed pane owns no bodies").toBe(0);
  expect(foregroundHolds(ref), "only the claim's routing hold keeps the ref").toBe(1);

  const claimOutcome = settleOutcome(claim);
  // The superseded read's late terminal answer: the hub refused a read whose
  // attempt a newer one replaced, so it says nothing about the retry record.
  supersededRead.reject(historyFailedRejection());
  expect(await claimOutcome, "the retry record survives a superseded read's refusal").toBe("pending");

  // The retry's own read succeeds and answers the claim.
  retry.fire?.();
  await flushUntil(() => reads === 4);
  retryRead.resolve(readResponse(ref, { name: "After retry" }));
  const lease = await claim;
  expect(threadsStore.getState().threads.get(ref)?.name, "the retry's read is the claim's answer").toBe("After retry");
  await pane;
  lease.release();
  threadsStore.getState().releaseWatchedThread(ref);
  restoreScheduler();
}, 20_000);

// A cached pane lookup belongs to the pane that opened it. A routing lease
// outlives the last pane during a resend, and it never reads bodies: a lookup
// that lands after that pane closed must not install a repository or attach a
// reader behind it.
test("a cached lookup finishing after the last pane closes does not attach a reader for the routing lease", async () => {
  const ref = "local:cached-lookup-after-close";
  const cache = createEntryCacheFixture();
  setProductionEntryCacheForTests(cache.cache);
  setSessionCacheLookupDeadlineForTests(30_000);
  const seed = await seedCachedShell(cache.cache, ref, 0);
  expect(seed.outcome, "the pane's cached shell is seeded durably").toBe("committed");
  // Hold the lookup open so the pane can close before it answers.
  const metadata = deferred<void>();
  const lookup = cache.cache.getSessionForRequestedRef.bind(cache.cache);
  let lookups = 0;
  vi.spyOn(cache.cache, "getSessionForRequestedRef").mockImplementation(async (clientId, requestedRef) => {
    lookups += 1;
    if (lookups === 1) await metadata.promise;
    return lookup(clientId, requestedRef);
  });
  const fake = connectMutationClient();
  const paneRead = deferred<ThreadReadResponse>();
  fake.on("thread/read", () => paneRead.promise);

  const pane = threadsStore.getState().ensureThread(ref);
  await flushUntil(() => lookups === 1);
  // A resend claims the same destination while the pane's lookup is pending.
  const claim = acquireUnreadThreadClaim(ref);
  await pump();
  expect(bodyOwners(ref), "the pane owns the live bodies").toBe(1);

  // The pane closes: its body ownership and its hold are gone, and only the
  // resend's routing lease keeps the ref alive.
  threadsStore.getState().releaseThread(ref);
  expect(bodyOwners(ref), "the pane's body ownership is gone").toBe(0);
  expect(foregroundHolds(ref), "the routing lease is all that holds the ref").toBe(1);

  // The held lookup answers now, after the pane it was opened for has closed.
  metadata.resolve();
  await pump(40);

  const route = entryRouteOf(ref);
  expect(route.identity, "the closed pane's lookup installs no identity").toBeUndefined();
  expect(route.repository, "the closed pane's lookup installs no repository").toBeUndefined();
  expect(route.reader, "the routing lease gets no reader behind the closed pane").toBeUndefined();

  paneRead.resolve(readResponse(ref));
  await pane;
  const lease = await claim;
  lease.release();
  await cache.dispose();
}, 20_000);

// A routing read publishes routing state and opens no cache, so a pane opening
// over one still owes the route's bounded lookup before it joins that read: the
// shell on disk is the pane's to restore, and the read already in flight is the
// answer it shares rather than a second one it spends beside it.
test("a pane opening over a routing read restores the cache and then joins that read", async () => {
  const ref = "local:pane-over-routing-read";
  const cache = createEntryCacheFixture();
  setProductionEntryCacheForTests(cache.cache);
  setSessionCacheLookupDeadlineForTests(30_000);
  const seed = await seedCachedShell(cache.cache, ref, 0);
  expect(seed.outcome, "the pane's cached shell is seeded durably").toBe("committed");
  const lookups = vi.spyOn(cache.cache, "getSessionForRequestedRef");
  const fake = connectMutationClient();
  const claimRead = deferred<ThreadReadResponse>();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    return claimRead.promise;
  });
  const claim = acquireUnreadThreadClaim(ref);
  await flushUntil(() => reads === 1);

  // The pane opens over the claim's read, which is still on the wire and has
  // published nothing: the lookup is this pane's own.
  const pane = threadsStore.getState().ensureThread(ref);
  const shellInstalled = await pumpIndexedDBUntil({
    turns: 40,
    until: () => entryRouteOf(ref).repository?.getSnapshot()?.history?.incarnation === "inc-1",
  });
  expect(shellInstalled, "the pane restored the cached shell").toBe(true);
  expect(lookups, "the pane spent the route's cached lookup").toHaveBeenCalled();
  expect(reads, "the pane joined the claim's read instead of issuing one").toBe(1);

  // The source's answer carries the fresh incarnation: it is what displaces the
  // older shell the pane restores, so the claim's read is the verification.
  claimRead.resolve(freshIncarnationAnswer(ref));
  await pane;
  const lease = await claim;
  expect(reads, "the joined read is still the only one on the wire").toBe(1);
  expect(threadsStore.getState().threads.has(ref), "the shared read hydrated the pane").toBe(true);
  lease.release();
  await cache.dispose();
}, 20_000);
