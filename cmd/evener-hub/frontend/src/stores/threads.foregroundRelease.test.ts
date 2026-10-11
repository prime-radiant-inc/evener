import "fake-indexeddb/auto";
import {
  hydrateThread,
  type QueueState,
  type Thread,
  type ThreadCapabilities,
  type ThreadReadResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  productionEntryTranscriptForTests,
  putThreadModel,
  resetThreadsStoreForTests,
  setProductionEntryCacheForTests,
  threadsStore,
} from "./threads";

let cache: SessionEntryCache;
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `foreground-release-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

type TestThreadOverrides = Omit<Partial<Thread>, "evener"> & {
  evener?: Omit<Thread["evener"], "queue"> & { queue: Partial<QueueState> };
};

function testThread(ref: string, overrides: TestThreadOverrides = {}): Thread {
  const { evener, ...threadOverrides } = overrides;
  const threadID = threadOverrides.id ?? `thr_${ref}`;
  return {
    id: threadID,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref,
      instanceId: threadID,
      mutationStateAuthoritative: true,
      capabilities: CAPABILITIES,
      ...evener,
      queue: { revision: 0, ...evener?.queue },
    },
    ...threadOverrides,
  };
}

function readResponse(ref: string, requestGeneration?: number): ThreadReadResponse {
  return {
    thread: testThread(ref),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration: requestGeneration ?? 1,
  };
}

describe("useThreadsStore.releaseThread", () => {
  test("refcounts panes; stops tracking only when the last pane releases", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse("ref_a", params.requestGeneration));
    await threadsStore.getState().ensureThread("ref_a"); // pane 1
    await threadsStore.getState().ensureThread("ref_a"); // pane 2 (refcount 2, no re-read)

    threadsStore.getState().releaseThread("ref_a"); // pane 1 leaves
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true); // pane 2 still open

    threadsStore.getState().releaseThread("ref_a"); // pane 2 leaves
    // R36: the finite final owner retains this same model until its terminal read.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("session and panel lifecycle claims survive close and remount reordering until the final reference releases", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse("ref_a", params.requestGeneration));

    await threadsStore.getState().ensureThread("ref_a"); // session pane mounts
    await threadsStore.getState().ensureThread("ref_a"); // panel pane mounts
    threadsStore.getState().releaseThread("ref_a"); // session pane closes first
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);

    // A dockview reorder can remount the session before it unmounts the panel.
    // The hand-off must remain refcounted instead of transiently evicting the
    // hydrated model or issuing another read.
    await threadsStore.getState().ensureThread("ref_a");
    threadsStore.getState().releaseThread("ref_a"); // panel pane unmounts
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);

    threadsStore.getState().releaseThread("ref_a"); // remounted session is the last reference
    // R36: the finite final owner retains this same model until its terminal read.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
  });

  test("releasing an untracked ref is a harmless no-op", () => {
    expect(() => threadsStore.getState().releaseThread("never_tracked")).not.toThrow();
  });

  test("the final pane release unsubscribes the ref on the wire; earlier releases do not", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse("ref_a", params.requestGeneration));
    await threadsStore.getState().ensureThread("ref_a"); // pane 1
    await threadsStore.getState().ensureThread("ref_a"); // pane 2

    threadsStore.getState().releaseThread("ref_a"); // pane 1 leaves
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);

    threadsStore.getState().releaseThread("ref_a"); // last pane leaves
    let unsubscribes = fake.calls.filter((call) => call.method === "thread/unsubscribe");
    // R36: shared membership remains held until the finite final owner settles.
    expect(unsubscribes).toHaveLength(0);
    expect(unsubscribes[0]?.params).toBeUndefined();
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));
    unsubscribes = fake.calls.filter((call) => call.method === "thread/unsubscribe");
    expect(unsubscribes).toHaveLength(1);
    expect(unsubscribes[0]?.params).toMatchObject({ ref: "ref_a" });
  });

  test("a released-then-re-ensured ref re-subscribes with subscribe:true again", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse("ref_a", params.requestGeneration));
    await threadsStore.getState().ensureThread("ref_a");
    const repository = productionEntryTranscriptForTests("ref_a").repository;
    expect(repository).toBeDefined();
    if (!repository) throw new Error("ordinary acquisition published no repository");
    const retire = vi.spyOn(repository, "retire");
    const cancelRetirement = vi.spyOn(repository, "cancelRetirement");
    threadsStore.getState().releaseThread("ref_a");
    expect(retire).toHaveBeenCalledTimes(1);

    await threadsStore.getState().ensureThread("ref_a");
    const reads = fake.calls.filter((call) => call.method === "thread/read");
    expect(reads).toHaveLength(2);
    expect(reads[0]?.params).toMatchObject({ subscribe: true });
    // R36: the same-connection re-claim cancels retirement, membership never ended.
    expect(reads[1]?.params).toMatchObject({ subscribe: false });
    expect(cancelRetirement).toHaveBeenCalledTimes(1);
    expect(await retire.mock.results[0]?.value).toEqual({ kind: "cancelled" });
    expect(productionEntryTranscriptForTests("ref_a").repository).toBe(repository);
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    // The second final release starts its own finite read, not a second subscription.
    threadsStore.getState().releaseThread("ref_a");
    // R36: no unsubscribe occurs at this same synchronous final-release point.
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    expect(retire).toHaveBeenCalledTimes(2);
    expect(await retire.mock.results[1]?.value).toEqual({ kind: "saved" });
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
  });
});

describe("putThreadModel ref invariant (map key === model.ref)", () => {
  // Round-2 fix, second finding: the replace path already threw when a
  // model's ref disagreed with the slot it was filed under; the pure-add
  // path now throws for the same disagreement. Both guards exist for the
  // routing index (see the routing-index describe block above): a model
  // filed under a key its own ref contradicts would mis-route every
  // ref-routed frame for that key, so it fails loudly at the membership
  // boundary instead.
  test("adding a model whose ref disagrees with the map key throws", () => {
    const model = hydrateThread(readResponse("ref_other"), "ref_other", Date.now());
    expect(() => putThreadModel("ref_a", model)).toThrow(/map key and model\.ref must agree/);
    // Nothing was filed: the store keeps whatever it had at that key.
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("replacing a model whose ref disagrees with the map key still throws (pre-existing guard)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    const moved = hydrateThread(readResponse("ref_moved"), "ref_moved", Date.now());
    expect(() => putThreadModel("ref_a", moved)).toThrow(/map key and model\.ref must agree/);
    // The tracked model is untouched by the rejected put.
    expect(threadsStore.getState().threads.get("ref_a")?.ref).toBe("ref_a");
  });

  test("an agreeing add still lands (guard does not reject the legitimate path)", () => {
    const model = hydrateThread(readResponse("ref_a"), "ref_a", Date.now());
    putThreadModel("ref_a", model);
    expect(threadsStore.getState().threads.get("ref_a")?.ref).toBe("ref_a");
  });
});
