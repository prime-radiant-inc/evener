// useThreadModel hydrates a session's thread model while the caller is
// mounted: ensure on mount (retrying once the connection is ready), release
// on unmount. Shared by the sessionTasks pane host and the activity sidebar's
// Tasks tab, which pay the same subscription for the same list in two slots.

import type { ThreadModel } from "@evener/appwire-client";
import { useEffect } from "react";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { connectionStore } from "./connection";
import { hasConversationHandoff, registerConversationClaim } from "./conversationClaimHandoff";
import { currentStoreGeneration, threadsStore, useThreadsStore } from "./threads";

// useThreadClaim holds one ensureThread(ref) claim while the caller is
// mounted, starting it once the connection is ready, and releases it on
// unmount. The threads store owns hydration, retries and re-subscription from
// there; the claim, not this effect, is what the store converges on.
export function useThreadClaim(ref: string, readView?: TranscriptReadView): void {
  useEffect(() => {
    let started = false;
    let rejected = false;
    let unregister: (() => void) | undefined;
    const tryStart = () => {
      if (started) return;
      // A retained, started claim survives connection loss. A never-started
      // reader still waits for readiness, exactly like unbound callers.
      if (connectionStore.getState().state !== "ready" && !(readView && hasConversationHandoff(ref, readView))) return;
      started = true;
      threadsStore
        .getState()
        .ensureThread(ref)
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
      if (started && !rejected) threadsStore.getState().releaseThread(ref);
    };
  }, [ref, readView]);
}

export interface ThreadClaimLease {
  release(): void;
}

/** A refcounted foreground claim for a recovery Send's destination, held for
 * the resend's duration. It always takes the claim, even when a tracked pane
 * already holds the destination: ensureThread adds a hold rather than
 * replacing one, so the model stays pinned until lease.release() and a pane
 * that unmounts mid-resend cannot retire the destination out from under the
 * resend. A destination that exists only as a watched stream model is claimed
 * for the same reason: routing and dispatch read the tracked map, so a
 * watched-only destination needs the foreground model while the resend runs
 * (the watch's own lean lifecycle is refcounted separately and is untouched).
 * The lease exists only once ensureThread RESOLVES: a rejected acquisition has
 * already rolled its own claim back inside ensureThread, so a caller that
 * released anyway would consume another foreground reader's claim and retire
 * its subscription or hydration lifecycle. */
export async function acquireUnreadThreadClaim(ref: string): Promise<ThreadClaimLease> {
  // The claim this lease will release belongs to the store that exists when
  // ensureThread takes it, which is now - before the await. A reset that lands
  // while that await is pending (tests only - see storeGeneration) discards
  // that store, and releasing its hold afterwards would consume a hold the
  // reset's successor owns and retire the model under it (issue #7236).
  const generation = currentStoreGeneration();
  await threadsStore.getState().ensureThread(ref);
  let released = false;
  return {
    release() {
      if (released) return;
      released = true;
      if (generation !== currentStoreGeneration()) return;
      threadsStore.getState().releaseThread(ref);
    },
  };
}

export function useThreadModel(ref: string): ThreadModel | undefined {
  useThreadClaim(ref);
  return useThreadsStore((state) => state.threads.get(ref));
}
