// useThreadModel hydrates a session's thread model while the caller is
// mounted: ensure on mount (retrying once the connection is ready), release
// on unmount. Shared by the sessionTasks pane host and the activity sidebar's
// Tasks tab, which pay the same subscription for the same list in two slots.

import type { ThreadModel } from "@evener/appwire-client";
import { useEffect } from "react";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { connectionStore } from "./connection";
import { hasConversationHandoff, registerConversationClaim } from "./conversationClaimHandoff";
import type { ForegroundHold } from "./threadOwners";
import { acquireBackgroundThread, threadRoutingVerified, threadsStore, useThreadsStore } from "./threads";

// useThreadClaim holds one ensureThread(ref) claim while the caller is
// mounted, starting it once the connection is ready, and releases it on
// unmount. The threads store owns hydration, retries and re-subscription from
// there; the claim, not this effect, is what the store converges on.
export function useThreadClaim(ref: string, readView?: TranscriptReadView): void {
  useEffect(() => {
    let started = false;
    let rejected = false;
    // This mount's own foreground hold, handed over the moment the acquisition
    // takes it. Unmount releases exactly this handle: two panes can hold one ref
    // at the same time, and a ref-keyed release names whichever hold is newest
    // by then - so the older pane's unmount would spend the newer pane's hold,
    // and if that pane is still opening its `claimStanding()` check ends the
    // acquisition while the closed pane's handle is the one left counting. The
    // handle names one hold, so each pane's exit drops its own.
    let hold: ForegroundHold | undefined;
    let unregister: (() => void) | undefined;
    const tryStart = () => {
      if (started) return;
      // A retained, started claim survives connection loss. A never-started
      // reader still waits for readiness, exactly like unbound callers.
      if (connectionStore.getState().state !== "ready" && !(readView && hasConversationHandoff(ref, readView))) return;
      started = true;
      threadsStore
        .getState()
        .ensureThread(ref, {
          onHold: (taken) => {
            hold = taken;
          },
        })
        .catch(() => {
          // `started` marks the call, not the claim: a rejected ensureThread
          // rolled its own claim back, so releasing on unmount would consume
          // another reader's ref-keyed hold and retire its lifecycle. A
          // pending or settled claim still belongs to this mount.
          rejected = true;
          unregister?.();
        });
      if (readView) unregister = registerConversationClaim(ref, readView);
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      unregister?.();
      // started AND not rejected: a reader that never called ensureThread
      // (mounted during reconnect, unmounted before readiness) owns nothing,
      // and a rejected call rolled its own claim back.
      if (started && !rejected) {
        if (hold) threadsStore.getState().releaseThreadHold(hold);
      }
    };
  }, [ref, readView]);
}

export interface ThreadClaimLease {
  release(): void;
}

/** Why a resend's destination claim was refused. Both mean "do not write at
 * this ref": the hub has confirmed it gone, or its routing state never
 * verified. */
const ThreadClaimRefusal = {
  deleted: "The destination was deleted before its routing state was verified",
  unverified: "The destination was not verified for routing",
} as const;

/** The recovery Send's destination, held for the resend's duration. The claim
 * is the store's own background ownership - the same counted lease a
 * background download keeps a model and its subscription under - reused here
 * because it is exactly this lifecycle's shape: it keeps the destination
 * reachable for dispatch without opening a pane's transcript bodies, and it
 * counts, so the pane that also holds the destination can unmount mid-resend
 * without retiring the model the enqueued row's dispatch reads.
 *
 * The lease's `ready` is the whole verification: it settles once the
 * destination's routing state is verified on a live connection, joining the
 * tracked read the store already owes the ref and waiting on the owned retry
 * lifecycle for the recovery a hub authorizes (a rebuilding index, a socket
 * loss). A destination the hub has confirmed deleted, or one whose read
 * lifecycle ended without verifying it, is not handed out at all: the lease is
 * released before rejecting, so a refused resend leaves no hold behind. */
export async function acquireUnreadThreadClaim(ref: string): Promise<ThreadClaimLease> {
  const lease = acquireBackgroundThread(ref);
  const release = (): void => {
    void lease.release();
  };
  const missing = (): boolean => threadsStore.getState().deletedRefs.has(ref);
  try {
    // The lease keeps the destination's model and subscription alive for the
    // whole verification, including across a reconnect: releasing it on a
    // socket loss would retire the model the replacement connection's read is
    // about to publish into, and refuse a resend the hub never answered.
    await lease.ready;
    if (missing()) throw new Error(ThreadClaimRefusal.deleted);
    if (!threadRoutingVerified(ref)) throw new Error(ThreadClaimRefusal.unverified);
  } catch (error) {
    release();
    throw error;
  }
  return {
    release,
  };
}

export function useThreadModel(ref: string): ThreadModel | undefined {
  useThreadClaim(ref);
  return useThreadsStore((state) => state.threads.get(ref));
}
