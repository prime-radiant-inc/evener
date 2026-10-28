import type { AnyNotification, MethodTypes, ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { productionEntryTranscriptForTests } from "./entryTranscriptRoutes";
import { observePendingHydrationRetirementsForTests, resetThreadsStoreForTests, threadsStore } from "./threads";

const ref = "local:recorded-accounting";
type FrameKind = "history" | "overlay" | "metadata";
const bankObservers: Array<() => void> = [];
beforeEach(() => {
  connectionStore.setState({ client: null, state: "idle", serverInfo: undefined });
  resetThreadsStoreForTests();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
});
afterEach(() => {
  for (const unsubscribe of bankObservers.splice(0)) unsubscribe();
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function packet(): ThreadReadResponse {
  const response = currentRead(ref);
  response.thread.status = { type: "idle" };
  response.thread.turns = [];
  delete response.thread.evener.activeTurnId;
  response.overlay = [];
  response.snapshot = { incarnation: "inc-1", length: 0 };
  delete response.olderCursor;
  return response;
}
async function acquire() {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => ({ ...packet(), requestGeneration: params.requestGeneration }));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  await threadsStore.getState().ensureThread(ref);
  const repository = productionEntryTranscriptForTests(ref).repository;
  expect(repository).toBeDefined();
  if (!repository) throw new Error("ordinary repository absent");
  expect(threadsStore.getState().frameTimes.has(ref)).toBe(false);
  return { fake, repository };
}
function observeBanks() {
  const banks: Array<{ count: number; handleNotification: boolean; hydrationPublication: boolean }> = [];
  bankObservers.push(
    threadsStore.subscribe((state, previous) => {
      const times = state.frameTimes.get(ref);
      if (times === previous.frameTimes.get(ref)) return;
      const stack = new Error().stack ?? "";
      banks.push({
        count: times?.length ?? 0,
        handleNotification: stack.includes("handleNotification"),
        hydrationPublication: stack.includes("publishThreadHydration"),
      });
    }),
  );
  return banks;
}
function frame(kind: FrameKind): AnyNotification {
  const response = packet();
  const identity = { ref, threadId: response.thread.id };
  if (kind === "metadata") return { method: "evener/jobs/treeUpdated", params: { ...identity, revision: 7 } };
  return kind === "history"
    ? {
        method: "history/updated",
        params: {
          ...identity,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [
            {
              type: "agentMessage",
              id: "recorded",
              transcriptKey: "recorded",
              turnId: "turn_1",
              position: { entry: 1, item: 0 },
              version: 1,
              text: "recorded text",
            },
          ],
        },
      }
    : {
        method: "overlay/upserted",
        params: {
          ...identity,
          item: {
            key: "notice:accounting",
            kind: "notice",
            anchor: { entry: 0, item: 0 },
            turnId: "turn_1",
            item: { type: "agentMessage", id: "overlay", text: "overlay text" },
          },
        },
      };
}
function expectContent(kind: FrameKind, exactIdentity = true) {
  const model = productionEntryTranscriptForTests(ref).repository?.getSnapshot();
  if (kind === "history")
    expect(model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["recorded text"]);
  else if (kind === "overlay") expect(model?.overlay?.["notice:accounting"]?.item.text).toBe("overlay text");
  else expect(model?.jobsTreeRevision).toBe(7);
  if (exactIdentity) expect(threadsStore.getState().threads.get(ref)).toBe(model);
  else expect(threadsStore.getState().threads.get(ref)).toStrictEqual(model);
}

test.each(["history", "overlay", "metadata"] as const)(
  "ordinary accepted %s input banks exactly one frame",
  async (kind) => {
    const { fake, repository } = await acquire();
    const banks = observeBanks();
    const accept = vi.spyOn(repository, "accept");
    const notification = frame(kind);
    fake.emitNotification(notification);
    expect(accept).toHaveBeenCalledTimes(1);
    expect(accept.mock.calls[0]?.[0]).toBe(notification);
    expect(accept.mock.results[0]?.value).toBe(true);
    expectContent(kind);
    expect(threadsStore.getState().frameTimes.get(ref)).toHaveLength(1);
    expect(banks).toEqual([{ count: 1, handleNotification: true, hydrationPublication: false }]);
  },
);

test("repository acceptance and a reached store publication bank the same frame only once", async () => {
  const { fake, repository } = await acquire();
  const accept = vi.spyOn(repository, "accept");
  const models: unknown[] = [];
  const unsubscribe = threadsStore.subscribe((state, previous) => {
    if (state.threads.get(ref) !== previous.threads.get(ref)) models.push(state.threads.get(ref));
  });
  try {
    fake.emitNotification({
      method: "thread/status/changed",
      params: { ref, threadId: packet().thread.id, status: { type: "active" } },
    });
    expect(accept).toHaveBeenCalledTimes(1);
    expect(accept.mock.results[0]?.value).toBe(true);
    expect(models).toHaveLength(2);
    expect(models[0]).toBe(repository.getSnapshot());
    expect(models[1]).toBe(threadsStore.getState().threads.get(ref));
    expect(models[1]).not.toBe(models[0]);
    expect(threadsStore.getState().threads.get(ref)?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().frameTimes.get(ref)).toHaveLength(1);
  } finally {
    unsubscribe();
  }
});

test.each(["history", "overlay", "metadata"] as const)(
  "pending %s acceptance banks live and replay cannot bank it twice",
  async (kind) => {
    const { fake, repository } = await acquire();
    const banks = observeBanks();
    const read = deferred<ThreadReadResponse>();
    const reached = deferred<MethodTypes["thread/read"]["params"]>();
    fake.on("thread/read", (params) => {
      reached.resolve(params);
      return read.promise;
    });
    const retired: Array<{ retainedNotificationCount: number }> = [];
    const unsubscribe = observePendingHydrationRetirementsForTests((event) => retired.push(event));
    try {
      const refreshing = threadsStore.getState().refreshThread(ref);
      const params = await reached.promise;
      const response = { ...packet(), requestGeneration: params.requestGeneration };
      let getterReached = false;
      Object.defineProperty(response, "olderCursor", {
        enumerable: true,
        configurable: true,
        get: () => {
          getterReached = true;
          return undefined;
        },
      });
      read.resolve(response);
      for (let i = 0; i < 20 && !getterReached; i += 1) await Promise.resolve();
      expect(getterReached).toBe(true);
      const originalAccept = repository.accept.bind(repository);
      const acceptance: Array<{ accepted: boolean; historyChanged: boolean }> = [];
      const accept = vi.spyOn(repository, "accept").mockImplementation((notification, context) => {
        const before = repository.getSnapshot()?.history?.turns;
        const accepted = originalAccept(notification, context);
        acceptance.push({ accepted, historyChanged: before !== repository.getSnapshot()?.history?.turns });
        return accepted;
      });
      const notification = frame(kind);
      fake.emitNotification(notification);
      expect(accept.mock.results.at(-1)?.value).toBe(true);
      expectContent(kind);
      expect(threadsStore.getState().frameTimes.get(ref)).toHaveLength(1);
      expect(banks).toEqual([{ count: 1, handleNotification: true, hydrationPublication: false }]);
      await refreshing;
      // This real retirement observation, not the getter, proves the frame remained in the post-cut tail.
      expect(retired.at(-1)?.retainedNotificationCount).toBe(1);
      expect(accept.mock.calls.map((call) => call[0])).toEqual([notification, notification]);
      expect(accept.mock.calls[0]?.[0]).toBe(notification);
      expect(accept.mock.calls[1]?.[0]).toBe(notification);
      expect(acceptance.map((call) => call.accepted)).toEqual(kind === "history" ? [true, false] : [true, true]);
      if (kind === "history") expect(acceptance.map((call) => call.historyChanged)).toEqual([true, false]);
      expectContent(kind, false);
      expect(threadsStore.getState().frameTimes.get(ref)).toHaveLength(1);
      expect(banks).toEqual([{ count: 1, handleNotification: true, hydrationPublication: false }]);
    } finally {
      unsubscribe();
    }
  },
);
