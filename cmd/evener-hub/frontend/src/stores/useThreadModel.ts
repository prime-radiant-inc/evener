// useThreadModel hydrates a session's thread model while the caller is
// mounted: ensure on mount (retrying once the connection is ready), release
// on unmount. Shared by the sessionTasks pane host and the activity sidebar's
// Tasks tab, which pay the same subscription for the same list in two slots.

import type { ThreadModel } from "@evener/appwire-client";
import { useEffect } from "react";
import { connectionStore } from "./connection";
import { threadsStore, useThreadsStore } from "./threads";

// useThreadClaim holds one ensureThread(ref) claim while the caller is
// mounted, starting it once the connection is ready, and releases it on
// unmount. The threads store owns hydration, retries and re-subscription from
// there; the claim, not this effect, is what the store converges on.
export function useThreadClaim(ref: string): void {
  useEffect(() => {
    let started = false;
    let rejected = false;
    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
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
        });
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      if (!rejected) threadsStore.getState().releaseThread(ref);
    };
  }, [ref]);
}

export function useThreadModel(ref: string): ThreadModel | undefined {
  useThreadClaim(ref);
  return useThreadsStore((state) => state.threads.get(ref));
}
