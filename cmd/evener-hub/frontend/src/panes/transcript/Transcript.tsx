// The read-only transcript pane (wave 8 T6). It renders ANOTHER thread's
// transcript for open-beside viewing (a subagent "open transcript" row, floor
// §3.7) - the M4 transcript engine in read-only mode: the SAME useTranscript /
// TurnBlock / item + tool renderers the live session pane uses, with NO
// composer, NO pending chips, NO session chrome. It is a DISTINCT surface from
// /thread/{ref}'s single-pane mode (which renders the live SESSION pane); this
// one has no URL (routing.ts's paneToURL returns null for it) and is reached
// only contextually via openBeside.
//
// It hydrates through the SAME refcounted threads store as the session pane
// (ensureThread/releaseThread, thread/read - no new data path), so a session
// pane and a transcript pane open on the same ref share one ThreadModel.
//
// Both live and read-only panes now hand their hydrated model to the shared
// TranscriptBody. The read-only surface injects only its older-row affordance
// and deliberately omits live flow-overlay/new-content-pill machinery - but it
// runs the SAME scroll coordinator (useTranscriptScroll) the live pane does,
// so landing at the latest, near-top paging, following a prepend, and the
// geometry fill behind a too-short page all behave identically on both
// surfaces instead of drifting apart in a second implementation.
import { configFingerprint, projectThread, resolveEffectiveConfig } from "@evener/appwire-client";
import { useEffect, useMemo, useRef, useState } from "react";
import { useStore } from "zustand";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { EmptyState, PaneScaffold, type VirtualListHandle } from "../../widgets";
import { VisuallyHidden } from "../../widgets/internal/VisuallyHidden";
import { NOW_TICK_MS, SessionNowContext, useNowTick } from "../session/liveness";
import { LoadOlderRow } from "../session/transcript/flow/LoadOlderRow";
import { useTranscriptScroll } from "../session/transcript/flow/useTranscriptScroll";
import {
  TranscriptBody,
  transcriptAnchorEntriesForRows,
  transcriptRowsForProjection,
  transcriptSourceTurnRowIndexesForRows,
} from "../session/transcript/TranscriptBody";
import { useTranscript } from "../session/transcript/useTranscript";
import { JobLog } from "./JobLog";

export interface TranscriptParams {
  ref: string;
  // The enclosing session's ref, when this pane was opened from a subagent
  // row (subagentModule.tsx's openTranscript - the only producer today).
  // Carried in the pane's OWN params - not passed around as a one-off
  // argument - so it survives a layout restore/reload. A "job:<id>" ref
  // resolves its owning session from it (JobLog fetches output through the
  // owner, never through thread/read). Undefined for a hypothetical future
  // producer with no enclosing session.
  parentRef?: string;
}

export default function Transcript({ params, paneId }: PaneProps<TranscriptParams>) {
  // A "job:<id>" ref is a shell job's output log, not a thread: it renders
  // through the job-log surface, which never touches the thread engine (no
  // thread/read, no ensureThread). Refs never change for a mounted pane, so
  // this dispatch is stable for the component's lifetime.
  if (params.ref.startsWith("job:")) {
    return <JobLog jobRef={params.ref} parentRef={params.parentRef} paneId={paneId} />;
  }
  return <ThreadTranscript params={params} paneId={paneId} />;
}

function ThreadTranscript({ params, paneId }: { params: TranscriptParams; paneId?: string }) {
  const { ref } = params;
  const now = useNowTick(NOW_TICK_MS);

  // ensureThread on mount / releaseThread on unmount, deferred until the one
  // client is actually ready - a deep-linked open can reach this effect before
  // the handshake finishes, and request() rejects until then. Exactly the
  // SessionPane lifecycle (see its own comment); once the single attempt has
  // fired it is never repeated (a reconnect blip is the store's own concern).
  useEffect(() => {
    let started = false;
    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore
        .getState()
        .ensureThread(ref)
        .catch(() => {});
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      if (started) threadsStore.getState().releaseThread(ref);
    };
  }, [ref]);

  // Older-turn paging is automatic here too, and reports a failed page inline
  // with a Retry rather than as a toast - see Session.tsx's own comment on the
  // same wiring. This surface passes the RAW loadOlder to the coordinator (its
  // rejections are best-effort, like the live pane's near-top trigger) and the
  // reporting one to the row, so the row's own geometry fill and its Retry
  // still surface a failure inline.
  const { model, loadOlder, loadingOlder, loadOlderReportingError, olderError, cancelOlder } = useTranscript(
    ref,
    paneId,
  );
  const listRef = useRef<VirtualListHandle>(null);
  const announcementSequence = useRef(0);
  const [viewAnnouncement, setViewAnnouncement] = useState({ text: "", key: 0 });

  const displayViewport = useStore(transcriptDisplayStore, (state) => state.viewport);
  const displayLocal = useStore(transcriptDisplayStore, (state) => state.local[displayViewport]);
  const displayHub = useStore(transcriptDisplayStore, (state) => state.hub[displayViewport]);
  const displayConfig = useMemo(
    () => resolveEffectiveConfig({ local: displayLocal, hub: displayHub, layout: displayViewport }),
    [displayHub, displayLocal, displayViewport],
  );

  // The projection/rows/anchors are derived here, not left to TranscriptBody,
  // for the same reason Session.tsx derives them: the scroll coordinator needs
  // the ROW count and the anchor list, and rows are not turns (a cross-turn
  // intent run coalesces into one row, a turn can split into several). Handing
  // the body the same trio also keeps one derivation per revision.
  const projection = useMemo(() => (model ? projectThread(model, displayConfig) : undefined), [model, displayConfig]);
  const rows = useMemo(() => (projection ? transcriptRowsForProjection(projection) : []), [projection]);
  const anchorEntries = useMemo(() => transcriptAnchorEntriesForRows(rows), [rows]);
  const sourceTurnRowIndexes = useMemo(() => transcriptSourceTurnRowIndexesForRows(rows), [rows]);
  const preparedView = useMemo(
    () => (projection ? { projection, rows, anchorEntries } : undefined),
    [projection, rows, anchorEntries],
  );

  // The same scroll coordinator the live pane runs: it lands at the end on
  // open, pages near the top, follows a prepend without stranding the reader
  // above the latest, and owns the stick-to-bottom bookkeeping. The read-only
  // surface renders none of the pill/ask-dock UI it also feeds.
  useTranscriptScroll({
    ref,
    model,
    listRef,
    loadOlder,
    cancelOlder,
    viewKey: configFingerprint(displayConfig),
    anchorEntries,
    renderedRowCount: rows.length,
    sourceTurnRowIndexes,
  });

  if (!model) {
    return (
      <PaneScaffold title={ref}>
        <EmptyState title="Loading transcript…" />
      </PaneScaffold>
    );
  }

  const content = (
    <PaneScaffold title={model.name || ref}>
      {model.turns.length === 0 ? (
        <EmptyState title="No turns yet" hint="This thread hasn't sent or received anything yet." />
      ) : (
        <TranscriptBody
          model={model}
          config={displayConfig}
          preparedView={preparedView}
          surface="readOnly"
          disclosureScope={`transcript:readOnly:${ref}`}
          sessionRef={ref}
          viewId={paneId}
          onAnnounceViewChange={(summary) => {
            announcementSequence.current += 1;
            setViewAnnouncement({ text: `Transcript detail: ${summary}`, key: announcementSequence.current });
          }}
          loadOlderRow={
            model.olderCursor && (
              <LoadOlderRow
                onLoad={loadOlderReportingError}
                loading={loadingOlder}
                error={olderError}
                scrollElement={() => listRef.current?.getScrollElement() ?? null}
              />
            )
          }
          listRef={listRef}
        />
      )}
      <div role="status" aria-live="polite" data-testid="transcript-view-announcement">
        <VisuallyHidden key={viewAnnouncement.key}>{viewAnnouncement.text}</VisuallyHidden>
      </div>
    </PaneScaffold>
  );
  return <SessionNowContext.Provider value={now}>{content}</SessionNowContext.Provider>;
}
