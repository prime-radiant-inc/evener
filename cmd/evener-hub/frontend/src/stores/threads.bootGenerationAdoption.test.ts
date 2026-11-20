// The web store's half of boot generation adoption: whichever source identity
// the store adopts for a ref — a successful live read, a live history frame, or
// a failed read that attests one — reaches the ref's SessionActivityStore, so a
// replaced daemon's held delegate row stops showing a wait nothing can release.
// A successful `daemonless` answer is saved-history provenance, not proof the
// daemon exited, so it adopts nothing; a *failed* `daemonless` answer is the
// hub's attested absence and does retire the hold. A forwarded `unknown-owner`
// failure names no source: it must adopt nothing and retire nothing.
//
// Adoption is an OWNED attempt's fact. A read a newer attempt has superseded
// may not name the ref's source when it finally answers: its token names the
// daemon it was issued against, not the one serving the ref now.
import "fake-indexeddb/auto";
import type { AnyNotification, SessionDelegatesResponse, ThreadReadResponse } from "@evener/appwire-client";
import { DAEMONLESS_BOOT_GENERATION, type SessionActivityStore, WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
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

  // An ORIGINAL initial history read can outlive the daemon it was issued
  // against: a daemon replacement resync reads, publishes and adopts the new
  // daemon while that first attempt is still in flight. When the first attempt
  // finally answers it still names the DEPARTED daemon, and adopting it would
  // retire the replacement's freshly published hold — which nothing corrects,
  // because an unchanged hold emits no update of its own.
  test("an initial history read answering after a replacement resync does not re-adopt the departed daemon", async () => {
    const initial = deferred<ThreadReadResponse>();
    // The generation every ordinary read answers with, and the switch that
    // makes exactly one read hang: the initial attempt's.
    let generation = "1";
    let holdInitial = false;
    let initialIssued = false;
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => {
        if (holdInitial && !initialIssued) {
          initialIssued = true;
          return initial.promise;
        }
        return Promise.resolve(read(generation));
      });
      fake.on("evener/thread/delegates/list", () => delegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      // A lean watch establishes the ref's subscription and loads the held
      // delegate row from the daemon generation 1, so the initial read below
      // can be the one that hangs.
      await threadsStore.getState().watchThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));
      holdInitial = true;
      const opening = threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(initialIssued).toBe(true));

      // The daemon is replaced while the initial read is still in flight. The
      // replacement resync publishes generation 2 and adopts it, and the
      // replacement's own frame numbers a hold under its generation.
      generation = "2";
      await threadsStore.getState().refreshThread(REF);
      await waitFor(() => expect(activity.store.bootGeneration).toBe("2"));
      client.emitNotification(holdFrame("2"));
      await waitFor(() => expect(heldSequence(activity.store)).toBe(8));

      // The superseded initial attempt answers with the departed generation.
      initial.resolve(read("1"));
      await opening;

      expect(activity.store.bootGeneration).toBe("2");
      expect(heldSequence(activity.store)).toBe(8);
    } finally {
      stop();
      activity.release();
    }
  });

  // A latest-window read is superseded within its own map by the next begin,
  // which clears its base model — but a WATCHED read and a TRACKED read keep
  // separate pending entries, so a watched attempt can still hold its base
  // model after a tracked read has published over it. Its rejection's
  // `daemonless` attests the absence the watcher's own daemon was issued
  // against, not the ref's current source, and retiring every hold on it
  // clears a live replacement's wait.
  test("a stale daemonless rejection from a superseded watched read does not clear the replacement's hold", async () => {
    const staleWatched = deferred<ThreadReadResponse>();
    let readsServed = 0;
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => {
        readsServed += 1;
        if (readsServed === 1) return Promise.resolve(read("1"));
        // The rich re-read of the same ref is the attempt that answers late.
        if (readsServed === 2) return staleWatched.promise;
        return Promise.resolve(read("2"));
      });
      fake.on("evener/thread/delegates/list", () => delegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, REF);
    const stop = activity.store.observe("delegates");
    try {
      await threadsStore.getState().watchThread(REF);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));
      // A rich watched re-read inherits the watched model's generation 1 as
      // its base model, and stays pending while the tracked read below runs.
      const upgrading = threadsStore.getState().watchThread(REF, { includeTurns: true });
      await waitFor(() => expect(readsServed).toBe(2));

      // A pane opens the ref and reads the daemon that replaced generation 1.
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(activity.store.bootGeneration).toBe("2"));
      client.emitNotification(holdFrame("2"));
      await waitFor(() => expect(heldSequence(activity.store)).toBe(8));

      // The superseded watched attempt answers with attested absence for the
      // daemon it was issued against.
      staleWatched.reject(
        new WireError("thread history failed", -32000, {
          evenerErrorInfo: "transcriptHistoryFailed",
          bootGeneration: DAEMONLESS_BOOT_GENERATION,
        }),
      );
      await upgrading.catch(() => undefined);

      expect(activity.store.bootGeneration).toBe("2");
      expect(heldSequence(activity.store)).toBe(8);
    } finally {
      stop();
      activity.release();
    }
  });
});
