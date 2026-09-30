// useThreadModel hydrates a session's thread model while the caller is
// mounted: ensure on mount (retrying once the connection is ready), release
// on unmount. Shared by the sessionTasks pane host and the activity sidebar's
// Tasks tab, which pay the same subscription for the same list in two slots.

import type { ThreadModel } from "@evener/appwire-client";
import { useEffect } from "react";
import { connectionStore } from "./connection";
import { threadsStore, useThreadsStore } from "./threads";

export function useThreadModel(ref: string): ThreadModel | undefined {
  useEffect(() => {
    let started = false;
    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore.getState().ensureThread(ref).catch(() => {});
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      if (started) threadsStore.getState().releaseThread(ref);
    };
  }, [ref]);
  return useThreadsStore((state) => state.threads.get(ref));
}
