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

/** A one-shot foreground claim for a destination this tab has no tracked pane
 * model for - the recovery Send's destination hydration. A destination that
 * exists only as a watched stream model is still claimed: routing and
 * dispatch read the tracked map, so a watched-only destination needs the
 * foreground model for the resend's duration (the watch's own lean lifecycle
 * is refcounted separately and is untouched). The lease exists only once
 * ensureThread RESOLVES: a rejected acquisition has already rolled its own
 * claim back inside ensureThread, so a caller that released anyway would
 * consume another foreground reader's claim and retire its subscription or
 * hydration lifecycle. Resolving null means no claim was needed and there is
 * nothing to release. */
export async function acquireUnreadThreadClaim(ref: string): Promise<ThreadClaimLease | null> {
  const state = threadsStore.getState();
  if (state.threads.get(ref) !== undefined) return null;
  await state.ensureThread(ref);
  let released = false;
  return {
    release() {
      if (released) return;
      released = true;
      threadsStore.getState().releaseThread(ref);
    },
  };
}

export function useThreadModel(ref: string): ThreadModel | undefined {
  useThreadClaim(ref);
  return useThreadsStore((state) => state.threads.get(ref));
}
