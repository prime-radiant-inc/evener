import {
  configFingerprint,
  projectThread,
  resolveEffectiveConfig,
  type SessionActivityReadState,
  sessionActionError,
} from "@evener/appwire-client";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useStore } from "zustand";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { EmptyState, type VirtualListHandle } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { VisuallyHidden } from "../../widgets/internal/VisuallyHidden";
import { NOW_TICK_MS, SessionNowContext, useNowTick } from "../session/liveness";
import { LoadOlderRow } from "../session/transcript/flow/LoadOlderRow";
import { restoreTranscriptView } from "../session/transcript/flow/transcriptViewRegistry";
import { useTranscriptScroll } from "../session/transcript/flow/useTranscriptScroll";
import {
  TranscriptBody,
  transcriptAnchorEntriesForRows,
  transcriptRowsForProjection,
  transcriptSourceTurnRowIndexesForRows,
} from "../session/transcript/TranscriptBody";
import type { TranscriptReadView } from "../session/transcript/transcriptReadView";
import { useTranscript } from "../session/transcript/useTranscript";
import styles from "./transcript.module.css";

const CLASS = {
  body: requireClass(styles.body, "transcript.module.css", "body"),
  list: requireClass(styles.list, "transcript.module.css", "list"),
};

export function ReadOnlyThreadContent({
  ref,
  view,
  availability,
}: {
  ref: string;
  view: TranscriptReadView;
  availability?: SessionActivityReadState;
}) {
  const now = useNowTick(NOW_TICK_MS);
  // The existing store owns hydration and reconnect. Each mounted content
  // owns one claim, released independently of the retained view handle.
  useEffect(() => {
    let started = false;
    const tryStart = () => {
      if (started || !view.alive || connectionStore.getState().state !== "ready") return;
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
  }, [ref, view]);

  const { model, loadOlder, loadingOlder, loadOlderReportingError, olderError, cancelOlder } = useTranscript(ref, view);
  const deletedRef = useThreadsStore((state) => state.deletedRefs.has(ref));
  const initialViewCapture = view.getCapture();
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
  const projection = useMemo(() => (model ? projectThread(model, displayConfig) : undefined), [model, displayConfig]);
  const rows = useMemo(() => (projection ? transcriptRowsForProjection(projection) : []), [projection]);
  const anchorEntries = useMemo(() => transcriptAnchorEntriesForRows(rows), [rows]);
  const sourceTurnRowIndexes = useMemo(() => transcriptSourceTurnRowIndexesForRows(rows), [rows]);
  const preparedView = useMemo(
    () => (projection ? { projection, rows, anchorEntries } : undefined),
    [projection, rows, anchorEntries],
  );
  const hasContent = rows.length > 0;
  useLayoutEffect(() => {
    // The child body registers first. Admit its retained placement before the
    // scroll coordinator initializes against a possibly unmeasured port, so
    // its end-follow intent agrees with the restored reader.
    if (hasContent && initialViewCapture) restoreTranscriptView(view.id, initialViewCapture);
  }, [hasContent, initialViewCapture, view.id]);
  useTranscriptScroll({
    ref,
    model,
    listRef,
    loadOlder,
    cancelOlder,
    onReaderIntent: view.supersedePositioning,
    onReaderMovement: view.syncPositioningMovement,
    viewKey: configFingerprint(displayConfig),
    anchorEntries,
    renderedRowCount: rows.length,
    sourceTurnRowIndexes,
    initialViewCapture,
  });

  if (deletedRef) return <EmptyState title="This session was deleted" hint="Its transcript is gone." />;
  if (!model) {
    if (availability?.permanent && availability.unavailable) {
      return (
        <EmptyState
          title="Transcript unavailable"
          hint={sessionActionError("Couldn't load transcript", availability.error)}
        />
      );
    }
    return <EmptyState title="Loading transcript…" />;
  }
  return (
    <SessionNowContext.Provider value={now}>
      <div className={CLASS.body}>
        <div className={CLASS.list}>
          {model.turns.length === 0 ? (
            <EmptyState title="No turns yet" hint="This thread hasn't sent or received anything yet." />
          ) : (
            <TranscriptBody
              model={model}
              config={displayConfig}
              preparedView={preparedView}
              surface="readOnly"
              disclosureScope={view.id}
              sessionRef={ref}
              viewId={view.id}
              initialViewCapture={initialViewCapture}
              readView={view}
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
        </div>
        <div role="status" aria-live="polite" data-testid="transcript-view-announcement">
          <VisuallyHidden key={viewAnnouncement.key}>{viewAnnouncement.text}</VisuallyHidden>
        </div>
      </div>
    </SessionNowContext.Provider>
  );
}
