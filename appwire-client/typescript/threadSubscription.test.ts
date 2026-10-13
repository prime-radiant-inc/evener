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
// A departed holder's still-pending acquisition must not block a fresh
// lease: the last holder released while the subscribing request hung, so the
// reopened reader supersedes the dead acquisition with its own subscribe
// instead of queueing behind a request whose owner departed. A held
// transition (holders > 0) still joins — the concurrent-mount dedup.
test("a reopened lease's read does not queue behind a departed holder's pending acquisition", async () => {
  const client = activityClient(),
    hung = deferred<ThreadReadResponse>(),
    entered = deferred<void>();
  client.on("thread/read", () => {
    entered.resolve();
    return hung.promise;
  });
  client.on("thread/unsubscribe", () => ({}));
  const departed = acquireThreadSubscription(client, activityRef);
  const departedRead = departed.read({ includeTurns: false }).catch(() => undefined);
  await entered.promise;
  departed.release();

  // The fresh lease's read must answer from its OWN subscribe request,
  // while the departed one is still unresolved on the wire.
  const fresh = acquireThreadSubscription(client, activityRef);
  client.on("thread/read", () => ({ thread: { ...threadFixture().thread, id: "wire-fresh" } }));
  const response = await fresh.read({ includeTurns: false });
  expect(response.thread.id).toBe("wire-fresh");

  // The departed request settles whenever it settles; nothing waited on it,
  // and its late response never rewrites the fresh acquisition's state.
  hung.resolve(threadFixture());
  await departedRead;
  expect(fresh.metadata()?.threadId).toBe("wire-fresh");
  fresh.release();
});

// Reacquiring after the last holder departed while a subscribe is pending
// supersedes the dead acquisition (round-21 medium 1): the fresh lease's
// ensure issues its own request instead of joining — and inheriting the
// failure of — a request whose owner is gone.
test("reacquiring after a departed holder's pending subscribe supersedes it", async () => {
  const client = activityClient(),
    old = deferred<ThreadReadResponse>(),
    firstEntered = deferred<void>(),
    freshEntered = deferred<void>();
  let reads = 0;
  client.on("thread/read", () => {
    reads += 1;
    if (reads === 1) {
      firstEntered.resolve();
      return old.promise;
    }
    freshEntered.resolve();
    return { thread: { ...threadFixture().thread, id: "wire-fresh" } };
  });
  const first = acquireThreadSubscription(client, activityRef),
    one = first.ensure().then(
      () => "settled",
      () => "rejected",
    );
  await firstEntered.promise;
  first.release();

  // The reacquired lease's ensure must neither wait on the departed
  // request nor inherit its failure: the departed request failing lands on
  // the departed holder's caller alone.
  const second = acquireThreadSubscription(client, activityRef),
    two = second.ensure();
  old.reject(new Error("the departed acquisition failed"));
  await two;
  expect(reads).toBe(2);
  await freshEntered.promise;
  expect(await one).toBe("rejected");
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

// A superseded acquisition still owns its teardown: when its replacement
// fails and that holder departs, the membership must survive the original
// request's outstanding flight — its late success is then the only thing
// holding the wire subscription, and the settle must unsubscribe it
// (round-20 medium 2's superseded sibling of the test above).
test("a superseded acquisition's late success tears down the subscription nobody owns", async () => {
  const client = activityClient(),
    original = deferred<ThreadReadResponse>(),
    entered = deferred<void>(),
    removed = deferred<void>();
  let reads = 0;
  client.on("thread/read", () => {
    reads += 1;
    if (reads === 1) {
      entered.resolve();
      return original.promise;
    }
    return Promise.reject(new Error("the replacement acquisition failed"));
  });
  client.on("thread/unsubscribe", () => {
    removed.resolve();
    return {};
  });
  const first = acquireThreadSubscription(client, activityRef),
    firstRead = first.read({ includeTurns: false }).catch(() => undefined);
  await entered.promise;
  first.release();

  // The fresh lease's read supersedes the original; its request fails, and
  // its holder departs: nothing may delete the membership while the
  // original acquisition is still outstanding on the wire.
  const second = acquireThreadSubscription(client, activityRef);
  await second.read({ includeTurns: false }).catch(() => undefined);
  second.release();

  // The original request succeeds late: it is the only thing holding the
  // wire subscription — its settle must unsubscribe it.
  original.resolve(threadFixture());
  await firstRead;
  await removed.promise;
  expect(callsTo(client, "thread/unsubscribe")).toBe(1);
});

// The outstanding gate prevents a two-member split: without it, the failed
// replacement's last release deletes the membership while the original
// request is still on the wire, a fresh lease builds a second member, and
// the original's late settle would unsubscribe the new member's live
// subscription.
test("a late superseded success does not tear down a reopened membership's subscription", async () => {
  const client = activityClient(),
    original = deferred<ThreadReadResponse>(),
    entered = deferred<void>(),
    removed = deferred<void>();
  let reads = 0;
  client.on("thread/read", () => {
    reads += 1;
    if (reads === 1) {
      entered.resolve();
      return original.promise;
    }
    if (reads === 2) {
      return Promise.reject(new Error("the replacement acquisition failed"));
    }
    return { thread: { ...threadFixture().thread, id: "wire-third" } };
  });
  client.on("thread/unsubscribe", () => {
    removed.resolve();
    return {};
  });
  const first = acquireThreadSubscription(client, activityRef),
    firstRead = first.read({ includeTurns: false }).catch(() => undefined);
  await entered.promise;
  first.release();
  const second = acquireThreadSubscription(client, activityRef);
  await second.read({ includeTurns: false }).catch(() => undefined);
  second.release();

  // A third lease reopens and its acquisition subscribes while the original
  // is still outstanding on the wire.
  const third = acquireThreadSubscription(client, activityRef);
  await third.read({ includeTurns: false });

  // The original's late success is superseded by the third's live
  // subscription: no orphan, no teardown of the reopened membership's
  // subscription.
  original.resolve(threadFixture());
  await firstRead;
  expect(callsTo(client, "thread/unsubscribe")).toBe(0);

  // The third's own release tears down its subscription.
  third.release();
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
