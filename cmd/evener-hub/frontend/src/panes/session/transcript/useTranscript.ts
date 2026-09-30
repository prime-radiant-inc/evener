// Selects the transcript and retains older-page demand across failures and
// inactive panes. SessionPane owns ensureThread/releaseThread; this hook's
// active readers own only the paging schedule.

import type { ThreadModel } from "@evener/appwire-client";
import { HistoryPaging, sessionActionError } from "@evener/appwire-client";
import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import { useConnectionStore } from "../../../stores/connection";
import { threadsStore, useThreadsStore } from "../../../stores/threads";

export interface UseTranscriptResult {
  model: ThreadModel | undefined;
  loadOlder(): Promise<void>; // thread/turns/list via olderCursor -> prependOlderTurns
  loadingOlder: boolean;
  cancelOlder(): void;
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

// A browser page serves one hub. Socket replacements retain demand for the
// same ref and session binding; a new binding owns a separate history window.
const pagingByRef = new Map<string, { binding: string | undefined; paging: HistoryPaging }>();

// Tests reset the thread store between hub fixtures.
export function resetTranscriptPagingForTests(): void {
  pagingByRef.clear();
}

export function useTranscript(ref: string, viewId = ref): UseTranscriptResult {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const { state: connection } = useConnectionStore();
  const binding = model?.instanceId ?? model?.threadId;
  const paging = useMemo(() => {
    const retained = pagingByRef.get(ref);
    if (retained && (binding === undefined || retained.binding === undefined || retained.binding === binding)) {
      if (binding !== undefined) retained.binding = binding;
      return retained.paging;
    }
    const entry = {
      binding,
      paging: new HistoryPaging(
        () => {
          const current = threadsStore.getState().threads.get(ref);
          if (current === undefined || (current.instanceId ?? current.threadId) !== entry.binding) return undefined;
          return current.olderCursor ?? null;
        },
        () => threadsStore.getState().loadOlderTurns(ref),
        () => {
          if (pagingByRef.get(ref) === entry) pagingByRef.delete(ref);
        },
      ),
    };
    pagingByRef.set(ref, entry);
    return entry.paging;
  }, [ref, binding]);
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

  const loadOlder = useCallback(() => paging.request(viewId), [paging, viewId]);
  const cancelOlder = useCallback(() => paging.cancel(viewId), [paging, viewId]);
  return {
    model,
    loadOlder,
    cancelOlder,
    loadingOlder: state.loading,
    loadOlderReportingError: () => {
      void paging.retryNow(viewId).catch(() => {});
    },
    olderError: state.error === null ? null : sessionActionError("Couldn't load older turns", state.error),
  };
}
