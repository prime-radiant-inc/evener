// Selects the transcript and retains older-page demand across failures and
// inactive panes. SessionPane owns ensureThread/releaseThread; this hook's
// active readers own only the paging schedule.

import type { ThreadModel } from "@evener/appwire-client";
import { HistoryPaging, sessionActionError } from "@evener/appwire-client";
import { useCallback, useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import { workspaceStore } from "../../../shell/workspace";
import { useConnectionStore } from "../../../stores/connection";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import type { TranscriptReadView } from "./transcriptReadView";

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
  paneConsumers: Map<string, ConsumerMembership>;
}
interface ConsumerMembership {
  paneId: string;
  type?: "session" | "transcript";
  view?: TranscriptReadView;
  unsubscribe?: () => void;
}
const pagingByRef = new Map<string, TranscriptPaging>();

// Tests reset the thread store between hub fixtures.
export function resetTranscriptPagingForTests(): void {
  for (const entry of pagingByRef.values()) {
    for (const id of entry.paneConsumers.keys()) {
      entry.paging.cancel(id);
      forgetConsumer(entry, id);
    }
  }
  pagingByRef.clear();
}

function matchesBinding(entry: TranscriptPaging | undefined, binding: string | undefined): entry is TranscriptPaging {
  return entry !== undefined && (binding === undefined || entry.binding === undefined || entry.binding === binding);
}

function createPaging(ref: string, binding: string | undefined): TranscriptPaging {
  const entry: TranscriptPaging = {
    ref,
    binding,
    paneConsumers: new Map(),
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

function forgetConsumer(entry: TranscriptPaging, id: string): void {
  entry.paneConsumers.get(id)?.unsubscribe?.();
  entry.paneConsumers.delete(id);
}

function rememberConsumer(entry: TranscriptPaging, viewId: string, view?: TranscriptReadView): HistoryPaging {
  // Unresolved reads and permanent failures both retain consumers. Only a
  // successful settlement or explicit cancellation can retire their ownership.
  const state = entry.paging.getSnapshot();
  if (!state.pending && state.error === null) {
    for (const id of entry.paneConsumers.keys()) forgetConsumer(entry, id);
  }
  if (entry.paneConsumers.get(viewId)?.view !== view || !entry.paneConsumers.has(viewId)) {
    forgetConsumer(entry, viewId);
    const paneId = view?.paneId ?? viewId;
    const pane = workspaceStore.getState().panes.find((pane) => pane.id === paneId);
    const membership: ConsumerMembership = {
      paneId,
      type: pane?.type === "session" || pane?.type === "transcript" ? pane.type : undefined,
      view,
    };
    if (view) {
      membership.unsubscribe = view.subscribe(() => {
        if (view.alive || view.role !== "cascade") return;
        entry.paging.cancel(viewId);
        forgetConsumer(entry, viewId);
      });
    }
    entry.paneConsumers.set(viewId, membership);
  }
  return entry.paging;
}

// Ordinary navigation retains its removed predecessor's browse intent. Adopt
// only that intent, never an independently open or collapsed cascade reader's.
function adoptRemovedConsumers(entry: TranscriptPaging, viewId: string, view?: TranscriptReadView): void {
  if (view?.role === "cascade") return;
  const panes = workspaceStore.getState().panes;
  const paneId = view?.paneId ?? viewId;
  const current = panes.find((pane) => pane.id === paneId && (pane.params as { ref?: unknown })?.ref === entry.ref);
  if (!current || (current.type !== "session" && current.type !== "transcript")) return;
  for (const [id, membership] of entry.paneConsumers) {
    if (id === viewId || membership.view?.alive || membership.view?.role === "cascade" || membership.type === undefined)
      continue;
    const stillOpen = panes.some(
      (pane) =>
        pane.id === membership.paneId &&
        pane.type === membership.type &&
        (pane.params as { ref?: unknown })?.ref === entry.ref,
    );
    if (stillOpen && (membership.view?.alive !== false || membership.paneId !== paneId)) continue;
    const paging = rememberConsumer(entry, viewId, view);
    void paging.request(viewId).catch(() => {});
    paging.cancel(id);
    forgetConsumer(entry, id);
  }
}

export function useTranscript(ref: string, reader: string | TranscriptReadView | null = ref): UseTranscriptResult {
  const view = typeof reader === "string" ? undefined : (reader ?? undefined);
  const viewId = typeof reader === "string" ? reader : (reader?.id ?? ref);
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
    if (retained && retained !== selected) {
      for (const id of retained.paneConsumers.keys()) {
        retained.paging.cancel(id);
        forgetConsumer(retained, id);
      }
    }
    if (binding !== undefined) selected.binding = binding;
    pagingByRef.set(ref, selected);
    adoptRemovedConsumers(selected, viewId, view);
    if (selected !== entry) setEntry(selected);
  }, [entry, ref, binding, viewId, view]);
  const paging = entry.paging;
  const state = useSyncExternalStore(paging.subscribe, paging.getSnapshot);
  // Wait for hydration when a pane returns before resuming its demand.
  const ready = reader !== null && model !== undefined && connection === "ready";
  useLayoutEffect(() => {
    view?.setReadable(true);
    return () => view?.setReadable(false);
  }, [view]);
  useEffect(() => {
    if (!ready || pagingByRef.get(ref) !== entry) return;
    let release: (() => void) | undefined;
    const sync = () => {
      if (document.visibilityState === "hidden" || (view && !view.readable)) {
        release?.();
        release = undefined;
      } else if (!release) release = paging.activate(viewId);
    };
    sync();
    const unsubscribe = view?.subscribe(sync);
    document.addEventListener("visibilitychange", sync);
    return () => {
      document.removeEventListener("visibilitychange", sync);
      unsubscribe?.();
      release?.();
    };
  }, [paging, ready, ref, entry, viewId, view]);

  // A layout commit can adopt another pane's owner before the state update
  // renders. Automatic effects must already route to the committed ref.
  const committedEntry = useCallback(() => pagingByRef.get(ref) ?? entry, [ref, entry]);
  const loadOlder = useCallback(
    () =>
      reader === null || view?.alive === false
        ? Promise.resolve()
        : rememberConsumer(committedEntry(), viewId, view).request(viewId),
    [committedEntry, viewId, view, reader],
  );
  const cancelOlder = useCallback(() => {
    const selected = committedEntry();
    selected.paging.cancel(viewId);
    forgetConsumer(selected, viewId);
    // Navigation retains demand but creates a fresh pane ID on return. Jump
    // to live abandons those removed predecessors, while an independently
    // open pane keeps its demand even when its reader is currently inactive.
    const panes = workspaceStore.getState().panes;
    for (const [consumer, membership] of selected.paneConsumers) {
      if (membership.view?.alive) continue;
      if (membership.type === undefined) continue;
      if (
        panes.some(
          (pane) =>
            pane.id === membership.paneId &&
            pane.type === membership.type &&
            (pane.params as { ref?: unknown })?.ref === ref,
        )
      )
        continue;
      selected.paging.cancel(consumer);
      forgetConsumer(selected, consumer);
    }
  }, [committedEntry, viewId, ref]);
  return {
    model,
    loadOlder,
    cancelOlder,
    loadingOlder: state.loading || (state.pending && state.error === null),
    loadOlderReportingError: () => {
      if (reader === null || view?.alive === false) return;
      const owner = rememberConsumer(committedEntry(), viewId, view);
      // Geometry can repeat quiet demand; only the error row's explicit Retry
      // should bypass pacing after a rejected read.
      const request = owner.getSnapshot().error === null ? owner.request : owner.retryNow;
      void request(viewId).catch(() => {});
    },
    olderError: state.error === null ? null : sessionActionError("Couldn't load older turns", state.error),
  };
}
