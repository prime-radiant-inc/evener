// @vitest-environment node
import { expect, test } from "vitest";
import { SessionActivityStore } from "./sessionActivityStore";
import { activityClient, activityRef, activityState, threadFixture } from "./sessionActivityTestUtils";
import { deferred } from "./testing/deferred";
import { callsTo } from "./testing/fakeClient";
import { acquireThreadSubscription } from "./threadSubscription";
import type { ThreadReadResponse } from "./types.gen";

test.each(["transcript-first", "activity-first"])(
  "transcript lease and activity share membership: %s",
  async (order) => {
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
    if (order === "transcript-first") {
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
