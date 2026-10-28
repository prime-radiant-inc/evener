// Selects the transcript and retains older-page demand across failures and
// inactive panes. SessionPane owns ensureThread/releaseThread; this hook's
// active readers own only the paging schedule.

import type { ThreadModel } from "@evener/appwire-client";
import { sessionActionError } from "@evener/appwire-client";
import { useCallback, useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import { useConnectionStore } from "../../../stores/connection";
import { olderTranscriptPageKey } from "../../../stores/entryTranscriptRoutes";
import { useThreadsStore } from "../../../stores/threads";
import type { TranscriptReadView } from "./transcriptReadView";

export interface UseTranscriptResult {
  model: ThreadModel | undefined;
  // Capture the scheduling key with loadingOlder so consumers cannot combine
  // an older idle snapshot with a cursor advanced by an unfinished page.
  olderPageKey: string | null | undefined;
  hasOlder: boolean;
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

import {
  createPaging,
  forgetConsumer,
  matchesBinding,
  pagingByRef,
  rememberConsumer,
  resetTranscriptPagingForTests,
  type TranscriptPaging,
  type TranscriptReader,
} from "./transcriptPaging";
import { adoptRemovedConsumers, cancelRemovedConsumers } from "./workspaceTranscriptMembership";

export type { TranscriptReader } from "./transcriptPaging";
export { resetTranscriptPagingForTests };

export function useTranscript(ref: string, reader: string | TranscriptReadView | null = ref): UseTranscriptResult {
  return useTranscriptInternal(ref, reader);
}

export function useTranscriptReader(ref: string, reader: TranscriptReader): UseTranscriptResult {
  return useTranscriptInternal(ref, reader);
}

function useTranscriptInternal(
  ref: string,
  reader: string | TranscriptReadView | TranscriptReader | null,
): UseTranscriptResult {
  const typedReader = typeof reader === "object" && reader !== null && "membership" in reader ? reader : undefined;
  const standalone = typedReader?.membership === "standalone" ? typedReader : undefined;
  const view =
    typeof reader === "object" && reader !== null && !typedReader ? (reader as TranscriptReadView) : undefined;
  const viewId = typeof reader === "string" ? reader : (reader?.id ?? ref);
  const model = useThreadsStore((s) => s.threads.get(ref));
  const olderPageKey = useThreadsStore(() => olderTranscriptPageKey(ref));
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
    if (!standalone) adoptRemovedConsumers(selected, viewId, view);
    if (selected !== entry) setEntry(selected);
  }, [entry, ref, binding, viewId, view, standalone]);
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
      if (document.visibilityState === "hidden" || (view && !view.readable) || (typedReader && !typedReader.readable)) {
        release?.();
        release = undefined;
      } else if (!release) release = paging.activate(viewId);
    };
    sync();
    const unsubscribe = (typedReader ?? view)?.subscribe(sync);
    document.addEventListener("visibilitychange", sync);
    return () => {
      document.removeEventListener("visibilitychange", sync);
      unsubscribe?.();
      release?.();
    };
  }, [paging, ready, ref, entry, viewId, view, typedReader]);

  useEffect(() => {
    if (!standalone) return;
    return () => {
      const selected = pagingByRef.get(ref);
      if (!selected) return;
      selected.paging.cancel(viewId);
      forgetConsumer(selected, viewId);
    };
  }, [standalone, ref, viewId]);

  // A layout commit can adopt another pane's owner before the state update
  // renders. Automatic effects must already route to the committed ref.
  const committedEntry = useCallback(() => pagingByRef.get(ref) ?? entry, [ref, entry]);
  const loadOlder = useCallback(
    () =>
      reader === null || view?.alive === false || typedReader?.alive === false
        ? Promise.resolve()
        : rememberConsumer(committedEntry(), viewId, view, standalone).request(viewId),
    [committedEntry, viewId, view, reader, standalone, typedReader],
  );
  const cancelOlder = useCallback(() => {
    const selected = committedEntry();
    selected.paging.cancel(viewId);
    forgetConsumer(selected, viewId);
    if (!standalone) cancelRemovedConsumers(selected, ref);
  }, [committedEntry, viewId, ref, standalone]);
  return {
    model,
    olderPageKey,
    hasOlder: olderPageKey != null,
    loadOlder,
    cancelOlder,
    loadingOlder: state.loading || (state.pending && state.error === null),
    loadOlderReportingError: () => {
      if (reader === null || view?.alive === false || typedReader?.alive === false) return;
      const owner = rememberConsumer(committedEntry(), viewId, view, standalone);
      // Geometry can repeat quiet demand; only the error row's explicit Retry
      // should bypass pacing after a rejected read.
      const request = owner.getSnapshot().error === null ? owner.request : owner.retryNow;
      void request(viewId).catch(() => {});
    },
    olderError: state.error === null ? null : sessionActionError("Couldn't load older turns", state.error),
  };
}
