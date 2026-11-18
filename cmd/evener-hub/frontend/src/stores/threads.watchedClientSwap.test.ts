import "fake-indexeddb/auto";
import type { EvenerDelegateInfo, ThreadReadResponse } from "@evener/appwire-client";
import { ErrorTranscriptHistoryFailed, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { readResponse } from "./threadHydrationTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "./threads";

// A hold's buffered copy belongs to the CONNECTION that filled it, not to the
// ref. When a replacement client is installed while a watched refresh is still
// pending, the frames in the old client's buffer were addressed to a hub
// session the new read no longer answers to: their delegate samples name
// daemons THAT session was serving. Carrying them across means the new
// client's first read - if it answers transcriptHistoryFailed - replays them
// onto the model its own hand-off just cleared, restoring a retired daemon's
// hold over the replacement's own. An unchanged hold emits no update of its
// own, so that restored wait would sit there for its whole duration.
//
// The fix is not a token filter: a buffered delegate frame carries no
// publisher identity (EvenerDelegateParams is threadId, ref, delegate), which
// is #6815's subject. It is the buffer's own provenance - same client, same
// ready epoch - that decides whether its contents are still evidence.

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
    databaseName: `watched-client-swap-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});

afterEach(() => {
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();
  cache.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// flushUntil drains microtask turns until `done()` reports true (or a bounded
// number of turns elapse, so a genuine hang fails fast instead of silently).
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

// The same live-history identity the delegate frames below are stamped with,
// so a read hydrated from this response lands on the versioned path instead of
// being invalidated by a boot-generation mismatch on the first live frame.
function versionedReadResponse(ref: string): ThreadReadResponse {
  return {
    ...readResponse(ref),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
  };
}

// A stable-delegate row waiting on a shell. `holdSequence` orders within one
// source epoch, so 900 outranks any sequence a daemon numbering from its own
// start would reach first - which is why a retired sample has to be dropped.
function heldDelegateRow(): EvenerDelegateInfo {
  return {
    runGeneration: 1,
    delegateId: "dlg_held",
    ownerSessionId: "sess_owner",
    rootSessionId: "sess_root",
    childSessionId: "sess_child",
    transcriptRef: "local:01dlg_held",
    type: "delegate",
    lifecycle: "stable",
    phase: "working",
    status: "running",
    outcome: "pending",
    terminal: false,
    resumable: true,
    needsAttention: false,
    projectionRevision: 3,
    drainHeldByJobs: true,
    holdSequence: 900,
    runningJobCount: 2,
    oldestRunningJobStartedAt: "2026-01-03T00:00:00Z",
  };
}

describe("a watched read's buffered hold across a client swap", () => {
  test("a replacement client does not replay the outgoing client's buffered hold", async () => {
    const a = connectReadyFakeClient();
    a.on("thread/read", (params) => {
      const response = versionedReadResponse("ref_a");
      response.requestGeneration = params.requestGeneration;
      return response;
    });
    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);

    // A refresh on the outgoing client, held open: this is the read the
    // replacement client supersedes.
    const held: { resolve: ((response: ThreadReadResponse) => void) | null } = { resolve: null };
    a.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (held.resolve = resolve)));
    a.emitStateChange("reconnecting");
    a.emitReady();
    await flushUntil(() => held.resolve !== null);
    expect(held.resolve, "the reset must leave a watched refresh pending").not.toBeNull();

    // The daemon THAT client serves publishes a live sample. It lands in the
    // outgoing client's buffer and nowhere else.
    a.emitNotification({
      method: "evener/delegate/updated",
      params: { threadId: "thr_ref_a", ref: "ref_a", delegate: heldDelegateRow() },
    });
    await flushUntil(() => false, 5);
    expect(
      threadsStore.getState().watchedThreads.get("ref_a")?.delegates?.[0]?.holdSequence,
      "a pending read must buffer the sample rather than publish it",
    ).toBeUndefined();

    // A replacement client arrives. Its first read answers for the new session
    // and fails structurally, naming a daemon this client never spoke to.
    const b = new FakeClient("ready");
    b.on("thread/read", () => {
      throw new WireError("thread history failed at entry 3", -32000, {
        evenerErrorInfo: ErrorTranscriptHistoryFailed,
        bootGeneration: "9",
      });
    });
    connectionStore.getState().connect(b);
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.history?.failed !== undefined, 60);
    expect(
      threadsStore.getState().watchedThreads.get("ref_a")?.history?.failed,
      "the failure must publish, or this test proves nothing about what it replays",
    ).toBeDefined();

    // The outgoing client's sample must not be here. The new session's failure
    // is its own account of what it serves, and that daemon was never asked.
    expect(
      threadsStore.getState().watchedThreads.get("ref_a")?.delegates?.[0]?.holdSequence,
      "the replaced client's buffered hold must not survive into the new client's read",
    ).toBeUndefined();

    threadsStore.getState().releaseWatchedThread("ref_a");
  });
});
