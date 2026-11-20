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

  test("an attested daemonless rejection retires the live hold", async () => {
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
          absenceAttested: true,
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

  // A peer too old to attest stamps `daemonless` on every failed saved read,
  // whether or not a daemon still serves the thread (appwire.NormalizeForwardedHistoryError
  // exists to demote it, but the browser must not trust a token a forwarding
  // layer may have passed through). An unchanged hold emits no update of its
  // own, so clearing one on a token that may lie loses a live wait for its whole
  // duration, while keeping one that really ended is corrected by the next
  // successful read.
  test("an unqualified daemonless rejection keeps the live hold", async () => {
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

      expect(activity.store.bootGeneration).toBe("1");
      expect(heldSequence(activity.store)).toBe(7);
      expect(activity.store.getSnapshot().delegates.rows[0]?.runningJobCount).toBe(0);
    } finally {
      stop();
      activity.release();
    }
  });

  // Only `daemonless` is ambiguous. A numeric live-owner stamp is positive
  // evidence of a serving daemon, so a rejection naming one still adopts it --
  // retiring the generation it replaced, exactly as a success naming one does.
  test.each(["2", "3@root"])("a rejection's live-owner stamp %s is still adopted", async (stamp) => {
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

      client.on("thread/read", () => {
        throw new WireError("thread history failed at entry 3", -32000, {
          evenerErrorInfo: "transcriptHistoryFailed",
          bootGeneration: stamp,
        });
      });
      await threadsStore
        .getState()
        .refreshThread(REF)
        .catch(() => undefined);

      await waitFor(() => expect(activity.store.bootGeneration).toBe(stamp));
      // The stamp is evidence of a serving daemon, so it is adopted as a
      // successful read's would be. The row's sample is a page's, which an
      // adoption reconciles rather than wipes: the page is re-served and the
      // wait it reports stands.
      expect(heldSequence(activity.store)).toBe(7);
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

  // The success-side twin of the case above. A watched read and a tracked read
  // keep SEPARATE pending entries, so a watched attempt still holds its base
  // model after a tracked read has published over it -- and a watched read does
  // not retire the tracked one either. When that late watched success names the
  // outgoing daemon, adopting it would move the ref's source back a generation
  // and retire the replacement's hold, which nothing restores: an unchanged hold
  // emits no update of its own.
  test("a late watched success from the outgoing daemon does not re-adopt it or clear the replacement's hold", async () => {
    const lateWatched = deferred<ThreadReadResponse>();
    let readsServed = 0;
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => {
        readsServed += 1;
        if (readsServed === 1) return Promise.resolve(read("1"));
        // The rich watched re-read of the same ref is the attempt that answers
        // late, with the daemon it was issued against.
        if (readsServed === 2) return lateWatched.promise;
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
      // A rich watched re-read inherits the watched model's generation 1 as its
      // base model and stays pending while the tracked read below runs.
      const upgrading = threadsStore.getState().watchThread(REF, { includeTurns: true });
      await waitFor(() => expect(readsServed).toBe(2));

      // A pane opens the ref and reads the daemon that replaced generation 1.
      await threadsStore.getState().ensureThread(REF);
      await waitFor(() => expect(activity.store.bootGeneration).toBe("2"));
      client.emitNotification(holdFrame("2"));
      await waitFor(() => expect(heldSequence(activity.store)).toBe(8));

      // The superseded watched attempt answers with the outgoing daemon.
      lateWatched.resolve(read("1"));
      await upgrading;

      expect(activity.store.bootGeneration).toBe("2");
      expect(heldSequence(activity.store)).toBe(8);
    } finally {
      stop();
      activity.release();
    }
  });

  // The tracked twin of the case above, where the attempt that answers late
  // still owns its own map: neither map's entry proves which read is newest
  // across the two, and only `readSequence` does. A watched rich read issued
  // since the tracked attempt began is a later read of the same ref, so the
  // tracked attempt's older generation may not name the source when it lands.
  test("a tracked read a later watched read superseded does not re-adopt the outgoing daemon", async () => {
    // Its own ref: earlier tests in this file leave hydration retries armed for
    // REF, and one firing mid-test would begin a newer read of REF and retire
    // this attempt's say for a reason the case is not about.
    const otherRef = "remote:owner-2";
    const otherRead = (bootGeneration: string): ThreadReadResponse => {
      const base = read(bootGeneration);
      return { ...base, thread: { ...base.thread, evener: { ...base.thread.evener, ref: otherRef } } };
    };
    const otherDelegates = (): SessionDelegatesResponse => ({
      // Push-eligible, so the replacement's frame merges rather than re-reads.
      context: { ...activityContext(otherRef), reportPreview: true },
      scope: "session",
      delegates: [activityDelegate({ ownerRef: otherRef, rootRef: otherRef, holdSequence: 7, runningJobCount: 0 })],
      page: { complete: true, issues: [] },
    });
    const otherHoldFrame = (bootGeneration: string): AnyNotification => {
      const frame = holdFrame(bootGeneration);
      return {
        ...frame,
        params: { ...(frame.params as { ref: string }), ref: otherRef },
      } as AnyNotification;
    };
    const lateTracked = deferred<ThreadReadResponse>();
    let generation = "1";
    let holdTracked = false;
    let trackedIssued = false;
    const client = connectReadyFakeClient((fake) => {
      fake.on("thread/read", () => {
        if (holdTracked && !trackedIssued) {
          trackedIssued = true;
          return lateTracked.promise;
        }
        return otherRead(generation);
      });
      fake.on("evener/thread/delegates/list", () => otherDelegates());
      fake.on("thread/unsubscribe", () => ({}));
    });
    const activity = acquireSessionActivity(client, otherRef);
    const stop = activity.store.observe("delegates");
    try {
      // The ref is open under generation 1, so the tracked attempt below
      // inherits generation 1's history as its base model.
      await threadsStore.getState().ensureThread(otherRef);
      await waitFor(() => expect(heldSequence(activity.store)).toBe(7));
      await waitFor(() => expect(activity.store.bootGeneration).toBe("1"));

      generation = "2";
      holdTracked = true;
      const refreshing = threadsStore.getState().refreshThread(otherRef);
      await waitFor(() => expect(trackedIssued).toBe(true));

      // A rich watched read of the same ref is issued after it, and it reads the
      // daemon that replaced generation 1. Its own generation is adopted.
      await threadsStore.getState().watchThread(otherRef, { includeTurns: true });
      await waitFor(() => expect(activity.store.bootGeneration).toBe("2"));
      client.emitNotification(otherHoldFrame("2"));
      await waitFor(() => expect(heldSequence(activity.store)).toBe(8));

      // The tracked attempt, still current in ITS map, answers with the daemon
      // it was issued against.
      lateTracked.resolve(otherRead("1"));
      await refreshing;

      expect(activity.store.bootGeneration).toBe("2");
      expect(heldSequence(activity.store)).toBe(8);
    } finally {
      stop();
      activity.release();
    }
  });
});
