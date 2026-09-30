// Selects the transcript and retains older-page demand across failures and
// inactive panes. SessionPane owns ensureThread/releaseThread; this hook's
// active readers own only the paging schedule.

import type { ThreadModel } from "@evener/appwire-client";
import { HistoryPaging, sessionActionError } from "@evener/appwire-client";
import { useCallback, useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import { workspaceStore } from "../../../shell/workspace";
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
interface TranscriptPaging {
  ref: string;
  binding: string | undefined;
  paging: HistoryPaging;
  consumers: Map<string, "session" | "transcript">;
}
const pagingByRef = new Map<string, TranscriptPaging>();

// Tests reset the thread store between hub fixtures.
export function resetTranscriptPagingForTests(): void {
  pagingByRef.clear();
}

function matchesBinding(entry: TranscriptPaging | undefined, binding: string | undefined): entry is TranscriptPaging {
  return entry !== undefined && (binding === undefined || entry.binding === undefined || entry.binding === binding);
}

function createPaging(ref: string, binding: string | undefined): TranscriptPaging {
  const entry: TranscriptPaging = {
    ref,
    binding,
    consumers: new Map(),
    paging: new HistoryPaging(
      () => {
        const current = threadsStore.getState().threads.get(ref);
        if (current === undefined || (current.instanceId ?? current.threadId) !== entry.binding) return undefined;
        // The loaded model's optional wire cursor is absent at history end.
        // An unavailable model or binding is handled above, not as an end.
        return current.olderCursor ?? null;
      },
      () => threadsStore.getState().loadOlderTurns(ref),
      () => {
        if (pagingByRef.get(ref) === entry) pagingByRef.delete(ref);
      },
    ),
  };
  return entry;
}

function rememberConsumer(entry: TranscriptPaging, viewId: string): HistoryPaging {
  // A fulfilled read may still have pending demand when its cursor did not
  // advance. Only settled demand can discard its retained pane ownership.
  if (!entry.paging.getSnapshot().pending) entry.consumers.clear();
  const pane = workspaceStore.getState().panes.find((pane) => pane.id === viewId);
  if (pane && (pane.type === "session" || pane.type === "transcript")) entry.consumers.set(viewId, pane.type);
  return entry.paging;
}

export function useTranscript(ref: string, viewId = ref): UseTranscriptResult {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const { state: connection } = useConnectionStore();
  const binding = model?.instanceId ?? model?.threadId;
  const [entry, setEntry] = useState(() => {
    const retained = pagingByRef.get(ref);
    return matchesBinding(retained, binding) ? retained : createPaging(ref, binding);
  });
  // Only committed readers can install or bind shared demand. A suspended
  // first render must not retire a waiting reader's unknown binding.
  useLayoutEffect(() => {
    const retained = pagingByRef.get(ref);
    let selected: TranscriptPaging;
    if (matchesBinding(retained, binding)) selected = retained;
    else selected = entry.ref === ref && matchesBinding(entry, binding) ? entry : createPaging(ref, binding);
    if (binding !== undefined) selected.binding = binding;
    pagingByRef.set(ref, selected);
    if (selected !== entry) setEntry(selected);
  }, [entry, ref, binding]);
  const paging = entry.paging;
  const state = useSyncExternalStore(paging.subscribe, paging.getSnapshot);
  // Wait for hydration when a pane returns before resuming its demand.
  const ready = model !== undefined && connection === "ready";
  useEffect(() => {
    if (!ready || pagingByRef.get(ref) !== entry) return;
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
  }, [paging, ready, ref, entry]);

  // A layout commit can adopt another pane's owner before the state update
  // renders. Automatic effects must already route to the committed ref.
  const committedEntry = useCallback(() => pagingByRef.get(ref) ?? entry, [ref, entry]);
  const loadOlder = useCallback(
    () => rememberConsumer(committedEntry(), viewId).request(viewId),
    [committedEntry, viewId],
  );
  const cancelOlder = useCallback(() => {
    const selected = committedEntry();
    selected.paging.cancel(viewId);
    selected.consumers.delete(viewId);
    // Navigation retains demand but creates a fresh pane ID on return. Jump
    // to live abandons those removed predecessors, while an independently
    // open pane keeps its demand even when its reader is currently inactive.
    const panes = workspaceStore.getState().panes;
    for (const [consumer, type] of selected.consumers) {
      if (
        panes.some(
          (pane) => pane.id === consumer && pane.type === type && (pane.params as { ref?: unknown })?.ref === ref,
        )
      )
        continue;
      selected.paging.cancel(consumer);
      selected.consumers.delete(consumer);
    }
  }, [committedEntry, viewId, ref]);
  return {
    model,
    loadOlder,
    cancelOlder,
    loadingOlder: state.loading || (state.pending && state.error === null),
    loadOlderReportingError: () => {
      const owner = rememberConsumer(committedEntry(), viewId);
      // Geometry can repeat quiet demand; only the error row's explicit Retry
      // should bypass pacing after a rejected read.
      const request = owner.getSnapshot().error === null ? owner.request : owner.retryNow;
      void request(viewId).catch(() => {});
    },
    olderError: state.error === null ? null : sessionActionError("Couldn't load older turns", state.error),
  };
}
