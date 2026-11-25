// Selects the transcript and retains older-page demand across failures and
// inactive panes. SessionPane owns ensureThread/releaseThread; this hook's
// active readers own only the paging schedule.

import type { ThreadModel } from "@evener/appwire-client";
import { sessionActionError } from "@evener/appwire-client";
import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
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
  // Whether an older-page read is on the wire right now. Distinct from
  // loadingOlder, which also counts retained demand while a retry is paced or
  // a settled page has not advanced the cursor: a fetching affordance must
  // read this one, or it claims I/O that is not happening.
  olderReadInFlight: boolean;
  cancelOlder(): void;
  // The fire-and-forget form paging affordances use: same fetch, but the
  // rejection lands in olderError instead of propagating. Both transcript
  // surfaces (the live session pane and the read-only transcript pane) need
  // exactly this, so it lives here rather than being written twice.
  loadOlderReportingError(): void;
  // Whether this pane is an active reader right now: its document is visible
  // and its view and reader are readable, the same state paging activation is
  // gated on below. An automatic affordance uses it to hold its re-check until
  // the reader comes back rather than queueing demand nobody can see.
  readerActive: boolean;
  // The last failed older-page fetch's finished sentence, or null when the
  // last attempt succeeded or none has been made. Cleared at the start of
  // every attempt, so a retry begins from a clean state. Retried automatically
  // while active; the row adds no label of its own.
  olderError: string | null;
  // A failure no retry can clear (an upgrade is required, or the session is
  // gone): demand stops, and the explanation in olderError stands.
  olderPermanent: boolean;
  // Find's own older-page demand: a separate consumer of the same paging
  // owner, so closing Find withdraws only what Find asked for and the reader's
  // demand stays. Asking again while a page is in flight or a retry is paced
  // changes nothing.
  findOlder(): void;
  cancelFindOlder(): void;
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

export function useTranscript(
  ref: string,
  reader: string | TranscriptReadView | null = ref,
  ownerDocument: Document = document,
): UseTranscriptResult {
  return useTranscriptInternal(ref, reader, ownerDocument);
}

export function useTranscriptReader(ref: string, reader: TranscriptReader): UseTranscriptResult {
  return useTranscriptInternal(ref, reader, document);
}

function useTranscriptInternal(
  ref: string,
  reader: string | TranscriptReadView | TranscriptReader | null,
  ownerDocument: Document,
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
  const [readerActive, setReaderActive] = useState(false);
  // What the last render published. The activation effect re-runs whenever the
  // owning document changes - a pane moved between windows rebinds without a
  // visibility event in either document - and it must start from that value:
  // seeding the effect's own tracker with "inactive" makes a hidden replacement
  // document agree with it while the pane still reports an active reader.
  const publishedActiveRef = useRef(false);
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
  const findConsumer = `${viewId}:find`;
  useLayoutEffect(() => {
    view?.setReadable(true);
    return () => view?.setReadable(false);
  }, [view]);
  useEffect(() => {
    if (!ready) {
      publishedActiveRef.current = false;
      setReaderActive(false);
      return;
    }
    if (pagingByRef.get(ref) === undefined) {
      // A remount above this hook can let the outgoing reader's store
      // subscription retire the idle entry between the layout install and this
      // effect. Nobody else owns the ref then, so restore this committed entry
      // rather than leaving the reinstalled demand unserved forever.
      if (!matchesBinding(entry, binding)) return;
      pagingByRef.set(ref, entry);
    } else if (pagingByRef.get(ref) !== entry) return;
    let release: (() => void) | undefined;
    let active = publishedActiveRef.current;
    const sync = () => {
      const inactive =
        ownerDocument.visibilityState === "hidden" ||
        (view && !view.readable) ||
        (typedReader && !typedReader.readable);
      if (inactive) {
        release?.();
        release = undefined;
      } else if (!release) {
        const releaseReader = paging.activate(viewId);
        const releaseFind = paging.activate(findConsumer);
        release = () => {
          releaseReader();
          releaseFind();
        };
      }
      const next = !inactive;
      if (next !== active) {
        active = next;
        publishedActiveRef.current = next;
        setReaderActive(next);
      }
    };
    sync();
    const unsubscribe = (typedReader ?? view)?.subscribe(sync);
    ownerDocument.addEventListener("visibilitychange", sync);
    return () => {
      ownerDocument.removeEventListener("visibilitychange", sync);
      unsubscribe?.();
      release?.();
    };
  }, [paging, ready, ref, entry, binding, viewId, findConsumer, view, typedReader, ownerDocument]);

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
  const findOlder = useCallback(() => {
    if (reader === null || view?.alive === false || typedReader?.alive === false) return;
    void committedEntry()
      .paging.request(findConsumer)
      .catch(() => {});
  }, [committedEntry, findConsumer, reader, view, typedReader]);
  const cancelFindOlder = useCallback(() => {
    committedEntry().paging.cancel(findConsumer);
  }, [committedEntry, findConsumer]);
  return {
    model,
    olderPageKey,
    hasOlder: olderPageKey != null,
    readerActive,
    loadOlder,
    cancelOlder,
    loadingOlder: state.loading || (state.pending && state.error === null),
    olderReadInFlight: state.loading,
    loadOlderReportingError: () => {
      if (reader === null || view?.alive === false || typedReader?.alive === false) return;
      const owner = rememberConsumer(committedEntry(), viewId, view, standalone);
      // Geometry can repeat quiet demand; only the error row's explicit Retry
      // should bypass pacing after a rejected read.
      const request = owner.getSnapshot().error === null ? owner.request : owner.retryNow;
      void request(viewId).catch(() => {});
    },
    olderError: state.error === null ? null : sessionActionError("Couldn't load older turns", state.error),
    olderPermanent: state.permanent,
    findOlder,
    cancelFindOlder,
  };
}
