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
        .catch(() => {});
      if (readView) unregister = registerConversationClaim(ref, readView);
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      unregister?.();
      if (started) threadsStore.getState().releaseThread(ref);
    };
  }, [ref, readView]);
}

export function useThreadModel(ref: string): ThreadModel | undefined {
  useThreadClaim(ref);
  return useThreadsStore((state) => state.threads.get(ref));
}
