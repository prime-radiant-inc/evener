import "fake-indexeddb/auto";
import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { connectionStore } from "./connection";
import { acquireSessionActivity } from "./sessionActivity";
import { activityClient } from "./sessionActivityTestUtils";
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
