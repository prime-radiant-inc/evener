import type { AnyNotification, MethodTypes, ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { observePendingHydrationRetirementsForTests, resetThreadsStoreForTests, threadsStore } from "./threads";

const ref = "local:post-cut";
const pushedGoal = { objective: "accepted after-cut goal", status: "active" as const, iterations: 3 };
const goalFrame: AnyNotification = {
  method: "evener/goal/updated",
  params: { ref, threadId: `thr_${ref}`, goal: pushedGoal },
};
const historyFrame: AnyNotification = {
  method: "history/updated",
  params: {
    ref,
    threadId: `thr_${ref}`,
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 1 },
    turns: [{ id: "turn_after_cut", status: "completed", itemsView: "full" }],
    items: [
      {
        type: "agentMessage",
        id: "item_after_cut",
        turnId: "turn_after_cut",
        position: { entry: 0, item: 0 },
        text: "accepted after-cut recorded text",
        status: "completed",
      },
    ],
  },
};

beforeEach(() => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
});
afterEach(() => {
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  vi.unstubAllGlobals();
});

function emptyCurrentRead(params: MethodTypes["thread/read"]["params"]): ThreadReadResponse {
  return {
    ...currentRead(ref),
    thread: wireThread(ref, { turns: [] }),
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration: params.requestGeneration,
    olderCursor: undefined,
    overlay: [],
  };
}

function client() {
  const value = new FakeClient("ready");
  value.on("thread/unsubscribe", () => ({}));
  connectionStore.setState({ client: value, state: "ready" });
  return value;
}

async function afterCut(frame: AnyNotification) {
  const value = client();
  const reached = deferred<MethodTypes["thread/read"]["params"]>();
  const read = deferred<ThreadReadResponse>();
  value.on("thread/read", (params) => {
    reached.resolve(params);
    return read.promise;
  });
  const retired: Array<{ ref: string; retainedNotificationCount: number; notificationCount: number }> = [];
  const unsubscribe = observePendingHydrationRetirementsForTests((event) => retired.push(event));
  try {
    const ensuring = threadsStore.getState().ensureThread(ref);
    const params = await reached.promise;
    const response = emptyCurrentRead(params);
    const cut = { reached: false };
    Object.defineProperty(response, "olderCursor", {
      configurable: true,
      enumerable: true,
      get: () => {
        cut.reached = true;
        return undefined;
      },
    });
    read.resolve(response);
    for (let i = 0; i < 20 && !cut.reached; i += 1) await Promise.resolve();
    expect(cut.reached, "the actual hydration response cut must run").toBe(true);
    expect(threadsStore.getState().threads.has(ref), "the snapshot must not have published before the frame").toBe(
      false,
    );
    value.emitNotification(frame);
    await ensuring;
    expect(value.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(
      retired.find((event) => event.ref === ref)?.retainedNotificationCount,
      "the accepted post-cut tail must contain this frame",
    ).toBe(1);
    expect(retired.find((event) => event.ref === ref)?.notificationCount, "publication retires the tail").toBe(0);
    return threadsStore.getState().threads.get(ref);
  } finally {
    unsubscribe();
  }
}

test("ordinary current-wire after-cut goal survives the first published snapshot", async () => {
  expect((await afterCut(goalFrame))?.goal).toEqual(pushedGoal);
});

test("ordinary current-wire after-cut recorded update survives the first published snapshot", async () => {
  const model = await afterCut(historyFrame);
  expect(model?.turns.map((turn) => turn.id)).toEqual(["turn_after_cut"]);
  expect(model?.turns[0]?.items[0]?.text).toBe("accepted after-cut recorded text");
});

test("control, the identical current-wire goal delivered after publication is accepted", async () => {
  const value = client();
  value.on("thread/read", emptyCurrentRead);
  await threadsStore.getState().ensureThread(ref);
  expect(threadsStore.getState().threads.get(ref)?.goal).toBeNull();
  value.emitNotification(goalFrame);
  expect(threadsStore.getState().threads.get(ref)?.goal).toEqual(pushedGoal);
  expect(value.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
});

test("a later repository publication keeps the recorded post-cut tail", async () => {
  expect((await afterCut(historyFrame))?.turns[0]?.items[0]?.text).toBe("accepted after-cut recorded text");
  const value = connectionStore.getState().client as FakeClient;
  value.emitNotification(goalFrame);
  const model = threadsStore.getState().threads.get(ref);
  expect(model?.goal).toEqual(pushedGoal);
  expect(model?.turns[0]?.items[0]?.text).toBe("accepted after-cut recorded text");
});

test("a source-stale post-cut frame does not bank an applied timestamp", async () => {
  const stale: AnyNotification = {
    ...historyFrame,
    params: { ...historyFrame.params, bootGeneration: "0" },
  };
  expect((await afterCut(stale))?.turns).toEqual([]);
  expect(threadsStore.getState().frameTimes.get(ref)).toBeUndefined();
});
