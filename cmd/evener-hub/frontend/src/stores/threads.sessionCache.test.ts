// The cached-shell load seam (web session-history cache plan, Task 5; spec
// docs/superpowers/specs/2026-09-29-web-session-cache-design.md, "The load
// seam: the cached shell"): the bounded lookup on ensureThread, the shell it
// paints before the read resolves, the lease epoch it captures, and the
// races the spec's scenario 13 concedes — the shared-lookup join (Review
// Focus 5), the release during the lookup, and the lookup that lost its own
// deadline race.
import { type TestThreadOverrides, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  HistoryChanges,
  SnapshotIdentity,
  ThreadModel,
  ThreadReadResponse,
  Turn,
  TurnModel,
} from "@evener/appwire-client";
import { FakeClient, type RequestHandler } from "@evener/appwire-client/testing/fakeClient";
import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readComposerDraft, writeComposerDraft } from "../panes/session/composer/draft";
import { setCacheChannelFactoryForTests } from "./cacheLifetime";
import { connectionStore } from "./connection";
import {
  isEntryTranscriptLocalOpening,
  productionEntryTranscriptForTests,
  setProductionEntryCacheForTests,
} from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { settleProjectionWorkForTests } from "./projectionWork";
import { entryHistoryKey, type TranscriptResolvedIdentity } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { observeProductionEntryForTests } from "./testing/entryProductionHarness";
import { deleteIndexedDatabase } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  clearCachedSessions,
  installHydrationRetrySchedulerForTests,
  markCacheSessionsDeleted,
  pendingThreadHydrationBaseModelsForTests,
  pendingThreadHydrationUpdatesForTests,
  resetThreadsStoreForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";

// The response-level overrides a read fixture can carry besides the thread's
// own fields (threads.test.ts's readResponse shape plus the v6 identity a
// cached shell's reconciling read answers with).
type ReadResponseOverrides = {
  bootGeneration?: string;
  epoch?: number;
  snapshot?: SnapshotIdentity;
  requestGeneration?: number;
  olderCursor?: string;
  changes?: HistoryChanges;
};

// The default v6 identity every fixture in this file agrees on: boot
// generation "1", epoch 1, incarnation "inc-1", length 40 — the same
// identity seedCache records, so a readResponse() out of the box merges with
// a shell built from the default seed.
function readResponse(ref: string, overrides: ReadResponseOverrides & TestThreadOverrides = {}): ThreadReadResponse {
  const { bootGeneration, epoch, snapshot, requestGeneration, olderCursor, changes, ...threadOverrides } = overrides;
  return {
    thread: testThread(ref, threadOverrides),
    bootGeneration: bootGeneration ?? "1",
    epoch: epoch ?? 1,
    snapshot: snapshot ?? { incarnation: "inc-1", length: 40 },
    requestGeneration: requestGeneration ?? 1,
    ...(olderCursor === undefined ? {} : { olderCursor }),
    ...(changes === undefined ? {} : { changes }),
  };
}

// A recorded turn with one positioned item: the anchor the shell's record
// carries (and what newestItemPosition reads from it) needs a position, and
// the replay assertion reads the turn id. The shape satisfies both the model
// (TurnModel — the record's history turns) and the wire (Turn — a read
// response's window turns and a history/updated fold) so one fixture serves
// the seed, the read, and the notification.
function turnFixture(id: string, text = "cached turn"): TurnModel & Turn {
  return {
    id,
    status: "completed",
    itemsView: "full",
    items: [{ id: `item_${id}`, turnId: id, type: "agentMessage", text, position: { entry: 1, item: 0 } }],
    version: 2,
  };
}

// nextHandledRequest resolves with the params of the first request to
// `method` — same contract as threads.test.ts's helper; a second request
// re-resolves it to no effect, so the count assertions (not this promise)
// are what pins "exactly one".
// connectFakeClient wires a fresh FakeClient through the locked connect()
// entry point — the same path threads.ts's requireClient() rides. The file's
// notification helpers emit through the last client connected here.
let connectedFakeClient: FakeClient | undefined;

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  connectedFakeClient = fake;
  return fake;
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

// echoingReadHandler answers a thread/read with the file's fixture, echoing
// the request's generation: the store's latest-window contract discards a
// response to a superseded generation, and both a resync and the
// stale-cursor retry re-issue with a bumped one.
function _echoingReadHandler(
  overrides: ReadResponseOverrides & TestThreadOverrides = {},
): RequestHandler<"thread/read"> {
  return (params) => {
    if (params.ref === undefined) throw new Error("thread/read params must carry a ref");
    return readResponse(params.ref, { ...overrides, requestGeneration: params.requestGeneration });
  };
}

// The connected client a notification helper emits through; a missing client
// or model is a fixture wiring bug, not an assertion outcome.
function requireConnectedClient(caller: string): FakeClient {
  const client = connectedFakeClient;
  if (client === undefined) throw new Error(`${caller}: no client is connected`);
  return client;
}

function requireOpenHistory(
  caller: string,
  ref: string,
): { model: ThreadModel; history: NonNullable<ThreadModel["history"]> } {
  const model = threadsStore.getState().threads.get(ref);
  if (model === undefined) throw new Error(`${caller}: no open model for ${ref}`);
  const history = model.history;
  if (history === undefined) throw new Error(`${caller}: ${ref} holds no versioned history`);
  return { model, history };
}

// emitHistoryUpdated pushes a history/updated fold through the connected fake
// client's notification path — the reducer's live merge, the publication route
// the subscription exists to see (the notification handler's own setState,
// never putThreadModel). An explicit `entry` positions the fold's item the
// way the daemon's own pushes do: the turn scalar in `turns` and the item —
// the position's only carrier — in top-level `items` (the reducer's live
// merge reads exactly that split; an item nested inside the turn is dropped
// and the fold would sort by its version surrogate instead of its entry).
// Without an entry the fold keeps the file's original shape, exactly as
// Tasks 5-6 emitted it.
function _emitHistoryUpdated(ref: string, options: { fold: string; entry?: number }): void {
  const client = requireConnectedClient("emitHistoryUpdated");
  const { model, history } = requireOpenHistory("emitHistoryUpdated", ref);
  client.emitNotification({
    method: "history/updated",
    params: {
      threadId: model.threadId,
      ref,
      bootGeneration: history.bootGeneration,
      epoch: history.epoch,
      snapshot: { incarnation: history.incarnation ?? "inc-1", length: history.length },
      ...(options.entry === undefined
        ? { turns: [turnFixture(options.fold, "folded live")] }
        : {
            turns: [{ id: options.fold, status: "completed", itemsView: "full", version: 2 }],
            items: [
              {
                type: "agentMessage",
                id: `item_${options.fold}`,
                turnId: options.fold,
                text: "folded live",
                status: "completed",
                position: { entry: options.entry, item: 0 },
              },
            ],
          }),
    },
  } as AnyNotification);
}

// emitAppliedQueuedNotification carries a clientMutationId on a
// history/updated item — the only trigger that settles a durable row (and so
// unpins its ref) without a successful authoritative read
// (threads.test.ts's appliedItemNotification contract).
// Subscription awaits, not polls: each resolves exactly when the store's own
// publication or removal runs (the retirement suite's awaitHydrationBump
// idiom), which is what lets a test hold a deterministic position between a
// notification it emits and the model state it asserts — under fake timers,
// where vi.waitFor's own polling clock never ticks.
// resolveEverything settles what an awaited ensureThread leaves in flight —
// the load seam's bounded lookup and any IndexedDB delivery still queued — so
// the publication the read produced is fully on the store before the test
// advances the debounce.
async function _resolveEverything(_fake: FakeClient): Promise<void> {
  // The tracker's settle waits for in-flight storage work by yielding through
  // a MessageChannel hop, which the scoped fake timers above do not fake —
  // so it both flushes the load seam's own lookup transaction and lets every
  // queued IndexedDB delivery land, which nextMacrotask (a faked setTimeout)
  // could not do here.
  await settleProjectionWorkForTests();
}

let _pendingReadRef: string | undefined;
let _pendingReadGeneration: number | undefined;
let _resolvePendingReadFn: ((response: ThreadReadResponse) => void) | undefined;
const readArmedWaiters: Array<() => void> = [];

// awaitReadArmed resolves on the scripted read handler's next invocation —
// the deterministic witness that a read actually reached the wire, which a
// retry arms only after its own await chain (never synchronously inside
// runScheduledHydrationRetryForThisFile).
// seedAndReload is Task 5's replay bed as the write seam's fixture: a prior
// tab's record is seeded, the store reloads against a scripted read, and the
// cached shell publishes before that read resolves. The seeded row is then
// deleted from storage: the shell's model is already in memory, and without
// the deletion no read-back could tell "the shell publication wrote nothing"
// apart from "it rewrote the seed" — the assertion the shell gate's tests
// stand on.
// resolvePendingRead answers the deferred read with the file's default
// identity (the seed's own), echoing the request's generation so the answer
// merges rather than being discarded as superseded.
// The hydration retry scheduler is injected so every retry in this suite is
// driven by an explicit call, never by elapsed time (threads.test.ts's own
// rationale): a scripted failed read arms a retry this file invokes by hand,
// and installing it for every test keeps the production backoff's real
// setTimeout out of the suite entirely.
interface ScheduledHydrationRetry {
  attempt: number;
  retry: () => void;
  cancelled: boolean;
}

let scheduledHydrationRetries: ScheduledHydrationRetry[] = [];

// runScheduledHydrationRetryForThisFile invokes exactly one scheduled retry,
// proving first that it exists and was not cancelled — the same guard
// threads.test.ts's own driver keeps, so a cancelled entry that still fires
// cannot make an assertion vacuous.
let restoreHydrationRetryScheduler: (() => void) | null = null;

beforeEach(async () => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  connectedFakeClient = undefined;
  _pendingReadRef = undefined;
  _pendingReadGeneration = undefined;
  _resolvePendingReadFn = undefined;
  _pendingReloadResolve = undefined;
  _pendingReloadGeneration = undefined;
  for (const resolve of readArmedWaiters.splice(0)) resolve();
  scheduledHydrationRetries = [];
  restoreHydrationRetryScheduler = installHydrationRetrySchedulerForTests((attempt, retry) => {
    const scheduled: ScheduledHydrationRetry = { attempt, retry, cancelled: false };
    scheduledHydrationRetries.push(scheduled);
    return () => {
      scheduled.cancelled = true;
    };
  });
  await deleteMutationDatabase();
  await deleteIndexedDatabase("evener-session-cache");
});

afterEach(() => {
  restoreHydrationRetryScheduler?.();
  restoreHydrationRetryScheduler = null;
  vi.restoreAllMocks();
});

describe("cached shell load seam", () => {
  it("miss: an absent ref arms the ordinary cold hydration exactly as today (no heldSnapshot)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) =>
      readResponse((params as { ref: string }).ref, { snapshot: { incarnation: "inc-1", length: 1 } }),
    );
    await threadsStore.getState().ensureThread("local:cold");
    const call = fake.calls.find((c) => c.method === "thread/read");
    expect((call?.params as { heldSnapshot?: unknown } | undefined)?.heldSnapshot).toBeUndefined();
  });
});

// The 6d removal deleted the legacy debounced write seam. Its remaining cases
// are retired under disposition; the entry route's equivalents are:
// Ruling 33 disposition 5: mapped. Original: flush: the pinned drain
// (dropUnpinnedModel) commits a pending debounced write before the model leaves
// the map. Entry protection: src/stores/threads.entryProduction.test.ts —
// "ordinary mutation-pin release saves a fresh final read before removing the
// last pinned model".
// Ruling 33 disposition 14: mapped. Original: a deleteSession success removes
// the record, cancels the pending timer and arms deletedRefs. Entry protection:
// src/stores/threads.entryProduction.test.ts — "ordinary deletion removes bound
// histories and durably refuses the captured pre-delete write".

// Parked-read helpers remain for the surviving legacy cached-shell tests.

// The reload bed's parked read: whichever deferred read a fixture parks — the
// shell's own, the stale-snapshot retry's, or the hand-driven failure
// retry's — the scenario resolves it through resolveReloadRead, which echoes
// the parked request's own generation back (the store's latest-window
// contract discards a response to a superseded generation; resolvePendingRead's
// own rule).
let _pendingReloadResolve: ((response: ThreadReadResponse) => void) | undefined;
let _pendingReloadGeneration: number | undefined;

// The live gap rule's default-blob cases are individually mapped below.
// Ruling 33 disposition 6: mapped. Original: scenario 6: an overlapping window merges and the deepest cursor is kept (the pinned reducer rule)
// Overlapping recorded windows preserve both older pages and the deepest cursor, without a blob-shell anchor.
// Entry protection: src/stores/threads.entryProduction.test.ts — "ordinary overlapping refresh keeps two loaded pages and the deepest usable cursor".
// Ruling 33 disposition 7: retired. Original: scenario 6: a window beginning exactly at the captured position's successor replaces (the accepted abut cost)
// The accepted one-item abut cost is specific to the blob captured-position predicate, entry paging uses causal certificates and reconciles an uncertain boundary instead.
// Entry protection: src/stores/threads.history.test.ts — "a non-authoritative ambiguity refresh retires the boundary and the next action pages on the new cursor".
// Ruling 33 disposition 8: retired. Original: scenario 7: a window starting entirely above the anchor replaces, and the response's cursor is taken
// The entirely-above-blob-anchor replacement predicate is retired, currentness instead follows the new source baseline and its returned cursor.
// Entry protection: src/stores/threads.history.test.ts — "a same-thread reconnect with unchanged source identifiers retires the outgoing page authority".
// Ruling 33 disposition 9: mapped. Original: scenario 7: a fold newer than the response replaces and replays the fold tail
// Preserve actual arrived input outside a delayed response window in UI and disk, blob-shell replacement and timer plumbing are retired.
// Entry protection: src/stores/threads.entryProduction.test.ts — "ordinary reconnect keeps a same-turn tail outside the recovered window without fabricating a missing row".
// Ruling 33 disposition 10: mapped. Original: scenario 7: a fold arriving with a dropped notification behind it replays what arrived, carrying today's hole
// Mirror the tail that arrived and do not invent the missing item, the original assertion did not prohibit a later suffix certificate.
// Entry protection: src/stores/threads.entryProduction.test.ts — "ordinary reconnect keeps a same-turn tail outside the recovered window without fabricating a missing row".
// Ruling 33 disposition 11: mapped. Original: scenario 8: stale identity: the server rejects the held snapshot, the retry replaces, and the cached turns do not survive
// A real stale-cursor rejection still drops held identity and stale older turns, its replacement intent now reaches the entry owner.
// Entry protection: src/stores/threads.history.test.ts — "a stale-cursor rejection retries thread/read without the held snapshot".
// Entry protection: src/stores/threads.history.test.ts — "a stale-cursor retry discards an older held turn outside the recovered window, not merges it".
// Ruling 33 disposition 12: mapped. Original: an empty record takes the ordinary cold merge and the predicate holds (the gap rule's empty case, scenario 15's tail)
// An empty history still cold-opens safely and accepts later input, the empty whole-blob anchor predicate itself is retired.
// Entry protection: src/stores/threads.entryProduction.test.ts — "ordinary empty source cold-opens and accepts later recorded input without a blob anchor".

// The scroll-back bed (Task 8; spec, "Scroll-back: the deepest held cursor",
// Testing scenario 12). olderPageResponse answers a thread/turns/list with
// the wire shape the hub stamps on every page it serves: identity rides the
// response (source.go's StampPage — bootGeneration, epoch, snapshot), the
// page's turns live in `data`, and an exhausted page is `data: []` with no
// nextCursor, "the transcript's first item ... as the daemon itself answers".
// The identity is the file's default one so a page into the settled window's
// versioned history classifies as a same-identity merge (pageDisposition),
// not as a boot-generation stranger.
// The scroll-back volume case preserves disk-only display before source success.
// Ruling 33 disposition 13: mapped. Original: renders window plus both cached pages, refuses loadOlderTurns while the shell is unverified, then continues from the settled cursor
// Ruling 34: start with real committed entries and no retained model or repository, observe all nine turns and the deepest cursor while the source read stays pending, withhold paging, then continue from the settled cursor. No visible-content guarantee is retired.
// Entry protection: src/stores/threads.entryProduction.test.ts — "ordinary disk-only opening displays the window and both cached pages before source success, withholds paging, then uses the settled cursor".
// Entry protection: src/stores/threads.entryProduction.test.ts — "ordinary overlapping refresh keeps two loaded pages and the deepest usable cursor".
// Entry protection: src/stores/threads.history.test.ts — "a same-connection close/reopen retains the cached older pages and their usable boundary, and the next older request carries the stored boundary cursor".
// Entry protection: src/stores/threads.history.test.ts — "an already-read local page does not publish across a pending hydration".
// Entry protection: src/stores/threads.history.test.ts — "a non-authoritative ambiguity refresh retires the boundary and the next action pages on the new cursor".

// The deletion bed (Task 9; spec, "The write seam" deletion bullets): the
// response-keyed hook, the deletion fence's cache re-arm, and cross-tab
// propagation with its idempotent heal. fencedReadError builds the hub's
// durable deletion-fence rejection exactly the way threads.test.ts's own
// markThreadDeletedIfFenced coverage does (data.mutationOutcome ===
// "targetDeleted" — cmd/evener-hub's deletionFenceError).
// TestBroadcastChannel is mutationOutbox.test.ts's shape: an EventTarget
// subclass with a peers set, so posts from a "peer" tab reach every channel
// of the same name — this tab's singleton handler included.
class TestBroadcastChannel extends EventTarget {
  constructor(
    readonly name: string,
    private readonly peers: Set<TestBroadcastChannel>,
    private readonly onPosted?: (message: unknown) => void,
  ) {
    super();
    this.peers.add(this);
  }

  postMessage(message: unknown): void {
    this.onPosted?.(message);
    for (const peer of this.peers) {
      if (peer !== this && peer.name === this.name) {
        peer.dispatchEvent(new MessageEvent("message", { data: message }));
      }
    }
  }

  close(): void {
    this.peers.delete(this);
  }
}

const CACHE_CHANNEL_NAME = "evener.session-cache.v1";

// installTestCacheChannel mirrors the outbox's injected-factory pattern: the
// singleton's channel is created inside the peers set (the factory seam), a
// peer tab posts into it, and `posted` records everything this tab sent.
function installTestCacheChannel(): { peer: { post(message: unknown): void }; posted: unknown[] } {
  const peers = new Set<TestBroadcastChannel>();
  const posted: unknown[] = [];
  const peer = new TestBroadcastChannel(CACHE_CHANNEL_NAME, peers);
  setCacheChannelFactoryForTests(
    (name: string) =>
      new TestBroadcastChannel(name, peers, (message: unknown) => {
        posted.push(message);
      }) as unknown as BroadcastChannel,
  );
  return { peer: { post: (message: unknown) => peer.postMessage(message) }, posted };
}

// installNoCacheChannel is the webview: the factory answers null, exactly as
// the typeof BroadcastChannel guard does where the API is missing.
function installNoCacheChannel(): void {
  setCacheChannelFactoryForTests(() => null);
}

// seedProductionEntryHistory commits one entry history for `ref` into an entry
// cache installed on the ordinary route, so a channel deletion has a real
// durable history to heal (deleteCachedRefs discovers it by requested-ref
// metadata).
async function seedProductionEntryHistory(cache: SessionEntryCache, ref: string): Promise<string> {
  const resolvedIdentity: TranscriptResolvedIdentity = {
    clientId: "client",
    hostId: "local",
    sessionId: `sess_${ref}`,
    incarnation: "inc-1",
  };
  const historyKey = entryHistoryKey(resolvedIdentity);
  const item = turnFixture("turn_seed").items[0]!;
  const outcome = await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [{ historyKey, transcriptKey: "seed-item", position: { entry: 1, item: 0 }, version: 1, item }],
    sessionMetadata: {
      historyKey,
      requestedRefs: [{ clientId: "client", ref }],
      resolvedIdentity,
      savedAt: Date.now(),
      accessedAt: Date.now(),
      formatVersion: 1,
    },
  });
  expect(outcome).toMatchObject({ outcome: "committed" });
  return historyKey;
}

describe("deletion", () => {
  // Ruling 33 disposition 14: mapped. Original: a deleteSession success removes
  // the record, cancels the pending timer and arms deletedRefs. Entry
  // protection: threads.entryProduction.test.ts — "ordinary deletion removes
  // bound histories and durably refuses the captured pre-delete write". The
  // retired debounced-write cases (queued task refused, open transaction
  // ordered before the delete) asserted the deleted legacy write seam.
  // Ruling 33 disposition 15/17: mapped. Entry protection:
  // threads.entryProduction.test.ts — "ordinary disk-only read-proven deletion
  // keeps cached content visible while healing entry storage and broadcasting
  // once" and "ordinary sibling deletion heals entry rows and retires the
  // still-held publication and write claims".

  it("the cache channel ignores wrong versions, invalid payloads and its own source id, and heals entry rows on a valid deletion", async () => {
    const entryDatabase = "entry-channel-validation-test";
    const cache = new SessionEntryCache({ databaseName: entryDatabase });
    setProductionEntryCacheForTests(cache);
    try {
      const { peer, posted } = installTestCacheChannel();
      markCacheSessionsDeleted(["local:source-id"]);
      const own = posted[0] as { sourceId: string };
      const ref = "local:channel-validation";
      const historyKey = await seedProductionEntryHistory(cache, ref);
      const message = { version: 1, sourceId: "peer", kind: "deletion", refs: [ref] };
      for (const invalid of [
        null,
        { ...message, version: 2 },
        { ...message, sourceId: "" },
        { ...message, sourceId: undefined },
        { ...message, sourceId: own.sourceId },
        { ...message, kind: "unknown" },
        { ...message, refs: ref },
        { version: 1, sourceId: "peer", kind: "clear", epoch: "1" },
      ])
        peer.post(invalid);
      await settleProjectionWorkForTests();
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBeUndefined();
      await expect(cache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
      peer.post(message);
      await vi.waitFor(async () => expect(await cache.getLatest(historyKey, 10)).toEqual([]));
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);
    } finally {
      cache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it.each([null, 7])("the cache v1 envelope still accepts source %s and extra fields", async (sourceId) => {
    const entryDatabase = `entry-channel-compat-${String(sourceId)}-test`;
    const cache = new SessionEntryCache({ databaseName: entryDatabase });
    setProductionEntryCacheForTests(cache);
    try {
      const { peer } = installTestCacheChannel();
      const ref = `local:channel-compat-${String(sourceId)}`;
      const historyKey = await seedProductionEntryHistory(cache, ref);
      peer.post({ version: 1, sourceId, kind: "deletion", refs: [ref], extra: true });
      await vi.waitFor(async () => expect(await cache.getLatest(historyKey, 10)).toEqual([]));
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);
    } finally {
      cache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  // Runs on the entry route: the channel factory answers null, as the typeof
  // guard does in a webview. The deletion must still clear the ref's entry
  // history locally and arm deletedRefs, with nothing thrown.
  it("BroadcastChannel absent: deletion still works locally and nothing throws (Review Focus 2)", async () => {
    installNoCacheChannel();
    const entryDatabase = "entry-no-channel-test";
    const cache = new SessionEntryCache({ databaseName: entryDatabase });
    setProductionEntryCacheForTests(cache);
    try {
      const ref = "local:quiet";
      const historyKey = await seedProductionEntryHistory(cache, ref);
      markCacheSessionsDeleted([ref]);
      await vi.waitFor(async () => expect(await cache.getLatest(historyKey, 10)).toEqual([]));
      expect(threadsStore.getState().deletedRefs.has(ref)).toBe(true);
    } finally {
      cache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });
});

describe("ordinary entry transcript route", () => {
  it("keeps background history in the repository until a local open restores it without read/page activation", async () => {
    const selected = "local:entry-background-only";
    const entryDatabase = "entry-background-only-test";
    const baselineBody = "RECORDED_BACKGROUND_BASELINE_BODY";
    const liveRecordedBody = "LIVE_RECORDED_BACKGROUND_BODY";
    const overlayBody = "ACTIVE_BACKGROUND_OVERLAY_BODY";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    const fake = connectFakeClient();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      const baseline = turnFixture("entry-background-baseline", baselineBody);
      return readResponse(params.ref ?? selected, {
        status: { type: "active" },
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            ...baseline,
            items: [
              {
                ...baseline.items[0]!,
                transcriptKey: "entry-background-baseline-item",
                version: 1,
              },
            ],
          },
        ],
        requestGeneration: params.requestGeneration,
      });
    });

    const lease = acquireBackgroundThread(selected);
    let opened = false;
    try {
      await lease.ready;
      expect(readCount).toBe(1);
      const backgroundModel = threadsStore.getState().threads.get(selected);
      if (!backgroundModel) throw new Error("real background demand did not publish the selected thread");
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId: backgroundModel.threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "entry-live-background", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "live-background-item",
              transcriptKey: "live-background-item",
              turnId: "entry-live-background",
              version: 2,
              text: liveRecordedBody,
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      fake.emitNotification({
        method: "overlay/upserted",
        params: {
          ref: selected,
          threadId: backgroundModel.threadId,
          item: {
            key: "background-overlay-key",
            kind: "stream",
            turnId: "entry-background-baseline",
            roundId: "background-round",
            streamId: "background-stream",
            item: {
              type: "agentMessage",
              id: "background-overlay-item",
              turnId: "entry-background-baseline",
              text: overlayBody,
              status: "inProgress",
            },
          },
        },
      } as AnyNotification);

      await new Promise<void>((resolve) => setTimeout(resolve, 1_050));
      await fixture.settled();
      await settleBackgroundTranscriptsForTests();
      await settleProjectionWorkForTests();

      const selectedStore = threadsStore.getState();
      const repositoryModel = fixture.inspect().model;
      const selectedThreadModel = selectedStore.threads.get(selected);
      const pendingUpdates = pendingThreadHydrationUpdatesForTests();
      const pendingBaseModels = pendingThreadHydrationBaseModelsForTests();
      const legacyResidency = backgroundTranscriptSnapshotForTests();
      const selectedBodyOwners = [
        ["entry repository", repositoryModel],
        ["thread store", selectedThreadModel],
        ["watched thread store", selectedStore.watchedThreads.get(selected)],
        ["pending notification updates", pendingUpdates],
        ["pending hydration base models", pendingBaseModels],
        ["legacy residency pending updates", legacyResidency.refs.get(selected)?.pending],
        ["legacy residency in-flight updates", legacyResidency.refs.get(selected)?.inFlight],
      ] as const;
      const completedBodies = [baselineBody, liveRecordedBody];
      const completedBodyOwners = selectedBodyOwners
        .filter(
          ([, owner]) => owner !== undefined && completedBodies.some((body) => JSON.stringify(owner).includes(body)),
        )
        .map(([name]) => name);
      const selectedBeforeOpen = JSON.stringify([repositoryModel, selectedThreadModel]);
      expect({
        completedBodyOwners,
        readerAttached: fixture.inspect().readerRetention !== undefined,
        legacyResidencyOwnsSelectedRef: legacyResidency.refs.has(selected),
        running: selectedThreadModel?.status.type,
        overlayPreserved: selectedBeforeOpen.includes(overlayBody),
      }).toEqual({
        completedBodyOwners: [],
        readerAttached: false,
        legacyResidencyOwnsSelectedRef: false,
        running: "active",
        overlayPreserved: true,
      });

      const traceBoundary = fake.calls.length;
      await threadsStore.getState().ensureThread(selected);
      opened = true;
      await fixture.settled();
      await settleProjectionWorkForTests();
      const locallyOpened = threadsStore.getState().threads.get(selected);
      const localSnapshot = JSON.stringify(locallyOpened ?? null);
      expect(localSnapshot).toContain(baselineBody);
      expect(localSnapshot).toContain(liveRecordedBody);
      expect(localSnapshot).toContain(overlayBody);
      expect(locallyOpened?.status.type).toBe("active");
      expect(fixture.inspect().readerRetention).toBeDefined();
      expect(
        fake.calls
          .slice(traceBoundary)
          .filter((call) => call.method === "thread/read" || call.method === "thread/turns/list"),
      ).toEqual([]);
    } finally {
      if (opened) threadsStore.getState().releaseThread(selected);
      await lease.release();
      await fixture.settled();
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("owns a selected recorded update through the repository and keeps the ordinary recorded reducer from being a second owner", async () => {
    const selected = "local:entry-exclusive-owner";
    const entryDatabase = "entry-exclusive-owner-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    const fake = connectFakeClient();
    const baseline = turnFixture("entry-exclusive-baseline", "BASELINE_RECORDED_BODY");
    fake.on("thread/read", (params) =>
      readResponse(params.ref ?? selected, {
        status: { type: "active" },
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ ...baseline, items: [{ ...baseline.items[0]!, transcriptKey: "exclusive-baseline", version: 1 }] }],
        requestGeneration: params.requestGeneration,
      }),
    );
    const lease = acquireBackgroundThread(selected);
    let opened = false;
    try {
      await lease.ready;
      await threadsStore.getState().ensureThread(selected);
      opened = true;
      await fixture.settled();
      await settleProjectionWorkForTests();
      // A real reader holds the ref, so the repository's publication is live.
      expect(fixture.inspect().readerRetention).toBeDefined();
      const threadId = `thr_${selected}`;

      // A valid recorded update the repository accepts and publishes; the
      // store follows that ongoing publication.
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "entry-accepted", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "accepted-item",
              transcriptKey: "accepted-item",
              turnId: "entry-accepted",
              version: 2,
              text: "REPOSITORY_OWNED_BODY",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await fixture.settled();
      await settleProjectionWorkForTests();
      expect(JSON.stringify(fixture.inspect().model ?? null)).toContain("REPOSITORY_OWNED_BODY");
      expect(JSON.stringify(threadsStore.getState().threads.get(selected) ?? null)).toContain("REPOSITORY_OWNED_BODY");

      // A recorded frame the repository refuses: its resolved thread id is not
      // this binding's. The ordinary reducer routes by ref and would fold it
      // anyway, making the store a second owner of recorded input; the store
      // must defer to the repository's refusal instead.
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId: "thr-not-this-binding",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 3 },
          turns: [{ id: "entry-refused", status: "completed", itemsView: "full", version: 3 }],
          items: [
            {
              type: "agentMessage",
              id: "refused-item",
              transcriptKey: "refused-item",
              turnId: "entry-refused",
              version: 3,
              text: "SECOND_OWNER_BODY",
              position: { entry: 3, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await fixture.settled();
      await settleProjectionWorkForTests();
      expect(JSON.stringify(fixture.inspect().model ?? null)).not.toContain("SECOND_OWNER_BODY");
      expect(JSON.stringify(threadsStore.getState().threads.get(selected) ?? null)).not.toContain("SECOND_OWNER_BODY");

      // Non-history input keeps its ordinary route: an active overlay and a
      // live status still reach the selected model while recorded history is
      // repository-exclusive.
      fake.emitNotification({
        method: "overlay/upserted",
        params: {
          ref: selected,
          threadId,
          item: {
            key: "exclusive-overlay-key",
            kind: "stream",
            turnId: "entry-accepted",
            roundId: "exclusive-round",
            streamId: "exclusive-stream",
            item: {
              type: "agentMessage",
              id: "exclusive-overlay-item",
              turnId: "entry-accepted",
              text: "LIVE_OVERLAY_BODY",
              status: "inProgress",
            },
          },
        },
      } as AnyNotification);
      fake.emitNotification({
        method: "thread/status/changed",
        params: { ref: selected, threadId, status: { type: "active" }, activeTurnId: "entry-accepted" },
      } as AnyNotification);
      await settleProjectionWorkForTests();
      const live = JSON.stringify(threadsStore.getState().threads.get(selected) ?? null);
      expect(live).toContain("LIVE_OVERLAY_BODY");
      expect(threadsStore.getState().threads.get(selected)?.status.type).toBe("active");
    } finally {
      if (opened) threadsStore.getState().releaseThread(selected);
      await lease.release();
      await fixture.settled();
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("replays recorded and overlay updates buffered during the accepted first read before local opening", async () => {
    const selected = "local:entry-hydration-replay";
    const entryDatabase = "entry-hydration-replay-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    let releaseRead: (() => void) | undefined;
    let heldResponse: ThreadReadResponse | undefined;
    try {
      const fake = connectFakeClient();
      fake.on(
        "thread/read",
        (params) =>
          new Promise<ThreadReadResponse>((resolve) => {
            releaseRead = () => {
              heldResponse = readResponse(selected, {
                epoch: 0,
                snapshot: { incarnation: "inc-1", length: 1 },
                turns: [
                  {
                    ...turnFixture("entry-A", "STALE_A_BODY"),
                    items: [
                      {
                        ...turnFixture("entry-A", "STALE_A_BODY").items[0]!,
                        transcriptKey: "replay-A",
                        version: 1,
                      },
                    ],
                  },
                ],
                requestGeneration: params.requestGeneration,
              });
              resolve(heldResponse);
            };
          }),
      );
      const hydration = threadsStore.getState().ensureThread(selected);
      await vi.waitFor(() => expect(releaseRead).toBeTypeOf("function"));

      const threadId = `thr_${selected}`;
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId,
          bootGeneration: "1",
          epoch: 0,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "entry-B", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "replay-B",
              transcriptKey: "replay-B",
              turnId: "entry-B",
              version: 2,
              text: "RECORDED_B_BODY",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      fake.emitNotification({
        method: "overlay/upserted",
        params: {
          ref: selected,
          threadId,
          item: {
            key: "replay-overlay-key",
            kind: "stream",
            turnId: "entry-B",
            roundId: "replay-round",
            streamId: "replay-stream",
            item: {
              type: "agentMessage",
              id: "replay-overlay",
              turnId: "entry-B",
              text: "OVERLAY_LIVE_BODY",
              status: "inProgress",
            },
          },
        },
      } as AnyNotification);
      await vi.waitFor(() => expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(2));
      expect(heldResponse).toBeUndefined();

      releaseRead?.();
      if (!heldResponse) throw new Error("held source response was not captured");
      await hydration;
      await fixture.settled();
      await settleProjectionWorkForTests();

      const repositoryBeforeOpen = JSON.stringify(fixture.inspect().model ?? null);
      expect(repositoryBeforeOpen).toContain("STALE_A_BODY");
      expect(repositoryBeforeOpen).toContain("RECORDED_B_BODY");
      expect(repositoryBeforeOpen).toContain("OVERLAY_LIVE_BODY");

      await threadsStore.getState().ensureThread(selected);
      const locallyOpened = JSON.stringify(threadsStore.getState().threads.get(selected) ?? null);
      expect(locallyOpened).toContain("RECORDED_B_BODY");
      expect(locallyOpened).toContain("OVERLAY_LIVE_BODY");
    } finally {
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("routes every ref through the entry repository and accepts the current wire baseline", async () => {
    const selected = "local:entry-selected";
    const other = "local:ordinary";
    const entryDatabase = "entry-transcript-fixture-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) =>
        readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [
            {
              ...turnFixture("entry-baseline", "current source baseline"),
              items: [
                {
                  ...turnFixture("entry-baseline", "current source baseline").items[0]!,
                  transcriptKey: "entry-baseline-item",
                  version: 1,
                },
              ],
            },
          ],
          requestGeneration: params.requestGeneration,
        }),
      );
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();

      const current = threadsStore.getState().threads.get(selected)!;
      requireConnectedClient("entry fixture notification").emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId: current.threadId,
          bootGeneration: current.history!.bootGeneration,
          epoch: current.history!.epoch,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "entry-live", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "entry-live-item",
              transcriptKey: "entry-live-item",
              version: 1,
              turnId: "entry-live",
              text: "current live notification",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await fixture.settled();

      const identity: TranscriptResolvedIdentity = {
        clientId: location.origin,
        hostId: "local",
        sessionId: `sess_${selected}`,
        incarnation: "inc-1",
      };
      await expect(entryCache.getLatest(entryHistoryKey(identity), 10)).resolves.toEqual(
        expect.arrayContaining([
          expect.objectContaining({ item: expect.objectContaining({ text: "current source baseline" }) }),
          expect.objectContaining({ item: expect.objectContaining({ text: "current live notification" }) }),
        ]),
      );
      await threadsStore.getState().ensureThread(other);
      expect(productionEntryTranscriptForTests(other).repository).toBeDefined();
    } finally {
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("settles only the matching opening: an older settle leaves the newer hold, then clearing publishes", async () => {
    const selected = "local:entry-overlap-token";
    const ordinary = "local:entry-opening-ordinary";
    const entryDatabase = "entry-overlap-token-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    // Hold each real opening at its own cache read so the two can be settled
    // in a controlled order.
    const releases: Array<() => void> = [];
    const realGetLatestWindow = entryCache.getLatestWindow.bind(entryCache);
    entryCache.getLatestWindow = ((historyKey: string, limit: number, boundary?: string) =>
      new Promise((resolve, reject) => {
        releases.push(() => {
          void realGetLatestWindow(historyKey, limit, boundary).then(resolve, reject);
        });
      })) as typeof entryCache.getLatestWindow;
    const snapshots: Array<{ present: boolean; token: number | undefined }> = [];
    const unsubscribe = threadsStore.subscribe((state) =>
      snapshots.push({ present: state.localOpenings.has(selected), token: state.localOpenings.get(selected) }),
    );
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) =>
        readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [turnFixture("entry-overlap", "OVERLAP_BODY")],
          requestGeneration: params.requestGeneration,
        }),
      );
      const first = threadsStore.getState().ensureThread(selected);
      const second = threadsStore.getState().ensureThread(selected);
      await vi.waitFor(() => expect(releases.length).toBeGreaterThanOrEqual(2));
      const newerToken = threadsStore.getState().localOpenings.get(selected);
      expect(newerToken).toBeGreaterThan(0);
      expect(snapshots.filter((entry) => entry.present).length).toBeGreaterThanOrEqual(2);

      // Settle the OLDER opening while the newer is still pending: its settle
      // must leave the newer hold published and in place.
      releases[0]!();
      await first;
      expect(isEntryTranscriptLocalOpening(selected)).toBe(true);
      expect(threadsStore.getState().localOpenings.get(selected)).toBe(newerToken);

      // Settle the newer opening: the hold clears and publishes.
      releases[1]!();
      await second;
      await fixture.settled();
      expect(isEntryTranscriptLocalOpening(selected)).toBe(false);
      // The matching clear was PUBLISHED to a real subscriber, not only in the
      // getter's final value.
      expect(snapshots.some((entry) => !entry.present)).toBe(true);
      expect(isEntryTranscriptLocalOpening(ordinary)).toBe(false);
    } finally {
      unsubscribe();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  // ensureThread always hydrates (accepts a model-bearing read) before it opens
  // the fixture's reader, so this path cannot present openLocal's model-less
  // branch (transcriptRepository.ts:394-395 needs `#model` undefined, and
  // :451-452 returns `latest`, which hydration has already set). A cold or
  // empty cache is not a model-less opening: openLocal still returns a
  // model-bearing result. Assert the genuine terminal outcome here - the
  // opening resolves, the ref keeps its published model, and the hold settles
  // with the clear observed by a real subscriber.
  it("a successful opening settles its hold and publishes the clear (ensureThread cannot yield a model-less opening)", async () => {
    const selected = "local:entry-opening-settles";
    const entryDatabase = "entry-opening-settles-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    const seen: boolean[] = [];
    const unsubscribe = threadsStore.subscribe((state) => seen.push(state.localOpenings.has(selected)));
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) =>
        readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [turnFixture("entry-settles", "SETTLES_BODY")],
          requestGeneration: params.requestGeneration,
        }),
      );
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      expect(seen).toContain(true);
      expect(seen).toContain(false); // the clear was published, not only the getter
      expect(isEntryTranscriptLocalOpening(selected)).toBe(false);
      // The terminal outcome is model-bearing, not model-less.
      expect(threadsStore.getState().threads.get(selected)).toBeDefined();
      expect(fixture.inspect().model).toBeDefined();
    } finally {
      unsubscribe();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  // A cache read that rejects is caught INSIDE openLocal
  // (transcriptRepository.ts:451-452), which resolves a model-bearing
  // recovery-needed result. The outer opening therefore RESOLVES; it does not
  // reject. Assert that real outcome and that the finally still settles the
  // hold, with the clear published to subscribers.
  it("a rejected cache read resolves a recovery-needed opening that settles and publishes the clear", async () => {
    const selected = "local:entry-reject-read";
    const entryDatabase = "entry-reject-read-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    const reads: Array<{ reject: (error: Error) => void }> = [];
    entryCache.getLatestWindow = (() =>
      new Promise((_resolve, reject) => {
        reads.push({ reject });
      })) as typeof entryCache.getLatestWindow;
    const seen: boolean[] = [];
    const unsubscribe = threadsStore.subscribe((state) => seen.push(state.localOpenings.has(selected)));
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) =>
        readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [turnFixture("entry-reject", "REJECT_BODY")],
          requestGeneration: params.requestGeneration,
        }),
      );
      const opening = threadsStore.getState().ensureThread(selected);
      await vi.waitFor(() => expect(reads.length).toBeGreaterThanOrEqual(1));
      expect(isEntryTranscriptLocalOpening(selected)).toBe(true);
      reads[0]!.reject(new Error("cache read failed"));
      // The real terminal outcome: openLocal catches and resolves; the opening
      // does not reject outward.
      await expect(opening).resolves.toBeUndefined();
      expect(isEntryTranscriptLocalOpening(selected)).toBe(false);
      expect(seen).toContain(true);
      expect(seen).toContain(false);
      expect(threadsStore.getState().threads.get(selected)).toBeDefined();
    } finally {
      unsubscribe();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  // Disposal and replacement: a disposed predecessor's late read completes
  // after a replacement fixture/reader holds ANOTHER ref; it must neither
  // clear nor re-arm the replacement's hold, and the replacement's own
  // settlement clears it. Disposal publishes the clear.
  //
  // The replacement is a distinct session ref on purpose: a replacement
  // fixture installed for an ALREADY-hydrated ref has no repository
  // (acceptEntryTranscriptHydration, which builds it, is skipped when the ref
  // is already in the store) and cannot open, so a same-ref replacement
  // opening is not producible by the real store. Per-ref keying is exactly
  // what keeps a late predecessor completion from touching the replacement.
  it("a disposed opening's late completion cannot clear or re-arm another ref's replacement hold", async () => {
    const predecessor = "local:entry-dispose-predecessor";
    const replacement = "local:entry-dispose-replacement";
    const databaseA = "entry-dispose-replace-a";
    const databaseB = "entry-dispose-replace-b";
    const cacheA = new SessionEntryCache({ databaseName: databaseA });
    const fixtureA = observeProductionEntryForTests(cacheA, predecessor);
    const readsA: Array<() => void> = [];
    const realA = cacheA.getLatestWindow.bind(cacheA);
    cacheA.getLatestWindow = ((historyKey: string, limit: number, boundary?: string) =>
      new Promise((resolve, reject) => {
        readsA.push(() => {
          void realA(historyKey, limit, boundary).then(resolve, reject);
        });
      })) as typeof cacheA.getLatestWindow;
    const seen: Array<{ ref: string; present: boolean }> = [];
    const unsubscribe = threadsStore.subscribe((state) => {
      seen.push({ ref: predecessor, present: state.localOpenings.has(predecessor) });
      seen.push({ ref: replacement, present: state.localOpenings.has(replacement) });
    });
    let cacheB: SessionEntryCache | undefined;
    let fixtureB: ReturnType<typeof observeProductionEntryForTests> | undefined;
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) =>
        readResponse(params.ref ?? predecessor, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [turnFixture("entry-dispose", "DISPOSE_BODY")],
          requestGeneration: params.requestGeneration,
        }),
      );
      const openingA = threadsStore.getState().ensureThread(predecessor);
      await vi.waitFor(() => expect(readsA.length).toBeGreaterThanOrEqual(1));
      expect(isEntryTranscriptLocalOpening(predecessor)).toBe(true);

      // Disposing the pending opening publishes the clear to subscribers.
      fixtureA.dispose();
      expect(isEntryTranscriptLocalOpening(predecessor)).toBe(false);
      expect(seen.some((entry) => entry.ref === predecessor && !entry.present)).toBe(true);

      // Replacement fixture/reader for a fresh ref hydrates and opens, taking
      // its own hold.
      cacheB = new SessionEntryCache({ databaseName: databaseB });
      const readsB: Array<() => void> = [];
      const realB = cacheB.getLatestWindow.bind(cacheB);
      cacheB.getLatestWindow = ((historyKey: string, limit: number, boundary?: string) =>
        new Promise((resolve, reject) => {
          readsB.push(() => {
            void realB(historyKey, limit, boundary).then(resolve, reject);
          });
        })) as typeof cacheB.getLatestWindow;
      const fixtureBCreated = observeProductionEntryForTests(cacheB, replacement);
      fixtureB = fixtureBCreated;
      const openingB = threadsStore.getState().ensureThread(replacement);
      await vi.waitFor(() => expect(readsB.length).toBeGreaterThanOrEqual(1));
      const replacementToken = threadsStore.getState().localOpenings.get(replacement);
      expect(replacementToken).toBeGreaterThan(0);

      // The disposed predecessor's real read completes late: no clear, no
      // re-arm of the replacement's hold.
      readsA[0]!();
      await openingA.catch(() => {});
      expect(isEntryTranscriptLocalOpening(predecessor)).toBe(false);
      expect(isEntryTranscriptLocalOpening(replacement)).toBe(true);
      expect(threadsStore.getState().localOpenings.get(replacement)).toBe(replacementToken);

      // The replacement's own settlement clears it.
      readsB[0]!();
      await openingB.catch(() => {});
      await fixtureB.settled();
      expect(isEntryTranscriptLocalOpening(replacement)).toBe(false);
      expect(seen.some((entry) => entry.ref === replacement && !entry.present)).toBe(true);
    } finally {
      unsubscribe();
      fixtureB?.dispose();
      fixtureA.dispose();
      cacheA.close();
      await deleteIndexedDatabase(databaseA);
      if (cacheB) {
        cacheB.close();
        await deleteIndexedDatabase(databaseB);
      }
    }
  });

  it("deleting its fixture ref removes the entry cache rows and coverage and retires only that route", async () => {
    const selected = "local:entry-delete-fixture";
    const entryDatabase = "entry-delete-fixture-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-delete", "DELETE_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-delete-item", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
      const generationBefore = await entryCache.getHistoryGeneration(historyKey);

      // A composer draft is independent of cache ownership and must survive.
      writeComposerDraft(selected, { text: "keep me", skillNames: [] });
      // And so is a queued mutation in the outbox.
      const outbox = new MutationOutboxIndexedDB({ createMutationId: () => "task4-delete-outbox" });
      const outboxRecord = await outbox.enqueueIntent({
        targetRef: selected,
        method: "turn/queue",
        payload: { ref: selected, expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
        attachments: [],
        optimisticDisplay: { text: "queued" },
      });

      markCacheSessionsDeleted([selected]);
      await vi.waitFor(async () => expect(await entryCache.getLatest(historyKey, 10)).toEqual([]));
      await expect(entryCache.getCoverage(historyKey, "read:fixture-read-1")).resolves.toBeUndefined();
      // The durable deletion fence advanced, so a late pre-delete capture is
      // refused rather than resurrecting the rows.
      expect(await entryCache.getHistoryGeneration(historyKey)).toBe(generationBefore + 1);
      expect(readComposerDraft(selected).text).toBe("keep me");
      await expect(outbox.getOutbox(outboxRecord.clientMutationId)).resolves.toEqual(outboxRecord);
      outbox.close();
      // The store's own model is gone and the route retires without touching an
      // unrelated ref.
      expect(threadsStore.getState().deletedRefs.has(selected)).toBe(true);
    } finally {
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("a delete while the entry route's authority read is held never adopts the post-delete authorities", async () => {
    const selected = "local:entry-delete-held";
    const entryDatabase = "entry-delete-held-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-delete-held", "DELETE_HELD_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-delete-held-item", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      // Open the cache connection before parking the reads, so the later
      // deleteHistory is not the first open.
      const realEpoch = entryCache.getClearEpoch.bind(entryCache);
      const realGeneration = entryCache.getHistoryGeneration.bind(entryCache);
      await realEpoch();
      // Park the durable authority reads before their real work.
      const releases: Array<() => void> = [];
      entryCache.getClearEpoch = (() =>
        new Promise<number>((resolve, reject) => {
          releases.push(() => void realEpoch().then(resolve, reject));
        })) as typeof entryCache.getClearEpoch;
      entryCache.getHistoryGeneration = ((historyKeyValue: string) =>
        new Promise<number>((resolve, reject) => {
          releases.push(() => void realGeneration(historyKeyValue).then(resolve, reject));
        })) as typeof entryCache.getHistoryGeneration;

      const opening = threadsStore.getState().ensureThread(selected);
      await vi.waitFor(() => expect(releases.length).toBeGreaterThanOrEqual(2));
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");

      // Delete while the authority read is in flight, then let it read the new
      // generation. The old response must not be relabelled fresh. A queued
      // outbox mutation must survive the delete untouched.
      const outbox = new MutationOutboxIndexedDB({ createMutationId: () => "task4-held-outbox" });
      const outboxRecord = await outbox.enqueueIntent({
        targetRef: selected,
        method: "turn/queue",
        payload: { ref: selected, expectedTurnId: "", input: [{ type: "text", text: "queued" }] },
        attachments: [],
        optimisticDisplay: { text: "queued" },
      });
      markCacheSessionsDeleted([selected]);
      await vi.waitFor(async () => expect(await entryCache.getLatest(historyKey, 10)).toEqual([]));
      expect(await realGeneration(historyKey)).toBe(1);
      for (const release of releases.splice(0)) release();
      await opening;
      await settleBackgroundTranscriptsForTests();

      await expect(entryCache.getLatest(historyKey, 10)).resolves.toEqual([]);
      await expect(entryCache.getCoverage(historyKey, "read:fixture-read-1")).resolves.toBeUndefined();
      // No replacement repository was installed with the old response.
      expect(fixture.inspect().model).toBeUndefined();
      await expect(outbox.getOutbox(outboxRecord.clientMutationId)).resolves.toEqual(outboxRecord);
      outbox.close();
    } finally {
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("a same-tab Clear removes entry rows, preserves the held display, and a fresh explicit open re-caches", async () => {
    const selected = "local:entry-same-tab-clear";
    const entryDatabase = "entry-same-tab-clear-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-same-tab-clear", "SAME_TAB_CLEAR_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-same-tab-clear-item", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
      const displayBeforeClear = fixture.inspect().model;
      expect(displayBeforeClear).toBeDefined();

      await expect(clearCachedSessions()).resolves.toEqual({ committed: true });
      await vi.waitFor(async () => expect(await entryCache.getLatest(historyKey, 10)).toEqual([]));
      // Clear retires cache authority, not the still-held live display.
      expect(await entryCache.getClearEpoch()).toBe(1);
      expect(fixture.inspect().model).toBeDefined();
      expect(fixture.inspect().model?.turns).toEqual(displayBeforeClear?.turns);
      expect(threadsStore.getState().cacheLifetimes.get(selected)?.suppressed).toBe(true);

      // A fresh explicit foreground acquisition re-caches under the new epoch.
      await threadsStore.getState().releaseThreadAndWait(selected);
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);

      // The replacement reader/subscription owns publication: an accepted
      // recorded notification reaches the store model.
      const threadId = threadsStore.getState().threads.get(selected)?.threadId;
      if (!threadId) throw new Error("expected a published model after re-acquisition");
      requireConnectedClient("entry fixture notification").emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "entry-clear-live", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "clear-live-item",
              transcriptKey: "clear-live-item",
              turnId: "entry-clear-live",
              version: 2,
              text: "CLEAR_LIVE_BODY",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await fixture.settled();
      await settleProjectionWorkForTests();
      expect(JSON.stringify(threadsStore.getState().threads.get(selected) ?? null)).toContain("CLEAR_LIVE_BODY");
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("a failed entry authority read does not reject an otherwise successful hydration", async () => {
    const selected = "local:entry-authority-reject";
    const entryDatabase = "entry-authority-reject-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-authority-reject", "AUTHORITY_REJECT_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [
            { ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-authority-reject-item", version: 1 }] },
          ],
          requestGeneration: params.requestGeneration,
        });
      });
      const realEpoch = entryCache.getClearEpoch.bind(entryCache);
      entryCache.getClearEpoch = (() =>
        Promise.reject(new Error("cache read failed"))) as typeof entryCache.getClearEpoch;
      await expect(threadsStore.getState().ensureThread(selected)).resolves.toBeUndefined();
      await fixture.settled();
      // The source hydration still published, and the durable read still works.
      expect(threadsStore.getState().threads.get(selected)).toBeDefined();
      await expect(realEpoch()).resolves.toBe(0);
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("a held entry authority read is bounded by the lookup deadline and does not stall the opening", async () => {
    const selected = "local:entry-authority-held";
    const entryDatabase = "entry-authority-held-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-authority-held", "AUTHORITY_HELD_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-authority-held-item", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      const realEpoch = entryCache.getClearEpoch.bind(entryCache);
      const realGeneration = entryCache.getHistoryGeneration.bind(entryCache);
      entryCache.getClearEpoch = (() => new Promise<number>(() => {})) as typeof entryCache.getClearEpoch;
      entryCache.getHistoryGeneration = (() => new Promise<number>(() => {})) as typeof entryCache.getHistoryGeneration;
      const started = Date.now();
      await expect(threadsStore.getState().ensureThread(selected)).resolves.toBeUndefined();
      expect(Date.now() - started).toBeLessThan(2_000);
      entryCache.getClearEpoch = realEpoch;
      entryCache.getHistoryGeneration = realGeneration;
      expect(threadsStore.getState().threads.get(selected)).toBeDefined();
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("a same-tab Clear retires cache claims immediately, before its transaction resolves", async () => {
    const selected = "local:entry-clear-immediate";
    const entryDatabase = "entry-clear-immediate-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-clear-immediate", "CLEAR_IMMEDIATE_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-clear-immediate-item", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
      const publishedBefore = JSON.stringify(threadsStore.getState().threads.get(selected) ?? null);

      // Hold the entry clear so the entry route's claims must retire BEFORE the
      // clear transactions resolve.
      let releaseClear!: () => void;
      const clearGate = new Promise<void>((resolve) => {
        releaseClear = resolve;
      });
      const realClear = entryCache.clear.bind(entryCache);
      const clearSpy = vi.spyOn(entryCache, "clear").mockImplementation(async () => {
        await clearGate;
        return realClear();
      });
      const clearing = clearCachedSessions();
      await vi.waitFor(() => expect(threadsStore.getState().clearInFlight).toBeDefined());
      // Immediate claim retirement without disconnection: the route is
      // suppressed but not disposed, so a still-held model can resume on abort.
      expect(fixture.inspect().model).toBeDefined();

      // A notification delivered in the window neither publishes nor persists.
      requireConnectedClient("entry fixture notification").emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId: `thr_${selected}`,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "entry-clear-window", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "clear-window-item",
              transcriptKey: "clear-window-item",
              turnId: "entry-clear-window",
              version: 2,
              text: "CLEAR_WINDOW_BODY",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await settleProjectionWorkForTests();
      expect(JSON.stringify(threadsStore.getState().threads.get(selected) ?? null)).toBe(publishedBefore);
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);

      releaseClear();
      await expect(clearing).resolves.toEqual({ committed: true });
      clearSpy.mockRestore();

      // Recovery: a fresh explicit acquisition re-caches.
      await threadsStore.getState().releaseThreadAndWait(selected);
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it.each([false, true])("a held same-tab Clear pauses queued entry writes, entry abort=%s", async (abort) => {
    const selected = `local:entry-clear-queued-${abort}`;
    const entryDatabase = `entry-clear-queued-${abort}-test`;
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      const turn = turnFixture("queued-baseline", "QUEUED_BASELINE");
      return readResponse(params.ref ?? selected, {
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "queued-baseline", version: 1 }] }],
        requestGeneration: params.requestGeneration,
      });
    });
    const lease = acquireBackgroundThread(selected);
    let releaseWrite!: () => void;
    let releaseClear!: () => void;
    const writeGate = new Promise<void>((resolve) => {
      releaseWrite = resolve;
    });
    const clearGate = new Promise<void>((resolve) => {
      releaseClear = resolve;
    });
    let reachedWrite!: () => void;
    const writeReached = new Promise<void>((resolve) => {
      reachedWrite = resolve;
    });
    const realCommit = entryCache.commitBatch.bind(entryCache);
    const dispatched: string[] = [];
    const commitSpy = vi.spyOn(entryCache, "commitBatch").mockImplementation(async (batch) => {
      dispatched.push(...batch.items.map((row) => row.transcriptKey));
      if (batch.items.some((row) => row.transcriptKey === "queued-9")) {
        reachedWrite();
        await writeGate;
      }
      return realCommit(batch);
    });
    const realClear = entryCache.clear.bind(entryCache);
    const clearSpy = vi.spyOn(entryCache, "clear").mockImplementation(async () => {
      await clearGate;
      if (!abort) return realClear();
      const original = IDBObjectStore.prototype.clear;
      const fault = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementation(function (this: IDBObjectStore) {
        if (this.transaction.objectStoreNames.contains("transcriptItems")) throw new Error("entry queued clear fault");
        return original.call(this);
      });
      try {
        return await realClear();
      } finally {
        fault.mockRestore();
      }
    });
    let clearing: ReturnType<typeof clearCachedSessions> | undefined;
    try {
      await lease.ready;
      await fixture.settled();
      expect(fixture.inspect().readerRetention).toBeUndefined();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected resolved history key");
      const emit = (entry: number) =>
        fake.emitNotification({
          method: "history/updated",
          params: {
            ref: selected,
            threadId: `thr_${selected}`,
            bootGeneration: "1",
            epoch: 1,
            snapshot: { incarnation: "inc-1", length: entry },
            turns: [{ id: "queued-turn", status: "completed", itemsView: "full", version: entry }],
            items: [
              {
                type: "agentMessage",
                id: `queued-${entry}`,
                transcriptKey: `queued-${entry}`,
                turnId: "queued-turn",
                version: entry,
                text: `QUEUED_${entry}`,
                position: { entry, item: 0 },
              },
            ],
          },
        } as AnyNotification);
      emit(9);
      await writeReached;
      emit(10);
      await settleProjectionWorkForTests();
      clearing = clearCachedSessions();
      expect(threadsStore.getState().clearInFlight).toBeDefined();
      releaseWrite();
      // The entry Clear is still held and the entry durable epoch is still 0.
      // Wait for the issued predecessor to commit, then its queue continuation.
      await vi.waitFor(async () =>
        expect((await entryCache.getLatest(historyKey, 20)).some((row) => row.transcriptKey === "queued-9")).toBe(true),
      );
      await expect(fixture.settled()).rejects.toThrow(/outstanding/);
      expect(await entryCache.getClearEpoch()).toBe(0);
      expect(dispatched).not.toContain("queued-10");
      expect((await entryCache.getLatest(historyKey, 20)).map((row) => row.transcriptKey)).not.toContain("queued-10");
      releaseClear();
      await expect(clearing).resolves.toEqual({ committed: !abort });
      if (abort) {
        // No new notification or acquisition: the same queued item must resume
        // automatically when the real entry Clear transaction aborts.
        // Observe disk before settled(), which can itself drive ready accepts.
        await vi.waitFor(async () =>
          expect((await entryCache.getLatest(historyKey, 20)).map((row) => row.transcriptKey)).toContain("queued-10"),
        );
        await fixture.settled();
        expect(dispatched.filter((key) => key === "queued-10")).toHaveLength(1);
        expect((await entryCache.getLatest(historyKey, 20)).map((row) => row.transcriptKey)).toContain("queued-10");
        expect(await entryCache.getClearEpoch()).toBe(0);
      } else {
        expect(fixture.inspect().model).toBeDefined();
        expect(fixture.inspect().readerRetention).toBeUndefined();
        expect(threadsStore.getState().cacheLifetimes.get(selected)?.suppressed).toBe(true);
        expect(dispatched).not.toContain("queued-10");
        expect(await entryCache.getLatest(historyKey, 20)).toEqual([]);
      }
    } finally {
      releaseWrite();
      releaseClear();
      await clearing;
      commitSpy.mockRestore();
      clearSpy.mockRestore();
      await lease.release();
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("a same-tab Clear whose entry transaction aborts is reported incomplete and keeps the entry rows", async () => {
    const selected = "local:entry-clear-abort";
    const entryDatabase = "entry-clear-abort-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-clear-abort", "CLEAR_ABORT_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-clear-abort-item", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);

      // Abort only the entry cache's clear transaction: the legacy clear still
      // commits.
      const originalClear = IDBObjectStore.prototype.clear;
      const clearSpy = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementation(function (
        this: IDBObjectStore,
        ...args: Parameters<IDBObjectStore["clear"]>
      ) {
        if (this.transaction.objectStoreNames.contains("transcriptItems")) throw new Error("entry clear fault");
        return originalClear.apply(this, args);
      });
      const result = await clearCachedSessions();
      clearSpy.mockRestore();

      // The outer result is honest about the incomplete clear, and the entry
      // rows it failed to remove remain.
      expect(result).toEqual({ committed: false });
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
      expect(await entryCache.getClearEpoch()).toBe(0);
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("a rejected old completion does not dispossess a replacement owner", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const selected = "local:entry-rejected-no-dispossess";
    const entryDatabase = "entry-rejected-no-dispossess-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-rejected", "REJECTED_BODY");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "entry-rejected-item", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      // Park acquisition A's authority getters.
      const releases: Array<() => void> = [];
      const realEpoch = entryCache.getClearEpoch.bind(entryCache);
      const realGeneration = entryCache.getHistoryGeneration.bind(entryCache);
      entryCache.getClearEpoch = (() =>
        new Promise<number>((resolve, reject) => {
          releases.push(() => void realEpoch().then(resolve, reject));
        })) as typeof entryCache.getClearEpoch;
      entryCache.getHistoryGeneration = ((historyKeyValue: string) =>
        new Promise<number>((resolve, reject) => {
          releases.push(() => void realGeneration(historyKeyValue).then(resolve, reject));
        })) as typeof entryCache.getHistoryGeneration;

      const openingA = threadsStore.getState().ensureThread(selected);
      await vi.waitFor(() => expect(releases.length).toBeGreaterThanOrEqual(2));
      // Restore the real getters so the Clear and reacquisition read normally.
      entryCache.getClearEpoch = realEpoch;
      entryCache.getHistoryGeneration = realGeneration;

      // Complete the same-tab Clear, then release and reacquire so acquisition B
      // installs its own repository.
      await expect(clearCachedSessions()).resolves.toEqual({ committed: true });
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      expect(fixture.inspect().model).toBeDefined();

      // Release A's parked getters: A is rejected and must not dispose B's route.
      for (const release of releases.splice(0)) release();
      await openingA;
      await settleBackgroundTranscriptsForTests();
      expect(fixture.inspect().model).toBeDefined();

      // B still publishes an accepted recorded notification.
      const threadId = threadsStore.getState().threads.get(selected)?.threadId;
      if (!threadId) throw new Error("expected B's published model");
      requireConnectedClient("entry fixture notification").emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "entry-B-survives", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "b-survives-item",
              transcriptKey: "b-survives-item",
              turnId: "entry-B-survives",
              version: 2,
              text: "B_SURVIVES_BODY",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await fixture.settled();
      await settleProjectionWorkForTests();
      expect(JSON.stringify(threadsStore.getState().threads.get(selected) ?? null)).toContain("B_SURVIVES_BODY");
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      vi.useRealTimers();
      await deleteIndexedDatabase(entryDatabase);
    }
  });

  it("an aborted same-tab Clear restores the held entry route so ingestion resumes", async () => {
    const selected = "local:entry-clear-abort-resume";
    const entryDatabase = "entry-clear-abort-resume-test";
    const entryCache = new SessionEntryCache({ databaseName: entryDatabase });
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      const fake = connectFakeClient();
      fake.on("thread/read", (params) => {
        const turn = turnFixture("entry-clear-abort-resume", "ABORT_RESUME_SEED");
        return readResponse(params.ref ?? selected, {
          snapshot: { incarnation: "inc-1", length: 1 },
          turns: [{ ...turn, items: [{ ...turn.items[0]!, transcriptKey: "abort-resume-seed", version: 1 }] }],
          requestGeneration: params.requestGeneration,
        });
      });
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
      const epochBefore = await entryCache.getClearEpoch();

      // Abort the real entry Clear before any commit; nothing changed.
      const originalClear = IDBObjectStore.prototype.clear;
      const clearFault = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementation(function (
        this: IDBObjectStore,
      ) {
        if (this.transaction.objectStoreNames.contains("transcriptItems")) throw new Error("entry clear fault");
        return originalClear.call(this);
      });
      await expect(clearCachedSessions()).resolves.toEqual({ committed: false });
      clearFault.mockRestore();
      expect(threadsStore.getState().clearInFlight).toBeUndefined();

      // The immediately-retired route was restored: a valid same-binding
      // notification advances the held display AND persists, without a release
      // or reacquire.
      const threadId = threadsStore.getState().threads.get(selected)?.threadId;
      if (!threadId) throw new Error("expected the restored held model");
      requireConnectedClient("entry fixture notification").emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "holder-live", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "abort-resume-item",
              transcriptKey: "abort-resume-item",
              turnId: "holder-live",
              version: 2,
              text: "ABORT_RESUME_BODY",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await fixture.settled();
      await settleProjectionWorkForTests();
      expect(JSON.stringify(threadsStore.getState().threads.get(selected) ?? null)).toContain("ABORT_RESUME_BODY");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toEqual(
        expect.arrayContaining([expect.objectContaining({ transcriptKey: "abort-resume-item" })]),
      );
      // Independent suppression is untouched: the durable epoch is unchanged.
      expect(await entryCache.getClearEpoch()).toBe(epochBefore);
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      await deleteIndexedDatabase(entryDatabase);
    }
  });
});
