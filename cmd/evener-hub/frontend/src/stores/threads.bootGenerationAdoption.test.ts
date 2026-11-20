// The web store's half of boot generation adoption: whichever source identity
// the store adopts for a ref — a successful read, a live history frame, or a
// failed read that still attests one — reaches the ref's SessionActivityStore,
// so a replaced daemon's held delegate row stops showing a wait nothing can
// release. A forwarded `unknown-owner` failure names no source: it must adopt
// nothing and retire nothing.
import "fake-indexeddb/auto";
import type { SessionDelegatesResponse, ThreadReadResponse } from "@evener/appwire-client";
import { type SessionActivityStore, WireError } from "@evener/appwire-client";
import { waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { ALL_CAPABILITIES } from "../threadTestUtils";
import { resetConnectionStoreForTests } from "./connection";
import { acquireSessionActivity } from "./sessionActivity";
import { activityContext, activityDelegate } from "./sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "./threads";

const REF = "remote:owner";
const THREAD_ID = "owner";

function read(bootGeneration: string | undefined): ThreadReadResponse {
  return {
    thread: {
      id: THREAD_ID,
      sessionId: "owner",
      preview: "",
      ephemeral: false,
      modelProvider: "test/provider",
      createdAt: 0,
      updatedAt: 0,
      status: { type: "idle" },
      cwd: "/scratch",
      cliVersion: "test",
      source: "evener",
      evener: { ref: REF, capabilities: ALL_CAPABILITIES, queue: { revision: 0 } },
    },
    ...(bootGeneration === undefined ? {} : { bootGeneration }),
    epoch: 0,
    snapshot: { incarnation: "inc-a", length: 0 },
    overlay: [],
  };
}

/** A delegate row wearing a hold sample the replaced daemon numbered. */
function heldDelegates(): SessionDelegatesResponse {
  return {
    context: activityContext(REF),
    scope: "session",
    delegates: [activityDelegate({ ownerRef: REF, rootRef: REF, holdSequence: 7, runningJobCount: 0 })],
    page: { complete: true, issues: [] },
  };
}

function heldSequence(store: SessionActivityStore): number | undefined {
  return store.getSnapshot().delegates.rows[0]?.holdSequence;
}

beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
});

afterEach(() => {
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();
});

describe("adopting a source identity reaches the ref's activity store", () => {
  test("a replacement read retires the replaced generation's held delegate row", async () => {
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("1"));
      fake.on("evener/thread/delegates/list", () => heldDelegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));

      // The daemon reboots as generation 2 and the store adopts it.
      client.on("thread/read", () => read("2"));
      await threadsStore.getState().refreshThread(REF);

      expect(heldSequence(activity.store)).toBeUndefined();
    } finally {
      stop();
      activity.release();
    }
  });

  test("a forwarded unknown-owner failure adopts nothing and keeps the held row", async () => {
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("1"));
      fake.on("evener/thread/delegates/list", () => heldDelegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));

      // A remote hub that could not establish who serves the thread forwards
      // this: a failure that names no source, so nothing a source published
      // may be retired on its word.
      client.on("thread/read", () => {
        throw new WireError("thread history failed at entry 3", -32000, {
          evenerErrorInfo: "transcriptHistoryFailed",
          bootGeneration: "unknown-owner",
        });
      });
      await threadsStore
        .getState()
        .refreshThread(REF)
        .catch(() => undefined);

      expect(heldSequence(activity.store)).toBe(7);
    } finally {
      stop();
      activity.release();
    }
  });

  test("a late frame from the already-retired generation does not clear the replacement's row", async () => {
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("2"));
      fake.on("evener/thread/delegates/list", () => heldDelegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));

      // Generation 1 is the generation this history already moved past: the
      // reducer discards such a frame's content, so its stamp is no source to
      // adopt and no reason to clear what generation 2's rows wear.
      client.emitNotification({
        method: "overlay/upserted",
        params: {
          ref: REF,
          threadId: THREAD_ID,
          bootGeneration: "1",
          item: { key: "stale", kind: "stream", item: { type: "agentMessage", id: "stale", text: "stale" } },
        },
      } as never);

      expect(heldSequence(activity.store)).toBe(7);
    } finally {
      stop();
      activity.release();
    }
  });

  test("a read that names no generation adopts nothing and keeps the held row", async () => {
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("1"));
      fake.on("evener/thread/delegates/list", () => heldDelegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));

      // An older peer omits the field outright: it says nothing about identity.
      client.on("thread/read", () => read(undefined));
      await threadsStore.getState().refreshThread(REF);

      expect(heldSequence(activity.store)).toBe(7);
    } finally {
      stop();
      activity.release();
    }
  });
});
