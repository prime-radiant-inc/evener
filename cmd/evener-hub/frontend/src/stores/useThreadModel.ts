// useThreadModel hydrates a session's thread model while the caller is
// mounted: ensure on mount (retrying once the connection is ready), release
// on unmount. Shared by the sessionTasks pane host and the activity sidebar's
// Tasks tab, which pay the same subscription for the same list in two slots.

import type { ThreadModel } from "@evener/appwire-client";
import { useEffect } from "react";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { connectionStore } from "./connection";
import { hasConversationHandoff, registerConversationClaim } from "./conversationClaimHandoff";
import { threadsStore, useThreadsStore } from "./threads";

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

/** A one-shot foreground claim on the recovery Send's destination, held for
 * the resend's duration. Every destination is claimed, including one another
 * pane already tracks: a destination that exists only as a watched stream
 * model needs the foreground model for the resend (routing and dispatch read
 * the tracked map, and the watch's own lean lifecycle is refcounted
 * separately and is untouched), and a tracked one needs the hold because the
 * pane that tracks it may unmount mid-resend - which retires the model the
 * enqueued row's dispatch reads, parking that row until someone reopens the
 * destination. ensureThread/releaseThread count foreground holds, so the
 * pane's own release still retires the model only when the resend has
 * released its count too. Checking the tracked map before acquiring was the
 * defect: the check and the acquisition were separable by an unmount. The
 * lease exists only once ensureThread RESOLVES: a rejected acquisition has
 * already rolled its own claim back inside ensureThread, so a caller that
 * released anyway would consume another foreground reader's claim and retire
 * its subscription or hydration lifecycle. There is no "no claim needed"
 * answer: a resolved acquisition always holds exactly one count.
 *
 * The hold is routing-only. A resend routes on the destination's identity,
 * status and capabilities, not its transcript bodies, so its hold keeps the
 * model alive without becoming the body owner a pane open defers to and
 * without waiting on a restoration read. A pane that opens later still finds
 * no body owner and restores the bodies itself; a cached shell is unverified
 * and falls through to the ordinary hydration, so the resend never routes on
 * unverified state. */
export async function acquireUnreadThreadClaim(ref: string): Promise<ThreadClaimLease> {
  await threadsStore.getState().ensureThread(ref, { routingOnly: true });
  let released = false;
  return {
    release() {
      if (released) return;
      released = true;
      threadsStore.getState().releaseThread(ref, { routingOnly: true });
    },
  };
}

export function useThreadModel(ref: string): ThreadModel | undefined {
  useThreadClaim(ref);
  return useThreadsStore((state) => state.threads.get(ref));
}
