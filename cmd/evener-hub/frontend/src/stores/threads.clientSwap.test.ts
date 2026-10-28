import { type TestThreadOverrides, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import type { InitializeResponse, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { connectionStore } from "./connection";
import { SessionEntryCache } from "./sessionEntryCache";
import { resetThreadsStoreForTests, setProductionEntryCacheForTests, threadsStore } from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `client-swap-${crypto.randomUUID()}`,
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

async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

describe("client swap (manual retry) rewiring", () => {
  // The trap this covers (see docs/superpowers/plans/
  // 2026-07-20-webui-rewrite-wave3-shell.md, Task 5): before this
  // describe block existed, wiredClient was only ever updated lazily,
  // inside requireClient(), the first time some ACTION (ensureThread/send/
  // steer/queue/interrupt) ran. Swapping connectionStore's client
  // reference alone (shell/ConnectionBanner.tsx's retry, which mints a
  // FRESH AppwireClient rather than reusing the dead one) left
  // onNotification/onReady still attached to the dead client - the banner
  // would report "ready" while every open pane silently stopped receiving
  // deltas, since nothing forces a store action to run just because the
  // connection recovered. This test drives the swap through NOTHING but
  // connectionStore.getState().connect(b) - no ensureThread/send/etc call
  // follows it - so it only passes if the rewiring is reactive to the
  // client reference itself, not piggybacked on some later action.
  test("swapping to a fresh client re-hydrates tracked refs, routes its notifications, and detaches the dead client's handlers (no double delivery)", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);

    // Kill A for good (a terminal drop, not a same-client reconnect - see
    // the "reconnect resubscribe" tests above for that separate case).
    a.emitStateChange("closed");

    // The manual retry: a FRESH client, already "ready" by the time it's
    // handed to connectionStore.connect() - mirrors the real sequence
    // ConnectionBanner's retry follows (construct, await connect(), THEN
    // wire the store), not an in-flight handshake. Its own thread/read
    // response deliberately differs from A's (empty turns, not one) so a
    // passing assertion below can only mean the re-hydrate actually ran
    // against B, not a stale snapshot left over from A.
    const b = new FakeClient("ready");
    b.on("thread/read", (params) => readResponse("ref_a", { turns: [] }, params.requestGeneration));
    connectionStore.getState().connect(b);

    // Re-hydration is async (a thread/read round trip against B) with
    // nothing else in this test driving it forward.
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns.length === 0);
    await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("ref_a")).toBe(2));
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(0);

    // B's own live notification reaches the tracked model...
    b.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    // ...while A's handlers were detached at the swap: the same
    // notification shape, injected via the now-dead client, must NOT be
    // delivered - proof of no lingering double-subscription, not just
    // that B independently works.
    a.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" }); // unchanged - A's emit was a no-op
  });

  test("swapping to a fresh client that is not yet ready waits for its own onReady, same as the initial connection", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    a.emitStateChange("closed");

    const b = new FakeClient("connecting"); // still mid-handshake when wired in
    b.on("thread/read", () => readResponse("ref_a"));
    connectionStore.getState().connect(b);

    // b isn't ready yet, so no eager hydrate should have fired against it -
    // asserted directly on b's own call log (not on `threads` content,
    // which can't distinguish "not re-hydrated yet" from "re-hydrated to
    // an identical snapshot").
    await Promise.resolve(); // let any (wrongly) eager work settle before asserting it didn't happen
    expect(b.calls.filter((c) => c.method === "thread/read")).toHaveLength(0);

    b.emitReady(); // completes B's own handshake
    await flushUntil(() => b.calls.filter((c) => c.method === "thread/read").length > 0);

    expect(b.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });

  // issue #1749: connectionStore losing its client is not a swap to another
  // client, and rewireClient was only ever reached with a non-null client
  // (the subscribe below guarded with `if (state.client)`), so the handlers
  // stayed attached and wiredClient kept pointing at the detached client. A
  // client that later reaches "ready" then still passed
  // readyGenerationCallback's `client === currentClient()` guard and ran a
  // ready generation for a connection the store no longer held.
  test("dropping connectionStore's client detaches the store: a later ready and a later notification from the dropped client reach nothing", async () => {
    // A client whose onReady unsubscribe does not detach, standing in for a
    // real client that already snapshotted this handler into an in-flight
    // ready dispatch (AppwireClient.setState iterates Array.from(handlers))
    // before the detach's unsubscribe ran. The identity guard, not the
    // unsubscribe alone, has to fence the stale firing.
    class UndetachableReadyClient extends FakeClient {
      override onReady(cb: (initialize: InitializeResponse) => void): () => void {
        super.onReady(cb);
        return () => {};
      }
    }

    const a = new UndetachableReadyClient("ready");
    a.on("thread/read", () => readResponse("ref_a", { turns: [] }));
    connectionStore.getState().connect(a);
    await threadsStore.getState().ensureThread("ref_a");
    const readsAfterHydrate = a.calls.filter((c) => c.method === "thread/read").length;
    expect(readsAfterHydrate).toBe(1);

    // The connection is dropped: connectionStore no longer holds A.
    connectionStore.setState({ client: null });

    // A reaches ready after the drop. No ready generation may begin for a
    // connection the store no longer holds — handleReady's re-read fan-out is
    // the observable, and it must not run.
    a.emitStateChange("reconnecting");
    a.emitReady();
    await flushUntil(() => false, 30);
    expect(a.calls.filter((c) => c.method === "thread/read")).toHaveLength(readsAfterHydrate);

    // A's handlers must be gone with it too: a frame it still emits reaches
    // nothing (the model keeps the idle status its snapshot hydrated).
    a.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "idle" });
  });
});
