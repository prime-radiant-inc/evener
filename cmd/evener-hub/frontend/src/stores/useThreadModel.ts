// useThreadModel hydrates a session's thread model while the caller is
// mounted: ensure on mount (retrying once the connection is ready), release
// on unmount. Shared by the sessionTasks pane host and the activity sidebar's
// Tasks tab, which pay the same subscription for the same list in two slots.

import type { ThreadModel } from "@evener/appwire-client";
import { useEffect } from "react";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { connectionStore } from "./connection";
import { hasConversationHandoff, registerConversationClaim } from "./conversationClaimHandoff";
import { existingEntryRoute } from "./entryTranscriptRoutes";
import type { ForegroundHold } from "./threadOwners";
import { acquireBackgroundThread, threadsStore, useThreadsStore } from "./threads";

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

// Routable state is what a resend can address a row with: the destination's
// identity, status and capabilities, read from a model whose source read has
// landed. The route's `cachedUnverified` marker is the one real record of
// whether that read has answered - a shell a cached open published advertises
// neither Send nor Queue - and a ref with no production route has no shell to
// wait out. existingEntryRoute, not entryTranscriptRoute: asking must not be
// what creates a route.
function residentRoutable(ref: string): boolean {
  return threadsStore.getState().threads.has(ref) && existingEntryRoute(ref)?.cachedUnverified !== true;
}

/** The recovery Send's destination, held for the resend's duration. The claim
 * is the store's own background ownership - the same counted lease a
 * background download keeps a model and its subscription under - reused here
 * because it is exactly this lifecycle's shape: it keeps the destination
 * reachable for dispatch without opening a pane's transcript bodies, and it
 * counts, so the pane that also holds the destination can unmount mid-resend
 * without retiring the model the enqueued row's dispatch reads.
 *
 * A resident destination whose source read has landed is handed out as it
 * stands: the lease is what keeps it alive, and no read is issued for an
 * answer already known. Anything else - an unopened destination, a cached
 * shell, a read in flight - is verified through the store's bounded tracked
 * refresh, which is the same read a pane would receive. A destination the hub
 * has confirmed deleted, or one that stays unverified, is not handed out at
 * all: the lease is released before rejecting, so a refused resend leaves no
 * hold behind. */
export async function acquireUnreadThreadClaim(ref: string): Promise<ThreadClaimLease> {
  const lease = acquireBackgroundThread(ref);
  const release = (): void => {
    void lease.release();
  };
  const missing = (): boolean => threadsStore.getState().deletedRefs.has(ref);
  try {
    // The lease's own acquisition joins a read already in flight for the ref
    // and otherwise issues one bounded attempt, so an unopened destination is
    // verified there and never read twice.
    await lease.ready;
    if (missing()) throw new Error(ThreadClaimRefusal.deleted);
    // The acquisition's own bounded attempt verified everything it could: an
    // unopened destination published from that read, and a destination the hub
    // refused left nothing to hand out. The one case it could not verify is a
    // resident cached shell - the acquisition returned on it without reading,
    // and its capabilities are the shell's, not the source's - so that shell is
    // verified by one bounded tracked read. A ref with no model at all is not
    // re-read here: its one attempt already ran, and a second read would turn a
    // terminal answer into a retry loop the claim has no recovery surface for.
    if (!residentRoutable(ref) && threadsStore.getState().threads.has(ref)) {
      await threadsStore.getState().refreshThread(ref);
      if (missing()) throw new Error(ThreadClaimRefusal.deleted);
    }
    if (!residentRoutable(ref)) throw new Error(ThreadClaimRefusal.unverified);
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
