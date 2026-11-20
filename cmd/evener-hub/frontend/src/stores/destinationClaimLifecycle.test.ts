// The destination claim lease meets the hydration lifecycle: a claim waits on
// one owned hydration record, and every terminal answer for that record - the
// deletion fence, the hub's typed history failure - has to reach the wait, or
// the resend behind it stays busy holding a destination nothing will decide.
// The same lifecycle owns the registration a reconnect refresh leaves behind
// and the body ownership that decides when a pane's reader is still owed.

import "fake-indexeddb/auto";
import { type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { beforeEach, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { ALL_CAPABILITIES } from "../threadTestUtils";
import { resetConnectionStoreForTests } from "./connection";
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
import { backgroundHolds, foregroundHolds, holdHistory, releaseHistory } from "./threadOwners";
import {
  installHydrationRetrySchedulerForTests,
  markCacheSessionsDeleted,
  resetThreadsStoreForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "./threads";
import { connectMutationClient, readResponse } from "./threadsSessionActionsTestUtils";
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

// A deletion that lands in this tab while the claim's own read is still on the
// wire must refuse the claim rather than hand its lease out on a destination the
// fence has already retired: the read's answer publishes nothing, and the claim
// checks the fence again after every read it waits on.
test("a deletion during a claim's read rejects the claim instead of handing out a lease", async () => {
  const ref = "local:deleted-mid-wait";
  const fake = connectMutationClient();
  const claimRead = deferred<ThreadReadResponse>();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    return claimRead.promise;
  });
  const holdsBefore = backgroundHolds(ref);
  const claim = acquireUnreadThreadClaim(ref);
  const outcome = settleOutcome(claim);
  await flushUntil(() => reads === 1);

  markCacheSessionsDeleted([ref]);
  claimRead.resolve(readResponse(ref));

  expect(await outcome, "the deletion fence refuses the waiting claim").toBe("rejected");
  expect(reads, "the refused claim leaves no read behind it").toBe(1);
  expect(backgroundHolds(ref), "a refused claim leaves no lease behind").toBe(holdsBefore);
  expect(foregroundHolds(ref), "a refused claim leaves no pane hold behind").toBe(0);
}, 20_000);

// A claim's background lease keeps the model, the subscription and the route alive
// for a resend to dispatch on, and never owns the bodies. When the last pane
// closes during that resend, its reader must still be detached and its display
// pruned: owning the bodies is what keeps a reader, not holding the ref.
test("the last pane closing during a resend detaches its reader and prunes its display", async () => {
  const ref = "local:body-owner";
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse(ref, { name: "Body" }));
  await threadsStore.getState().ensureThread(ref);
  const route = entryRouteOf(ref);
  expect(foregroundHolds(ref), "a pane's acquisition owns the bodies").toBe(1);
  expect(route.reader, "a body owner keeps a reader").toBeDefined();
  // The resend's claim, taken over the same destination.
  const lease = await acquireUnreadThreadClaim(ref);

  // The pane closes: its body ownership and its hold are gone, the lease stays.
  threadsStore.getState().releaseThread(ref);

  expect(foregroundHolds(ref), "the pane's body ownership is gone").toBe(0);
  expect(route.reader, "a background lease does not keep a reader").toBeUndefined();
  // The route and its repository survive: the resend dispatches on them.
  expect(route.repository, "the background lease keeps the repository").toBeDefined();
  expect(threadsStore.getState().threads.has(ref), "the background lease keeps the model").toBe(true);
  lease.release();
  await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
}, 20_000);

// A history hold owns the bodies, so it keeps the reader even though no pane is
// open. The body question, not the keep-alive count, is what separates the two.
test("a history hold keeps the reader a background lease alone would release", async () => {
  const ref = "local:history-hold";
  const fake = connectMutationClient();
  fake.on("thread/read", () => readResponse(ref));
  await threadsStore.getState().ensureThread(ref);
  const route = entryRouteOf(ref);
  holdHistory(ref);

  threadsStore.getState().releaseThread(ref);

  expect(route.reader, "a history hold owns the live bodies").toBeDefined();
  expect(foregroundHolds(ref), "the pane's body ownership is gone").toBe(0);
  releaseHistory(ref);
  releaseEntryTranscriptReader(ref, true);
  expect(route.reader, "with no body owner left the reader is released").toBeUndefined();
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
test("a cached shell carrying a saved request generation does not discard the claim's read that verifies it", async () => {
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

// A resend sharing a pane's pending read takes a background lease, not the
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
  // only the resend's background lease keeps the destination alive.
  threadsStore.getState().releaseThread(ref);
  expect(foregroundHolds(ref), "the pane's body ownership is gone").toBe(0);
  expect(entryRouteOf(ref).reader, "the released pane's reader is detached").toBeUndefined();

  // The shared read lands, resolving both the closed pane's acquisition and the
  // resend's claim.
  read.resolve(readResponse(ref));
  await pane;
  const lease = await claim;
  await pump();

  expect(foregroundHolds(ref), "the closed pane never takes body ownership back").toBe(0);
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

// The composer's resend gate reads the destination's capabilities. A claim's
// read must not resolve on a cached shell a pane installed while that
// read awaited its durable authorities: the shell's incarnation is older, so
// the repository refuses the source response, and a claim that resolved on the
// shell would carry its empty capability set onto the lease - refusing a
// resend the hub's fresh snapshot allows.
test("a cached shell installed during a claim's read does not certify the claim", async () => {
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
// no read may follow a refusal the claim has been told about, and the lease it
// took must be gone before the rejection reaches the composer behind it.
test("a claim on an unopened destination rejects on the hub's terminal answer and leaves no read behind", async () => {
  const ref = "local:first-read-failed";
  const fake = connectMutationClient();
  let reads = 0;
  fake.on("thread/read", () => {
    reads += 1;
    throw historyFailedRejection();
  });
  const holdsBefore = backgroundHolds(ref);
  const claim = acquireUnreadThreadClaim(ref);

  expect(await settleOutcome(claim), "the hub's answer rejects the unopened claim").toBe("rejected");
  expect(reads, "the refusal is the only read").toBe(1);
  expect(backgroundHolds(ref), "a rejected claim leaves no lease behind").toBe(holdsBefore);
  await pump();
  expect(reads, "the rejected claim leaves no read behind it").toBe(1);
  expect(foregroundHolds(ref), "a rejected claim leaves no pane hold behind").toBe(0);
}, 20_000);

// A WebSocket drop is not an answer. The client rejects every pending request
// with the socket's own error, and the lease's bounded attempt ends there - but
// the hub never saw the destination's read, so the claim must not read that
// rejection as "not verified" and refuse a resend the replacement connection
// can still authorize. It keeps the lease, waits for the replacement
// connection, and joins its re-read of the still-held ref: the resend goes
// through, and the destination is read exactly once per connection.
test("a socket loss during the destination read lets the claim finish on the replacement connection's read", async () => {
  const ref = "local:socket-drop-during-claim";
  const fake = connectMutationClient();
  const firstRead = deferred<ThreadReadResponse>();
  const reads: number[] = [];
  fake.on("thread/read", () => {
    reads.push(reads.length + 1);
    if (reads.length === 1) return firstRead.promise;
    return readResponse(ref);
  });
  const claim = acquireUnreadThreadClaim(ref);
  let outcome: "pending" | "resolved" | "rejected" = "pending";
  void claim.then(
    () => {
      outcome = "resolved";
    },
    () => {
      outcome = "rejected";
    },
  );
  await flushUntil(() => reads.length === 1);

  // The socket drops mid-read: the client rejects the pending read, and the
  // same client schedules its own reconnect.
  fake.emitStateChange("reconnecting");
  firstRead.reject(new Error("AppwireClient: socket closed (code 1006)"));
  await pump();
  expect(outcome, "the claim is still waiting out the reconnect, not refusing").toBe("pending");

  // The reconnect lands and the store re-reads every ref it still holds. That
  // read is the claim's verification, and the claim joins it rather than
  // issuing one of its own.
  fake.emitReady();
  await flushUntil(() => outcome !== "pending");
  expect(outcome, "the replacement connection's read hands the destination out").toBe("resolved");
  expect(reads.length, "the dropped connection's read and the replacement's, and no duplicate").toBe(2);
  expect(backgroundHolds(ref), "the granted claim keeps exactly its own lease").toBe(1);
  expect(threadsStore.getState().threads.has(ref), "the destination is resident for the resend").toBe(true);
}, 20_000);

// The same reconnect, with the destination already being read for a reason of
// its own: a cached pane restoration holds a replacement read open when the
// claim arrives. A claim that issued its own tracked read would supersede that
// read's publication claim and spend a second wire round trip on one
// destination. It joins the read that exists instead.
test("a claim joins a replacement read already in flight instead of issuing a duplicate", async () => {
  const ref = "local:claim-joins-replacement-read";
  const cache = createEntryCacheFixture();
  setProductionEntryCacheForTests(cache.cache);
  setSessionCacheLookupDeadlineForTests(30_000);
  const seed = await seedCachedShell(cache.cache, ref, 0);
  expect(seed.outcome, "the pane's cached shell is seeded durably").toBe("committed");
  const fake = connectMutationClient();
  let reads = 0;
  const replacementRead = deferred<ThreadReadResponse>();
  fake.on("thread/read", () => {
    reads += 1;
    if (reads === 1) return replacementRead.promise;
    return readResponse(ref);
  });
  // The pane opens and its replacement read is the one on the wire; the shell
  // it restores is unverified, so the claim behind it has nothing to hand out.
  const pane = threadsStore.getState().ensureThread(ref);
  await flushUntil(() => reads === 1);
  const shellInstalled = await pumpIndexedDBUntil({
    turns: 40,
    until: () => entryRouteOf(ref).repository !== undefined,
  });
  expect(shellInstalled, "the pane restored its cached shell").toBe(true);
  expect(entryRouteOf(ref).cachedUnverified, "the shell advertises no verified capabilities").toBe(true);

  // The resend claims the destination while that read is outstanding.
  const claim = acquireUnreadThreadClaim(ref);
  await pump();
  expect(reads, "the claim joined the read already in flight").toBe(1);

  replacementRead.resolve(freshIncarnationAnswer(ref));
  await pane;
  const lease = await claim;
  expect(reads, "no duplicate read was issued behind the in-flight one").toBe(1);
  expect(entryRouteOf(ref).cachedUnverified, "the joined read verified the route").toBe(false);
  expect(threadsStore.getState().threads.has(ref), "the joined read published the destination").toBe(true);
  lease.release();
  threadsStore.getState().releaseThread(ref);
  await cache.dispose();
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

  expect(await claimOutcome, "the claim learns the hub's answer without waiting out the pane's recovery").toBe(
    "rejected",
  );
  expect(reads, "the refusal is still the only read").toBe(1);
  expect(foregroundHolds(ref), "the claim's rollback leaves only the mounted pane's hold").toBe(1);

  retry.fire?.();
  await pane;

  expect(threadsStore.getState().threads.get(ref)?.name, "the pane's recovery hydrated its own view").toBe(
    "After retry",
  );
  expect(reads, "the recovery read once").toBe(2);
  restoreScheduler();
}, 20_000);

// A cached pane lookup belongs to the pane that opened it. A claim's background
// lease outlives the last pane during a resend, and it never reads bodies: a
// lookup that lands after that pane closed must not install a repository or
// attach a reader behind it.
test("a cached lookup finishing after the last pane closes does not attach a reader for the background lease", async () => {
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
  expect(foregroundHolds(ref), "the pane owns the live bodies").toBe(1);

  // The pane closes: its body ownership and its hold are gone, and only the
  // resend's background lease keeps the ref alive.
  threadsStore.getState().releaseThread(ref);
  expect(foregroundHolds(ref), "the pane's body ownership is gone").toBe(0);
  expect(backgroundHolds(ref), "the claim's lease is all that holds the ref").toBe(1);

  // The held lookup answers now, after the pane it was opened for has closed.
  metadata.resolve();
  await pump(40);

  const route = entryRouteOf(ref);
  expect(route.identity, "the closed pane's lookup installs no identity").toBeUndefined();
  expect(route.repository, "the closed pane's lookup installs no repository").toBeUndefined();
  expect(route.reader, "the lease gets no reader behind the closed pane").toBeUndefined();

  paneRead.resolve(readResponse(ref));
  await pane;
  const lease = await claim;
  lease.release();
  await cache.dispose();
}, 20_000);

// A claim's read publishes routing state and opens no cache, so a pane opening
// over one still owes the route's bounded lookup before it reads: the shell on
// disk is the pane's to restore. The claim's read is background ownership, not a
// pane's, so the pane spends its own read rather than adopting one that was
// never issued on its behalf; both answers land on the one destination.
test("a pane opening over a claim's read restores the cache and then reads for itself", async () => {
  const ref = "local:pane-over-claim-read";
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

  // The source's answer carries the fresh incarnation: it is what displaces the
  // older shell the pane restores.
  claimRead.resolve(freshIncarnationAnswer(ref));
  await pane;
  const lease = await claim;
  expect(threadsStore.getState().threads.has(ref), "the pane restored and then hydrated the destination").toBe(true);
  lease.release();
  await cache.dispose();
}, 20_000);

// A saved-index fallback for the same owner is the continuation of the history
// the pane's cached shell restored, not a new history identity: the reducer
// merges it into that history (docs/product/history-content.md, "Saved fallback
// windows accumulate without erasing held history"). Replacing the shell's
// repository for it hydrates a fresh baseline from the fallback's own bounded
// window, dropping every restored turn outside that window, and the
// replacement's cache write pairs the fallback incarnation in the header with
// the live incarnation in the record - a pairing the next cached open refuses.
test("a saved-index fallback into a restored shell keeps its turns and its cache record", async () => {
  const ref = "local:saved-fallback-shell";
  const fixture = createEntryCacheFixture();
  setProductionEntryCacheForTests(fixture.cache);
  setSessionCacheLookupDeadlineForTests(30_000);
  try {
    // Nine recorded turns, each with one positioned body, so the record is a
    // real history the reopened shell can restore.
    const recordedTurns = Array.from({ length: 9 }, (_, index) => ({
      id: `turn_${index + 1}`,
      status: "completed",
      itemsView: "full",
      version: 1,
      items: [
        {
          id: `item-${index + 1}`,
          transcriptKey: `item-${index + 1}`,
          turnId: `turn_${index + 1}`,
          version: 1,
          type: "agentMessage" as const,
          text: `body-${index + 1}`,
          position: { entry: index + 1, item: 0 },
        },
      ],
    }));
    const keys = () =>
      threadsStore
        .getState()
        .threads.get(ref)
        ?.turns.flatMap((turn) => turn.items.map((item) => item.transcriptKey));
    const fake = connectMutationClient();
    fake.on("thread/read", () => readResponse(ref, { turns: recordedTurns }));
    await threadsStore.getState().ensureThread(ref);
    await settleEntryTranscriptForTests(ref);
    expect(keys(), "the first open restored every recorded body").toEqual(
      recordedTurns.map((turn) => turn.items[0]?.transcriptKey),
    );

    // The pane closes and reopens. The lookup restores the shell from disk, and
    // the hub's live read fails, so the same session is served from the saved
    // index: a one-entry window naming its own incarnation, with the
    // destination's Send revoked.
    threadsStore.getState().releaseThread(ref);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
    const savedFallback = {
      ...readResponse(ref, {
        turns: [recordedTurns[8]!],
        evener: { ref, capabilities: { ...ALL_CAPABILITIES, send: false }, queue: { revision: 0 } },
      }),
      servedFrom: "saved-index",
      fallbackReason: "live-read-failed",
      authoritative: false,
      bootGeneration: "99",
      epoch: 99,
      snapshot: { incarnation: "saved-inc", length: 9 },
      olderCursor: "saved-older",
    };
    // The live read is held so the shell lands first, as it does over a real
    // storage lookup; its answer is the fallback the hub substituted.
    const answer = deferred<void>();
    fake.on("thread/read", (params) =>
      answer.promise.then(() => ({ ...savedFallback, requestGeneration: params.requestGeneration })),
    );
    const opened = threadsStore.getState().ensureThread(ref);
    const shellInstalled = await pumpIndexedDBUntil({
      turns: 60,
      until: () => entryRouteOf(ref).repository?.getSnapshot()?.history?.incarnation === "inc-1",
    });
    expect(shellInstalled, "the reopen restored the cached shell").toBe(true);
    const shellRepository = entryRouteOf(ref).repository;
    expect(shellRepository, "the shell installed a repository").toBeDefined();
    answer.resolve();
    await opened;
    await settleEntryTranscriptForTests(ref);

    const route = entryRouteOf(ref);
    expect(route.repository, "the fallback applied to the shell's own repository").toBe(shellRepository);
    expect(keys(), "the turns outside the fallback's window survive").toEqual(
      recordedTurns.map((turn) => turn.items[0]?.transcriptKey),
    );
    expect(
      threadsStore.getState().threads.get(ref)?.capabilities.send,
      "the fallback's identity fields are applied",
    ).toBe(false);
    const metadata = await fixture.cache.getSessionMetadata(route.historyKey ?? "");
    expect(
      metadata?.cachedHeader?.history.incarnation,
      "the record's header names the incarnation its identity names, so a cached open accepts it",
    ).toBe(metadata?.resolvedIdentity.incarnation);

    // The same reopen, but the hub answers with a snapshot naming another
    // SESSION: that is a new history identity, not a continuation of the
    // restored one, so the shell must be replaced by a fresh baseline of the
    // response's own window rather than merged into it.
    threadsStore.getState().releaseThread(ref);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
    const replacement = {
      ...readResponse(ref, {
        turns: [recordedTurns[0]!],
        sessionId: "sess_replacement",
        evener: { ref, capabilities: ALL_CAPABILITIES, queue: { revision: 0 } },
      }),
      servedFrom: "live-daemon",
      bootGeneration: "99",
      epoch: 99,
      snapshot: { incarnation: "replacement-inc", length: 1 },
    };
    const replacementAnswer = deferred<void>();
    fake.on("thread/read", (params) =>
      replacementAnswer.promise.then(() => ({ ...replacement, requestGeneration: params.requestGeneration })),
    );
    const reopened = threadsStore.getState().ensureThread(ref);
    const shellAgain = await pumpIndexedDBUntil({
      turns: 60,
      until: () => entryRouteOf(ref).repository?.getSnapshot()?.history?.incarnation === "inc-1",
    });
    expect(shellAgain, "the reopen restored the shell the fallback left behind").toBe(true);
    const previousRepository = entryRouteOf(ref).repository;
    replacementAnswer.resolve();
    await reopened;
    await settleEntryTranscriptForTests(ref);

    expect(
      entryRouteOf(ref).repository,
      "a response naming another session replaces the shell with a fresh baseline",
    ).not.toBe(previousRepository);
    expect(keys(), "only the replacement's own window is served").toEqual(["item-1"]);
    expect(threadsStore.getState().threads.get(ref)?.history?.incarnation).toBe("replacement-inc");
  } finally {
    await fixture.dispose();
  }
}, 20_000);

// The other ordering of the same collision. A refresh's response can arrive
// FIRST and wait on its durable authorities while the pane's bounded lookup
// installs the cached shell. The accumulation decision is made after that wait,
// against the repository actually installed: a fallback naming the same owner
// and thread merges into the shell's history and keeps its key and deletion
// generation. Deciding it before the wait reads an empty route, replaces the
// shell with the fallback's bounded window, and drops every restored turn
// outside it (docs/product/history-content.md, "Saved fallback windows
// accumulate without erasing held history").
test("a saved-index fallback whose response precedes the shell install still merges into it", async () => {
  const ref = "local:fallback-authority-wait";
  const fixture = createEntryCacheFixture();
  setProductionEntryCacheForTests(fixture.cache);
  setSessionCacheLookupDeadlineForTests(30_000);
  try {
    const recordedTurns = Array.from({ length: 9 }, (_, index) => ({
      id: `turn_${index + 1}`,
      status: "completed",
      itemsView: "full",
      version: 1,
      items: [
        {
          id: `item-${index + 1}`,
          transcriptKey: `item-${index + 1}`,
          turnId: `turn_${index + 1}`,
          version: 1,
          type: "agentMessage" as const,
          text: `body-${index + 1}`,
          position: { entry: index + 1, item: 0 },
        },
      ],
    }));
    const keys = () =>
      threadsStore
        .getState()
        .threads.get(ref)
        ?.turns.flatMap((turn) => turn.items.map((item) => item.transcriptKey));
    const fake = connectMutationClient();
    fake.on("thread/read", () => readResponse(ref, { turns: recordedTurns }));
    await threadsStore.getState().ensureThread(ref);
    await settleEntryTranscriptForTests(ref);
    expect(keys(), "the first open restored every recorded body").toEqual(
      recordedTurns.map((turn) => turn.items[0]?.transcriptKey),
    );

    threadsStore.getState().releaseThread(ref);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
    const savedFallback = {
      ...readResponse(ref, {
        turns: [recordedTurns[8]!],
        evener: { ref, capabilities: { ...ALL_CAPABILITIES, send: false }, queue: { revision: 0 } },
      }),
      servedFrom: "saved-index",
      fallbackReason: "live-read-failed",
      authoritative: false,
      bootGeneration: "99",
      epoch: 99,
      snapshot: { incarnation: "saved-inc", length: 9 },
      olderCursor: "saved-older",
    };
    // The response answers at once; its authority read is held, so the pane's
    // lookup installs the shell while the response waits.
    fake.on("thread/read", (params) => ({ ...savedFallback, requestGeneration: params.requestGeneration }));
    const authorities = deferred<void>();
    const readAuthorities = fixture.cache.getClearEpoch.bind(fixture.cache);
    let authorityReads = 0;
    vi.spyOn(fixture.cache, "getClearEpoch").mockImplementation(async () => {
      authorityReads += 1;
      if (authorityReads === 1) await authorities.promise;
      return readAuthorities();
    });
    // The refresh's owner holds the ref without opening a pane, the way a
    // reconnect refresh runs for a destination someone still tracks.
    holdHistory(ref);
    const refresh = threadsStore.getState().refreshThread(ref);
    await vi.waitFor(() =>
      expect(authorityReads, "the fallback response is in and waiting on its authorities").toBe(1),
    );
    const pane = threadsStore.getState().ensureThread(ref);
    const shellInstalled = await pumpIndexedDBUntil({
      turns: 60,
      until: () => entryRouteOf(ref).repository?.getSnapshot()?.history?.incarnation === "inc-1",
    });
    expect(shellInstalled, "the pane installed the shell while the response waited").toBe(true);
    const shellRepository = entryRouteOf(ref).repository;
    expect(shellRepository, "the shell installed a repository").toBeDefined();
    // The binding the shell installed: the merge keeps exactly this history key
    // and this key's deletion generation.
    const shellKey = entryRouteOf(ref).historyKey;
    const shellGeneration = entryRouteOf(ref).historyGeneration;
    authorities.resolve();
    await refresh;
    await pane;
    await settleEntryTranscriptForTests(ref);

    const route = entryRouteOf(ref);
    expect(route.repository, "the fallback applied to the shell's own repository").toBe(shellRepository);
    expect(route.historyKey, "the fallback keeps the installed history key").toBe(shellKey);
    expect(route.historyGeneration, "the fallback keeps the installed deletion generation").toBe(shellGeneration);
    expect(keys(), "the turns outside the fallback's window survive").toEqual(
      recordedTurns.map((turn) => turn.items[0]?.transcriptKey),
    );
    expect(
      threadsStore.getState().threads.get(ref)?.capabilities.send,
      "the fallback's identity fields are applied",
    ).toBe(false);
    const metadata = await fixture.cache.getSessionMetadata(route.historyKey ?? "");
    expect(
      metadata?.cachedHeader?.history.incarnation,
      "the record's header names the incarnation its identity names",
    ).toBe(metadata?.resolvedIdentity.incarnation);
    releaseHistory(ref);
  } finally {
    await fixture.dispose();
  }
}, 20_000);
