// @vitest-environment node
import { expect, test } from "vitest";
import { SessionActivityStore } from "./sessionActivityStore";
import { activityClient, activityRef, activityState, threadFixture } from "./sessionActivityTestUtils";
import { deferred } from "./testing/deferred";
import { callsTo } from "./testing/fakeClient";
import { acquireThreadSubscription } from "./threadSubscription";
import type { ThreadReadResponse } from "./types.gen";

test("shared leases expose only admitted runtime metadata, including after a rich read", async () => {
  const client = activityClient(),
    removed = deferred<void>();
  const response: ThreadReadResponse = {
    thread: {
      ...threadFixture().thread,
      id: "wire-root",
      sessionId: "root-session",
      status: { type: "active", activeFlags: ["waitingOnTool"] },
      turns: [{ id: "rich-turn", status: "completed", items: [], itemsView: "full" }],
    },
  };
  client.on("thread/read", () => response);
  client.on("thread/unsubscribe", () => {
    removed.resolve();
    return {};
  });
  const first = acquireThreadSubscription(client, activityRef),
    second = acquireThreadSubscription(client, activityRef);
  expect(first.metadata?.()).toBeNull();
  await Promise.all([first.ensure(), second.ensure()]);
  expect(callsTo(client, "thread/read")).toBe(1);
  expect(first.metadata?.()).toEqual({
    threadId: "wire-root",
    sessionId: "root-session",
    status: { type: "active", activeFlags: ["waitingOnTool"] },
  });
  expect(second.metadata?.()).toEqual(first.metadata?.());
  const rich: ThreadReadResponse = { thread: { ...response.thread, status: { type: "idle" } } };
  client.on("thread/read", () => rich);
  expect(await second.read({ includeTurns: true })).toBe(rich);
  expect(second.metadata?.()).toEqual({
    threadId: "wire-root",
    sessionId: "root-session",
    status: { type: "idle" },
  });
  expect(client.calls.filter((call) => call.method === "thread/read").map((call) => call.params)).toEqual([
    { ref: activityRef, includeTurns: false, subscribe: true, replaceSubscription: false },
    { ref: activityRef, includeTurns: true, subscribe: false, replaceSubscription: false },
  ]);
  first.release();
  expect(first.metadata?.()).toBeNull();
  expect(second.metadata?.()?.sessionId).toBe("root-session");
  expect(callsTo(client, "thread/unsubscribe")).toBe(0);
  second.release();
  expect(second.metadata?.()).toBeNull();
  await removed.promise;
  expect(callsTo(client, "thread/unsubscribe")).toBe(1);
});

test("metadata uses the thread identity when the resolved session id is absent", async () => {
  const client = activityClient();
  const response = { thread: { ...threadFixture().thread, id: "wire-only" } };
  Reflect.deleteProperty(response.thread, "sessionId");
  client.on("thread/read", () => response);
  const lease = acquireThreadSubscription(client, activityRef);
  await lease.ensure();
  expect(lease.metadata?.()).toEqual({ threadId: "wire-only", sessionId: "wire-only", status: { type: "idle" } });
  lease.release();
});

test("disconnect clears metadata and fences a late rich read from the former generation", async () => {
  const client = activityClient(),
    late = deferred<ThreadReadResponse>(),
    entered = deferred<void>();
  const lease = acquireThreadSubscription(client, activityRef);
  await lease.ensure();
  expect(lease.metadata?.()?.sessionId).toBe("session");
  client.on("thread/read", ({ includeTurns }) => {
    if (includeTurns) {
      entered.resolve();
      return late.promise;
    }
    return { thread: { ...threadFixture().thread, id: "wire-new", sessionId: "replacement-session" } };
  });
  const prior = lease.read({ includeTurns: true });
  await entered.promise;
  client.emitStateChange("reconnecting");
  expect(lease.metadata?.()).toBeNull();
  client.emitReady();
  await lease.ensure();
  expect(lease.metadata?.()?.sessionId).toBe("replacement-session");
  late.resolve(threadFixture());
  await prior;
  expect(lease.metadata?.()).toEqual({
    threadId: "wire-new",
    sessionId: "replacement-session",
    status: { type: "idle" },
  });
  expect(callsTo(client, "thread/read")).toBe(3);
  lease.release();
});

test("a late old subscribe cannot publish metadata after reconnect", async () => {
  const client = activityClient(),
    late = deferred<ThreadReadResponse>(),
    entered = deferred<void>();
  client.on("thread/read", () => {
    if (callsTo(client, "thread/read") === 1) {
      entered.resolve();
      return late.promise;
    }
    return { thread: { ...threadFixture().thread, id: "wire-new", sessionId: "replacement-session" } };
  });
  const lease = acquireThreadSubscription(client, activityRef),
    prior = lease.ensure();
  await entered.promise;
  client.emitStateChange("reconnecting");
  expect(lease.metadata?.()).toBeNull();
  client.emitReady();
  await lease.ensure();
  late.resolve(threadFixture());
  await prior;
  expect(lease.metadata?.()?.sessionId).toBe("replacement-session");
  expect(callsTo(client, "thread/read")).toBe(2);
  lease.release();
});

test.each([
  { order: "transcript-first", releaseOrder: "transcript-first" },
  { order: "transcript-first", releaseOrder: "activity-first" },
  { order: "activity-first", releaseOrder: "transcript-first" },
  { order: "activity-first", releaseOrder: "activity-first" },
])(
  "transcript lease and activity share membership: acquire $order, release $releaseOrder",
  async ({ order, releaseOrder }) => {
    const client = activityClient(),
      removed = deferred<void>();
    client.on("thread/unsubscribe", () => {
      removed.resolve();
      return {};
    });
    const activity = new SessionActivityStore(client, activityRef),
      transcript = acquireThreadSubscription(client, activityRef);
    const params = { includeTurns: true, itemsView: "fragment", itemLimit: 25, requestGeneration: 7 };
    if (order === "transcript-first") await transcript.read(params);
    activity.start();
    await activityState(activity, () => activity.getSnapshot().summary !== null);
    if (order === "activity-first") await transcript.read(params);
    expect(
      client.calls.filter((call) => call.method === "thread/read" && (call.params as { subscribe: boolean }).subscribe),
    ).toHaveLength(1);
    expect(
      client.calls.find(
        (call) => call.method === "thread/read" && (call.params as { includeTurns: boolean }).includeTurns,
      )?.params,
    ).toEqual({ ...params, ref: activityRef, subscribe: order === "transcript-first", replaceSubscription: false });
    if (releaseOrder === "transcript-first") {
      transcript.release();
      expect(callsTo(client, "thread/unsubscribe")).toBe(0);
      activity.dispose();
    } else {
      activity.dispose();
      expect(callsTo(client, "thread/unsubscribe")).toBe(0);
      transcript.release();
    }
    await removed.promise;
    expect(callsTo(client, "thread/unsubscribe")).toBe(1);
    transcript.release();
    activity.dispose();
    expect(callsTo(client, "thread/unsubscribe")).toBe(1);
  },
);
test("reacquiring during pending subscribe preserves active membership", async () => {
  const client = activityClient(),
    pending = deferred<ThreadReadResponse>(),
    entered = deferred<void>();
  client.on("thread/read", () => {
    entered.resolve();
    return pending.promise;
  });
  const first = acquireThreadSubscription(client, activityRef),
    one = first.ensure();
  await entered.promise;
  first.release();
  const second = acquireThreadSubscription(client, activityRef),
    two = second.ensure();
  pending.resolve(threadFixture());
  await Promise.all([one, two]);
  expect(callsTo(client, "thread/read")).toBe(1);
  expect(callsTo(client, "thread/unsubscribe")).toBe(0);
  second.release();
});
test("last release cleans a subscription that completes late", async () => {
  const client = activityClient(),
    pending = deferred<ThreadReadResponse>(),
    entered = deferred<void>(),
    removed = deferred<void>();
  client.on("thread/read", () => {
    entered.resolve();
    return pending.promise;
  });
  client.on("thread/unsubscribe", () => {
    removed.resolve();
    return {};
  });
  const lease = acquireThreadSubscription(client, activityRef),
    ensure = lease.ensure();
  await entered.promise;
  lease.release();
  pending.resolve(threadFixture());
  await ensure;
  await removed.promise;
  expect(callsTo(client, "thread/unsubscribe")).toBe(1);
});
test("reacquiring during pending unsubscribe waits then resubscribes", async () => {
  const client = activityClient(),
    pending = deferred<Record<string, never>>(),
    removing = deferred<void>();
  client.on("thread/unsubscribe", () => {
    removing.resolve();
    return pending.promise;
  });
  const first = acquireThreadSubscription(client, activityRef);
  await first.ensure();
  first.release();
  await removing.promise;
  const second = acquireThreadSubscription(client, activityRef),
    ensure = second.ensure();
  expect(callsTo(client, "thread/read")).toBe(1);
  pending.resolve({});
  await ensure;
  expect(callsTo(client, "thread/read")).toBe(2);
  second.release();
});
test("failed subscribe retries on the owner's attempt and rich reads are independent", async () => {
  const client = activityClient(),
    rich = deferred<ThreadReadResponse>(),
    entered = deferred<void>();
  let fail = true;
  client.on("thread/read", ({ includeTurns }) => {
    if (fail) throw new Error("temporary");
    if (includeTurns) {
      entered.resolve();
      return rich.promise;
    }
    return threadFixture();
  });
  const lease = acquireThreadSubscription(client, activityRef);
  await expect(lease.ensure()).rejects.toThrow("temporary");
  fail = false;
  await lease.ensure();
  const read = lease.read({ includeTurns: true });
  await entered.promise;
  await lease.read({ includeTurns: false, requestGeneration: 3 });
  expect(callsTo(client, "thread/read")).toBe(4);
  rich.resolve(threadFixture());
  await read;
  lease.release();
});
test("reconnect fences an old subscribe result", async () => {
  const client = activityClient(),
    old = deferred<ThreadReadResponse>(),
    entered = deferred<void>();
  client.on("thread/read", () => {
    if (callsTo(client, "thread/read") === 1) {
      entered.resolve();
      return old.promise;
    }
    return threadFixture();
  });
  const lease = acquireThreadSubscription(client, activityRef),
    prior = lease.ensure();
  await entered.promise;
  client.emitStateChange("reconnecting");
  client.emitReady();
  await lease.ensure();
  old.resolve(threadFixture());
  await prior;
  await lease.ensure();
  expect(callsTo(client, "thread/read")).toBe(2);
  lease.release();
});
