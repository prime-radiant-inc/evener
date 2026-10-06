import "fake-indexeddb/auto";
import {
  projectSessionActivity,
  type SessionActivitySummary,
  type SessionDelegatesResponse,
} from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { connectionStore } from "./connection";
import { acquireSessionActivity } from "./sessionActivity";
import { activityClient, activityContext, activityDelegate, activitySummary } from "./sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "./threads";

afterEach(() => {
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
});

describe("activity and transcript subscription ownership", () => {
  for (const activityFirst of [true, false]) {
    for (const releaseActivityFirst of [true, false]) {
      it(`shares workspace alias membership: activity first ${activityFirst}, release activity first ${releaseActivityFirst}`, async () => {
        const client = activityClient();
        const ref = "remote:workspace";
        connectionStore.getState().connect(client);
        const transcript = threadsStore.getState();
        let activity: ReturnType<typeof acquireSessionActivity>;
        if (activityFirst) {
          activity = acquireSessionActivity(client, ref);
          await waitFor(() => expect(activity.store.getSnapshot().summary).not.toBeNull());
          await transcript.ensureThread(ref);
        } else {
          await transcript.ensureThread(ref);
          activity = acquireSessionActivity(client, ref);
          await waitFor(() => expect(activity.store.getSnapshot().summary).not.toBeNull());
        }
        expect(
          client.calls.filter(
            (call) => call.method === "thread/read" && (call.params as { subscribe: boolean }).subscribe,
          ),
        ).toHaveLength(1);
        expect(
          client.calls
            .filter((call) => call.method === "thread/read")
            .every(
              (call) =>
                (call.params as { ref: string }).ref === ref &&
                (call.params as { replaceSubscription: boolean }).replaceSubscription === false,
            ),
        ).toBe(true);
        if (!activityFirst)
          expect(client.calls[0]).toMatchObject({ method: "thread/read", params: { ref, includeTurns: true } });
        if (releaseActivityFirst) activity.release();
        else transcript.releaseThread(ref);
        await Promise.resolve();
        expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
        if (releaseActivityFirst) transcript.releaseThread(ref);
        else activity.release();
        await waitFor(() =>
          expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1),
        );
        expect(client.calls.find((call) => call.method === "thread/unsubscribe")?.params).toEqual({ ref });
      });
    }
  }
});

it("fences a late pre-clear alias summary and collection at the resync boundary", async () => {
  const client = activityClient();
  const ref = "remote:workspace";
  const oldSummary = deferred<SessionActivitySummary>();
  const oldDelegates = deferred<SessionDelegatesResponse>();
  const nextSummary = deferred<SessionActivitySummary>();
  const nextDelegates = deferred<SessionDelegatesResponse>();
  let summaries = 0,
    delegates = 0;
  client.on("evener/thread/activity/read", ({ scope }) =>
    ++summaries === 1 ? oldSummary.promise : nextSummary.promise,
  );
  client.on("evener/thread/delegates/list", () => (++delegates === 1 ? oldDelegates.promise : nextDelegates.promise));
  connectionStore.getState().connect(client);
  await threadsStore.getState().ensureThread(ref);
  const activity = acquireSessionActivity(client, ref);
  const stop = activity.store.observe("delegates");
  try {
    await waitFor(() => expect(delegates).toBe(1));
    client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: "replacement" } });
    oldSummary.resolve(activitySummary(ref));
    oldDelegates.resolve({
      context: activityContext(ref),
      scope: "session",
      delegates: [activityDelegate()],
      page: { complete: true, issues: [] },
    });
    await waitFor(() => expect(summaries).toBe(2));
    expect(activity.store.getSnapshot().summary?.context.sessionId).not.toBe("owner");
    expect(activity.store.getSnapshot().delegates.rows).toHaveLength(0);
  } finally {
    stop();
    activity.release();
    threadsStore.getState().releaseThread(ref);
    nextSummary.resolve(activitySummary(ref));
    nextDelegates.resolve({
      context: activityContext(ref),
      scope: "session",
      delegates: [],
      page: { complete: true, issues: [] },
    });
  }
});

it("never presents prior-session rows under a replacement alias summary", async () => {
  const client = activityClient(),
    ref = "remote:workspace";
  connectionStore.getState().connect(client);
  await threadsStore.getState().ensureThread(ref);
  const activity = acquireSessionActivity(client, ref),
    stop = activity.store.observe("delegates");
  await waitFor(() => expect(activity.store.getSnapshot().delegates.rows).toHaveLength(1));
  const replacement = activitySummary(ref);
  replacement.context = {
    ...activityContext(ref),
    sessionId: "replacement",
    ref: "remote:new-daemon",
    epoch: "new-epoch",
  };
  const pending = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/activity/read", ({ scope }) => replacement);
  client.on("evener/thread/delegates/list", () => pending.promise);
  try {
    client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: "replacement" } });
    await waitFor(() => expect(activity.store.getSnapshot().context?.sessionId).toBe("replacement"));
    const presentation = projectSessionActivity(activity.store.getSnapshot());
    expect(presentation.tree?.root.entries).toHaveLength(0);
    expect(
      client.calls
        .filter((call) => call.method === "thread/read")
        .every((call) => (call.params as { ref: string }).ref === ref),
    ).toBe(true);
    expect(
      client.calls.filter((call) => call.method === "thread/read" && (call.params as { subscribe: boolean }).subscribe),
    ).toHaveLength(1);
  } finally {
    stop();
    activity.release();
    threadsStore.getState().releaseThread(ref);
    pending.resolve({
      context: replacement.context,
      scope: "session",
      delegates: [],
      page: { complete: true, issues: [] },
    });
  }
});
