// Selects the transcript and retains older-page demand across failures and
// inactive panes. SessionPane owns ensureThread/releaseThread; this hook's
// active readers own only the paging schedule.

import type { AppwireClientLike, ThreadModel } from "@evener/appwire-client";
import { HistoryPaging, sessionActionError } from "@evener/appwire-client";
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { useConnectionStore } from "../../../stores/connection";
import { threadsStore, useThreadsStore } from "../../../stores/threads";

export interface UseTranscriptResult {
  model: ThreadModel | undefined;
  loadOlder(): Promise<void>; // thread/turns/list via olderCursor -> prependOlderTurns
  loadingOlder: boolean;
  // The fire-and-forget form paging affordances use: same fetch, but the
  // rejection lands in olderError instead of propagating. Both transcript
  // surfaces (the live session pane and the read-only transcript pane) need
  // exactly this, so it lives here rather than being written twice.
  loadOlderReportingError(): void;
  // The last failed older-page fetch's finished sentence, or null when the
  // last attempt succeeded or none has been made. Cleared at the start of
  // every attempt, so a retry begins from a clean state. Retried automatically
  // while active; the row adds no label of its own.
  olderError: string | null;
}

// Dock panes unmount while inactive. Keep only unsatisfied demand across that
// lifecycle; successful owners are released with their last reader.
const pagingByClient = new WeakMap<AppwireClientLike, Map<string, HistoryPaging>>();

export function useTranscript(ref: string): UseTranscriptResult {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const { client, state: connection } = useConnectionStore();
  const paging = useMemo(() => {
    let pagingByRef = client === null ? undefined : pagingByClient.get(client);
    if (pagingByRef === undefined) {
      pagingByRef = new Map<string, HistoryPaging>();
      if (client !== null) pagingByClient.set(client, pagingByRef);
    }
    const owners = pagingByRef;
    const retained = owners.get(ref);
    if (retained) return retained;
    const owner = new HistoryPaging(
      () => {
        const current = threadsStore.getState().threads.get(ref);
        return current === undefined ? undefined : (current.olderCursor ?? null);
      },
      () => threadsStore.getState().loadOlderTurns(ref),
      () => {
        if (owners.get(ref) === owner) owners.delete(ref);
      },
    );
    owners.set(ref, owner);
    return owner;
  }, [ref, client]);
  const state = useSyncExternalStore(paging.subscribe, paging.getSnapshot);
  // Wait for hydration when a pane returns before resuming its demand.
  const ready = model !== undefined && connection === "ready";
  useEffect(() => {
    if (!ready) return;
    let release: (() => void) | undefined;
    const sync = () => {
      if (document.visibilityState === "hidden") {
        release?.();
        release = undefined;
      } else if (!release) release = paging.activate();
    };
    sync();
    document.addEventListener("visibilitychange", sync);
    return () => {
      document.removeEventListener("visibilitychange", sync);
      release?.();
    };
  }, [paging, ready]);

  return {
    model,
    loadOlder: paging.request,
    loadingOlder: state.loading,
    loadOlderReportingError: () => {
      void paging.retryNow().catch(() => {});
    },
    olderError: state.error === null ? null : sessionActionError("Couldn't load older turns", state.error),
  };
}
