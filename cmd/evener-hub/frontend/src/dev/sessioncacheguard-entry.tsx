// Two real documents load this entry: only the AppWire boundary is scripted.
// The store's module-scope BroadcastChannel and IndexedDB adapter are untouched.
import type { CachedSessionRecord, ThreadReadResponse, Turn, TurnModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { deleteSession } from "../shell/rail/actions";
import { connectionStore } from "../stores/connection";
import { settleProjectionWorkForTests } from "../stores/projectionWork";
import { SessionCacheIndexedDB } from "../stores/sessionCacheIndexedDB";
import { clearCachedSessions, resetThreadsStoreForTests, threadsStore } from "../stores/threads";

const instance = new URLSearchParams(location.search).get("instance");
if (instance !== "a" && instance !== "b") throw new Error("sessioncacheguard needs ?instance=a|b");
const errors: string[] = [];
window.addEventListener("error", (event) => errors.push(event.error?.stack ?? event.message));
window.addEventListener("unhandledrejection", (event) => errors.push(String(event.reason)));

// Observe our real outgoing deletion envelope rather than inventing a sourceId
// or replacing the channel factory. A separate native channel receives local
// posts too. Source discovery is exercised only after the deletion scenario.
let sourceId: string | null = null;
const ownedDeletions = new Set<string>();
const observer = new BroadcastChannel("evener.session-cache.v1");
observer.addEventListener("message", ({ data }) => {
  if (data.kind === "deletion" && data.refs.some((ref: string) => ownedDeletions.has(ref))) sourceId = data.sourceId;
});

function turn(): Turn & TurnModel {
  return {
    id: "turn-1",
    status: "completed",
    itemsView: "full",
    version: 1,
    items: [
      {
        id: "item-1",
        turnId: "turn-1",
        type: "agentMessage",
        text: "durable guard content",
        position: { entry: 1, item: 0 },
      },
    ],
  };
}

function record(ref: string): CachedSessionRecord {
  return {
    ref,
    threadId: ref.slice("local:".length),
    name: "sessioncacheguard seed",
    modelProvider: "guard",
    model: "guard",
    savedAt: Date.now(),
    history: {
      bootGeneration: "1",
      epoch: 1,
      incarnation: "guard-incarnation",
      length: 1,
      appliedGeneration: 0,
      issuedGeneration: 0,
      turns: [turn()],
    },
  };
}

function response(ref: string, requestGeneration: number | undefined): ThreadReadResponse {
  const cached = record(ref);
  return {
    thread: {
      id: cached.threadId,
      sessionId: cached.threadId,
      name: "sessioncacheguard live",
      preview: "sessioncacheguard",
      ephemeral: false,
      modelProvider: "guard",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "idle" },
      cwd: "/tmp/sessioncacheguard",
      cliVersion: "guard",
      source: "evener",
      evener: {
        ref,
        queue: { revision: 0 },
        capabilities: {
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
        },
      },
      turns: [turn()],
    },
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "guard-incarnation", length: 1 },
    requestGeneration,
  };
}

function deferred() {
  let resolve: () => void;
  return {
    promise: new Promise<void>((done) => {
      resolve = done;
    }),
    resolve: () => resolve(),
  };
}

interface HeldRead {
  entered: () => void;
  admission: Promise<void>;
  release: () => void;
  hydration: Promise<void>;
}
const heldReads = new Map<string, HeldRead>();
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", async (params) => {
    if (!params.ref) throw new Error("sessioncacheguard read needs a ref");
    const held = heldReads.get(params.ref);
    held?.entered();
    await held?.admission;
    return response(params.ref, params.requestGeneration);
  });
  fake.on("evener/session/delete", ({ ref }) => ({
    deleted: [ref.slice("local:".length)],
    skipped: [],
    navigation: { generation_id: "guard", targets: [] },
  }));
  connectionStore.getState().connect(fake);
  return fake;
}
let fake = connectFakeClient();
const reader = new SessionCacheIndexedDB();

function snapshot(ref: string) {
  const state = threadsStore.getState();
  return {
    instance,
    sourceId,
    deletedRefs: [...state.deletedRefs].sort(),
    lifetime: state.cacheLifetimes.get(ref) ?? null,
    lifetimes: [...state.cacheLifetimes].sort(([a], [b]) => a.localeCompare(b)),
    clearInFlight: state.clearInFlight ?? null,
    errors: [...errors],
  };
}

const api = {
  snapshot,
  async seed(ref: string) {
    await reader.get(ref, Date.now());
    return reader.put(record(ref), reader.observedEpoch ?? 0, Date.now());
  },
  async durable(ref: string) {
    const found = await reader.get(ref, Date.now());
    return { record: found?.record ?? null, epoch: reader.observedEpoch ?? null };
  },
  async delete(ref: string) {
    ownedDeletions.add(ref);
    return deleteSession(fake, ref);
  },
  clear: clearCachedSessions,
  async armLease(ref: string) {
    const entered = deferred();
    const admission = deferred();
    const held: HeldRead = {
      entered: entered.resolve,
      admission: admission.promise,
      release: admission.resolve,
      hydration: Promise.resolve(),
    };
    heldReads.set(ref, held);
    held.hydration = threadsStore.getState().ensureThread(ref);
    // thread/read starts after the cache lookup, but cannot publish a model
    // or schedule a cache write while admission is held. The runner reads ONLY
    // snapshot() between this point and its channel-delivery assertion.
    await Promise.race([
      entered.promise,
      held.hydration.then(() => {
        throw new Error("held read never entered");
      }),
    ]);
    return snapshot(ref);
  },
  async fireWrite(ref: string) {
    const held = heldReads.get(ref);
    if (!held) throw new Error(`no armed lease for ${ref}`);
    held.release();
    await held.hydration;
    heldReads.delete(ref);
    // Last-owner release synchronously cancels the debounce and invokes the
    // REAL fire-time gates on the hydrated model, before dropping its lifetime.
    threadsStore.getState().releaseThread(ref);
    await settleProjectionWorkForTests();
    return api.durable(ref);
  },
  async reset() {
    for (const ref of heldReads.keys()) await api.fireWrite(ref);
    await settleProjectionWorkForTests();
    resetThreadsStoreForTests();
    const result = await reader.clear(); // fixture cleanup, deliberately no broadcast
    if (!result.committed) throw new Error("sessioncacheguard reset did not commit");
    fake = connectFakeClient();
    return result;
  },
};

Object.assign(window, { [`${instance}Guard`]: api });
