// The web store's half of boot generation adoption: whichever source identity
// the store adopts for a ref — a successful live read, a live history frame, or
// a failed read that attests one — reaches the ref's SessionActivityStore, so a
// replaced daemon's held delegate row stops showing a wait nothing can release.
// A successful `daemonless` answer is saved-history provenance, not proof the
// daemon exited, so it adopts nothing; a *failed* `daemonless` answer is the
// hub's attested absence and does retire the hold. A forwarded `unknown-owner`
// failure names no source: it must adopt nothing and retire nothing.
import "fake-indexeddb/auto";
import type { AnyNotification, SessionDelegatesResponse, ThreadReadResponse } from "@evener/appwire-client";
import { DAEMONLESS_BOOT_GENERATION, type SessionActivityStore, WireError } from "@evener/appwire-client";
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

/** A delegate row wearing a hold sample the replaced daemon numbered, or the
 * replacement's own row when `held` is false. */
function delegates(held = true): SessionDelegatesResponse {
  return {
    // Live and push-eligible, so a delegate frame is merged rather than
    // re-read: the point of the daemonless case is that the still-running
    // daemon's frames keep reaching the row.
    context: { ...activityContext(REF), reportPreview: true },
    scope: "session",
    delegates: [
      activityDelegate({
        ownerRef: REF,
        rootRef: REF,
        ...(held ? { holdSequence: 7, runningJobCount: 0 } : {}),
      }),
    ],
    page: { complete: true, issues: [] },
  };
}

/** The still-running daemon's own next hold edge, stamped with the generation
 * its publisher booted under. */
function holdFrame(bootGeneration: string): AnyNotification {
  return {
    method: "evener/delegate/updated",
    params: {
      ref: REF,
      threadId: THREAD_ID,
      bootGeneration,
      delegate: {
        runGeneration: 1,
        delegateId: "delegate-1",
        ownerSessionId: "owner",
        logicalOwnerSessionId: "owner",
        rootSessionId: "owner",
        childSessionId: "child",
        childRef: "remote:child",
        transcriptRef: "remote:child",
        type: "delegate",
        lifecycle: "running",
        phase: "running",
        status: "running",
        terminal: false,
        resumable: true,
        needsAttention: false,
        projectionRevision: 1,
        drainHeldByJobs: true,
        holdSequence: 8,
        runningJobCount: 2,
      },
    },
  } as AnyNotification;
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
    let page = delegates();
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("1"));
      fake.on("evener/thread/delegates/list", () => page);
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));

      // The daemon reboots as generation 2 and the store adopts it. The page a
      // read serves names no boot generation, so the adoption does not wipe the
      // hold the loaded row wears: it reconciles, and the replacement's own
      // page -- which reports no wait -- is what settles the row.
      page = delegates(false);
      client.on("thread/read", () => read("2"));
      await threadsStore.getState().refreshThread(REF);

      await waitFor(() => expect(heldSequence(activity.store)).toBeUndefined());
    } finally {
      stop();
      activity.release();
    }
  });

  test("a forwarded unknown-owner failure adopts nothing and keeps the held row", async () => {
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("1"));
      fake.on("evener/thread/delegates/list", () => delegates());
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
      fake.on("evener/thread/delegates/list", () => delegates());
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
      fake.on("evener/thread/delegates/list", () => delegates());
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

  test("a successful daemonless read keeps the live source and its hold", async () => {
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("1"));
      fake.on("evener/thread/delegates/list", () => delegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));

      // A relay outage makes the transcript refresh answer from saved history.
      // The hub stamps `daemonless` because it fell back, not because it proved
      // the daemon exited (docs/product/history-content.md, "A successful saved
      // fallback carries daemonless"): the live source keeps its hold.
      client.on("thread/read", () => read(DAEMONLESS_BOOT_GENERATION));
      await threadsStore.getState().refreshThread(REF);
      expect(heldSequence(activity.store)).toBe(7);

      // And the boot was not adopted, so a frame from the daemon that is still
      // running is applied rather than judged retired.
      client.emitNotification(holdFrame("1"));
      await waitFor(() => expect(heldSequence(activity.store)).toBe(8));
      expect(activity.store.getSnapshot().delegates.rows[0]?.drainHeldByJobs).toBe(true);
    } finally {
      stop();
      activity.release();
    }
  });

  test("a failed daemonless read attests absence and retires the live hold", async () => {
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => read("1"));
      fake.on("evener/thread/delegates/list", () => delegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));

      // A failed read is the case that does carry ownership: the hub stamps
      // `daemonless` on a roster showing nobody owns the thread, or on a ref no
      // daemon here serves. Nothing can be holding the run open.
      client.on("thread/read", () => {
        throw new WireError("thread history failed at entry 3", -32000, {
          evenerErrorInfo: "transcriptHistoryFailed",
          bootGeneration: DAEMONLESS_BOOT_GENERATION,
        });
      });
      await threadsStore
        .getState()
        .refreshThread(REF)
        .catch(() => undefined);

      expect(heldSequence(activity.store)).toBeUndefined();
    } finally {
      stop();
      activity.release();
    }
  });
});
